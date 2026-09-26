// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpinesnap

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestRemainingLength(t *testing.T) {
	tests := []struct {
		Name string
		N    int
		Want []byte
	}{
		{Name: "Zero", N: 0, Want: []byte{0}},
		{Name: "OneByteMax", N: 127, Want: []byte{0x7f}},
		{Name: "TwoByteMin", N: 128, Want: []byte{0x80, 0x01}},
		{Name: "TwoByteMax", N: 16383, Want: []byte{0xff, 0x7f}},
		{Name: "ThreeByteMin", N: 16384, Want: []byte{0x80, 0x80, 0x01}},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			if got := appendRemainingLength(nil, tc.N); !bytes.Equal(got, tc.Want) {
				t.Errorf("appendRemainingLength() = %x, want %x", got, tc.Want)
			}
			pkt := append(append([]byte{0x30}, tc.Want...), make([]byte, tc.N)...)
			typ, body, err := readPacket(bufio.NewReader(bytes.NewReader(pkt)))
			if err != nil || typ != 0x30 || len(body) != tc.N {
				t.Errorf("readPacket() = (%x, %d bytes, %v)", typ, len(body), err)
			}
		})
	}
}

func TestPackets(t *testing.T) {
	if diff := cmp.Diff([]byte{0x10, 17, 0, 4, 'M', 'Q', 'T', 'T', 4, 2, 0, 60, 0, 5, 'a', 'b', 'c', 'd', 'e'}, connectPacket("abcde", 60)); diff != "" {
		t.Errorf("connectPacket() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]byte{0x82, 8, 0, 1, 0, 3, 'a', '/', '#', 0}, subscribePacket(1, []string{"a/#"})); diff != "" {
		t.Errorf("subscribePacket() mismatch (-want +got):\n%s", diff)
	}
	m, err := parsePublish(0x31, []byte{0, 3, 'x', '/', 'y', 'h', 'i'})
	if err != nil || m.Topic != "x/y" || string(m.Payload) != "hi" || !m.Retained {
		t.Errorf("parsePublish() = (%+v, %v)", m, err)
	}
	if _, err := parsePublish(0x30, []byte{0, 9, 'x'}); err == nil {
		t.Error("parsePublish() accepted a short topic")
	}
}

// serveBroker runs script against the first connection to a loopback
// listener and returns the listener's address.
func serveBroker(t *testing.T, script func(c net.Conn, r *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		script(c, bufio.NewReader(c))
	}()
	return ln.Addr().String()
}

func TestSubscribe(t *testing.T) {
	addr := serveBroker(t, func(c net.Conn, r *bufio.Reader) {
		if typ, _, err := readPacket(r); err != nil || typ != 0x10 {
			t.Errorf("broker: got packet %x (%v), want CONNECT", typ, err)
			return
		}
		c.Write([]byte{0x20, 2, 0, 0})
		typ, body, err := readPacket(r)
		if err != nil || typ != 0x82 {
			t.Errorf("broker: got packet %x (%v), want SUBSCRIBE", typ, err)
			return
		}
		c.Write([]byte{0x90, 3, body[0], body[1], 0})
		pub := func(typ byte, topic, payload string) {
			c.Write(packet(typ, append(appendString(nil, topic), payload...)))
		}
		pub(0x31, "build/build-3-24-x86_64", "idle")
		pub(0x30, "rsync/dl-master.alpinelinux.org/v3.24/x86_64", "v3.24/main/x86_64")
		time.Sleep(100 * time.Millisecond)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []Message
	err := Subscribe(ctx, addr, "test", []string{"build/#", "rsync/#"}, time.Minute, func(m Message) { got = append(got, m) })
	if err == nil {
		t.Error("Subscribe() = nil after the broker hung up, want error")
	}
	want := []Message{
		{Topic: "build/build-3-24-x86_64", Payload: []byte("idle"), Retained: true},
		{Topic: "rsync/dl-master.alpinelinux.org/v3.24/x86_64", Payload: []byte("v3.24/main/x86_64")},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("messages mismatch (-want +got):\n%s", diff)
	}
}

func TestSubscribeRefused(t *testing.T) {
	addr := serveBroker(t, func(c net.Conn, r *bufio.Reader) {
		readPacket(r)
		c.Write([]byte{0x20, 2, 0, 5}) // not authorized
	})
	err := Subscribe(context.Background(), addr, "test", []string{"#"}, time.Minute, func(Message) {})
	if err == nil || !strings.Contains(err.Error(), "return code 5") {
		t.Errorf("Subscribe() = %v, want refusal with return code 5", err)
	}
}
