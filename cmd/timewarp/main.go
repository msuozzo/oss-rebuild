// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// The timewarp binary serves the registry timewarp HTTP handler on a local port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/google/oss-rebuild/internal/gcsx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/timewarp"
)

var (
	port    = flag.Int("port", 8081, "port on which to serve")
	gcsAuth = flag.Bool("gcs-auth", false, "read GCS objects, such as a private Alpine snapshot archive, with application default credentials, serving on localhost only")
)

func main() {
	flag.Parse()
	var client httpx.BasicClient = http.DefaultClient
	var host string
	if *gcsAuth {
		c, err := gcsx.NewReadAuthClient(context.Background(), client)
		if err != nil {
			log.Fatalf("Creating GCS client: %v", err)
		}
		client = c
		// Other hosts must not read with these credentials.
		host = "127.0.0.1"
	}
	log.Printf("Server listening on port %d", *port)
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", host, *port), timewarp.Handler{Client: client}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
