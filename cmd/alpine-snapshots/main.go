// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// The alpine-snapshots binary archives Alpine package repositories so that
// their past states can be served again. See internal/alpinesnap and
// pkg/registry/alpine/snapshot.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/oss-rebuild/internal/alpinesnap"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/pkg/errors"
)

var (
	public       = flag.String("public", "", "published archive: gs://bucket[/prefix] or a directory")
	state        = flag.String("state", "", "private state, the database backups: gs://bucket[/prefix] or a directory")
	dbDir        = flag.String("db-dir", "/var/lib/alpine-snapshots", "local directory for the live database and downloads in flight")
	repositories = flag.String("repositories", "", "comma-separated branch/repo/arch repositories to archive, e.g. v3.24/main/x86_64")
	mqtt         = flag.String("mqtt", alpinesnap.DefaultMQTT, "MQTT broker whose upload notices trigger polls and whose builder statuses are recorded, empty to only poll")
	every        = flag.Duration("every", 5*time.Minute, "poll interval without upload notices")
	port         = flag.Int("port", 8080, "port serving /healthz, 0 to disable")
	once         = flag.Bool("once", false, "poll once, archive what that finds, back up and exit")
)

func run(ctx context.Context) error {
	if *public == "" || *state == "" || *repositories == "" {
		return errors.New("--public, --state and --repositories are required")
	}
	var repos []alpine.Repository
	for s := range strings.SplitSeq(*repositories, ",") {
		r, err := alpine.ParseRepository(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		repos = append(repos, r)
	}
	// The filesystems outlive ctx, which a signal cancels, for the last backup.
	res := billyx.NewResolver()
	pub, err := res.DirFS(context.WithoutCancel(ctx), *public)
	if err != nil {
		return err
	}
	st, err := res.DirFS(context.WithoutCancel(ctx), *state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dbDir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(*dbDir, "apksnap.sqlite")
	backup, err := alpinesnap.Restore(st, dbPath)
	if err != nil {
		return errors.Wrap(err, "restoring database")
	}
	log.Printf("restored backup %d", backup)
	db, err := alpinesnap.OpenDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	client := &httpx.WithUserAgent{BasicClient: &http.Client{Timeout: 30 * time.Minute}, UserAgent: "oss-rebuild-alpine-snapshots (+https://github.com/google/oss-rebuild)"}
	a := &alpinesnap.Archiver{
		DB:           db,
		Public:       pub,
		State:        st,
		TmpDir:       filepath.Join(*dbDir, "tmp"),
		Repositories: repos,
		IndexMirror:  alpine.Mirror{Client: client, URL: alpine.OriginMirror},
		Mirrors:      []alpine.Mirror{{Client: client, URL: alpine.CDNMirror}, {Client: client, URL: alpine.OriginMirror}},
		BuildLogs:    alpine.BuildLogs{Client: client, URL: alpine.BuildLogHost},
		MQTT:         *mqtt,
		Every:        *every,
		Backup:       backup,
	}
	if *once {
		return a.Once(ctx)
	}
	if *port != 0 {
		go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), a)) }()
	}
	log.Printf("archiving %v into %s", repos, *public)
	return a.Run(ctx)
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatalf("alpine-snapshots: %v", err)
	}
}
