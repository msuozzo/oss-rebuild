// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpinesnap

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// DefaultMQTT is Alpine's message broker. Builders publish their status on
// build/<hostname>, and the origin mirror announces each upload on
// rsync/dl-master.alpinelinux.org/<branch>/<arch> with payload
// "<branch>/<repo>/<arch>" about two seconds after the index's mtime
// (observed 2026-09-23).
const DefaultMQTT = "msg.alpinelinux.org:1883"

// Message is one MQTT PUBLISH received from the broker.
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

// MQTT packet types, from MQTT 3.1.1 section 2.2.1.
// See https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html.
const (
	mqttConnect   = 1
	mqttConnAck   = 2
	mqttPublish   = 3
	mqttSubscribe = 8
	mqttSubAck    = 9
	mqttPingReq   = 12
	mqttPingResp  = 13
)

// Subscribe connects to an MQTT 3.1.1 broker at addr (host:port),
// subscribes to topics and delivers messages to fn until the connection
// fails or ctx is done. It returns the error that ended the session.
//
// This is a minimal subscribe-only client: plain TCP, clean session, no
// credentials and QoS 0 subscriptions, which is all Alpine's anonymous
// broker offers and needs. A broker delivers at most at the subscription's
// QoS (MQTT 3.1.1 section 3.8.4), so no message needs an acknowledgement.
func Subscribe(ctx context.Context, addr, clientID string, topics []string, keepalive time.Duration, fn func(Message)) error {
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return errors.Wrap(err, "connecting to broker")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var wmu sync.Mutex
	write := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, err := conn.Write(b)
		return err
	}
	if err := write(connectPacket(clientID, uint16(keepalive/time.Second))); err != nil {
		return errors.Wrap(err, "sending CONNECT")
	}
	r := bufio.NewReader(conn)
	// The broker disconnects a client silent for 1.5x keepalive. Pings go
	// out every keepalive/2, so some packet arrives at least that often.
	readDeadline := func() { conn.SetReadDeadline(time.Now().Add(keepalive + keepalive/2)) }
	readDeadline()
	typ, body, err := readPacket(r)
	if err != nil {
		return errors.Wrap(err, "reading CONNACK")
	}
	if typ>>4 != mqttConnAck || len(body) != 2 {
		return errors.Errorf("expected CONNACK, got packet type %d", typ>>4)
	}
	if body[1] != 0 {
		return errors.Errorf("connection refused with return code %d", body[1])
	}
	if err := write(subscribePacket(1, topics)); err != nil {
		return errors.Wrap(err, "sending SUBSCRIBE")
	}
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		t := time.NewTicker(keepalive / 2)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if write([]byte{mqttPingReq << 4, 0}) != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	for {
		readDeadline()
		typ, body, err := readPacket(r)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.Wrap(err, "reading packet")
		}
		switch typ >> 4 {
		case mqttPublish:
			m, err := parsePublish(typ, body)
			if err != nil {
				return err
			}
			fn(m)
		case mqttSubAck:
			if len(body) < 3 {
				return errors.New("short SUBACK")
			}
			for i, rc := range body[2:] {
				if rc == 0x80 && i < len(topics) {
					return errors.Errorf("subscription to %q refused", topics[i])
				}
			}
		case mqttPingResp:
		}
	}
}

func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

// appendRemainingLength appends MQTT's variable-length integer encoding.
func appendRemainingLength(b []byte, n int) []byte {
	for {
		c := byte(n % 128)
		n /= 128
		if n > 0 {
			c |= 0x80
		}
		b = append(b, c)
		if n == 0 {
			return b
		}
	}
}

func packet(typ byte, body []byte) []byte {
	return append(appendRemainingLength([]byte{typ}, len(body)), body...)
}

func connectPacket(clientID string, keepaliveSec uint16) []byte {
	body := appendString(nil, "MQTT")
	body = append(body, 4, 0x02) // protocol level 4 (3.1.1), clean session
	body = binary.BigEndian.AppendUint16(body, keepaliveSec)
	body = appendString(body, clientID)
	return packet(mqttConnect<<4, body)
}

func subscribePacket(id uint16, topics []string) []byte {
	body := binary.BigEndian.AppendUint16(nil, id)
	for _, t := range topics {
		body = appendString(body, t)
		body = append(body, 0) // QoS 0
	}
	// SUBSCRIBE has the fixed header flags 0010, MQTT 3.1.1 section 3.8.1.
	return packet(mqttSubscribe<<4|0x02, body)
}

// readPacket reads one MQTT control packet and returns its first byte and
// body.
func readPacket(r *bufio.Reader) (byte, []byte, error) {
	typ, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(c&0x7f) * mult
		if c&0x80 == 0 {
			break
		}
		if i == 3 {
			return 0, nil, errors.New("malformed remaining length")
		}
		mult *= 128
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return typ, body, nil
}

// parsePublish parses a QoS 0 PUBLISH, which has no packet id.
func parsePublish(typ byte, body []byte) (Message, error) {
	if len(body) < 2 || len(body) < 2+int(binary.BigEndian.Uint16(body)) {
		return Message{}, errors.New("short PUBLISH")
	}
	tl := int(binary.BigEndian.Uint16(body))
	return Message{Topic: string(body[2 : 2+tl]), Payload: body[2+tl:], Retained: typ&1 == 1}, nil
}
