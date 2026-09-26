// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package alpinesnap archives Alpine package repositories: every index a
// mirror publishes, every package those indexes list and the official build
// log of every origin version, in the public layout of
// pkg/registry/alpine/snapshot.
//
// A private SQLite database tracks what was observed and what remains to
// be fetched. It lives on local disk and is backed up to a private state
// store, from which a replacement instance restores it.
package alpinesnap

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// backupName is the name of the n-th database backup in the state
// filesystem, gzipped per sqlitex. Each backup is created under the next
// number and never overwritten, which is how archivers claim the archive
// (see Archiver).
func backupName(n int64) string { return fmt.Sprintf("db/%010d.sqlite.gz", n) }

const (
	keepBackups = 100              // about a day's worth, to recover from a bad one
	backupEvery = 15 * time.Minute // longest a change waits for a backup, besides new uploads
	workers     = 4                // concurrent package downloads
)

// logGap is the minimum time between build log requests.
var logGap = time.Second / 2

// exclusive creates files only if they do not exist, as backups must be.
type exclusive struct{ billy.Filesystem }

func (e exclusive) Create(name string) (billy.File, error) {
	return e.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o644)
}

// ErrClaimLost reports that another archiver claimed the archive.
var ErrClaimLost = errors.New("another archiver claimed the archive")

// Archiver collects Alpine repositories into an archive. A poll loop reads
// each repository's index when the broker announces an upload, or every
// Every without one, and records new uploads. A fetch loop archives the
// packages and build logs they reference.
//
// Only one archiver may write an archive. An archiver restores its database
// from the latest backup (Restore) and claims the archive by creating the
// next backup. It stops when that next number is taken, as another
// archiver's claim does, so the newest archiver wins and at most one keeps
// writing after the next poll. Package and log writes are create-only, so
// the overlap cannot corrupt them.
type Archiver struct {
	DB           *DB
	Public       billy.Filesystem // the published archive
	State        billy.Filesystem // private state: the database backups
	TmpDir       string
	Repositories []alpine.Repository
	// IndexMirror is polled for indexes. It should be the origin, which has
	// an index the moment the builder's upload finishes.
	IndexMirror alpine.Mirror
	// Mirrors are tried in order for packages. The CDN first takes load off
	// the origin and may still serve a package the origin deleted.
	Mirrors   []alpine.Mirror
	BuildLogs alpine.BuildLogs
	// MQTT is the broker whose upload notices trigger polls and whose
	// builder statuses are recorded, or "" to only poll every Every.
	MQTT string
	// Every is the poll interval without notices. It bounds the time between
	// two unannounced uploads of a repository that the archive can tell apart.
	Every time.Duration
	// Backup is the number of the backup the database was restored from, or
	// zero for a new database.
	Backup     int64
	mu         sync.Mutex // guards startedAt and polledAt
	startedAt  time.Time
	polledAt   time.Time // the last poll of every repository without error
	backedUp   int64     // DB.TotalChanges at the last backup
	lastBackup time.Time
	trigger    chan struct{} // an upload notice arrived
}

// minPollGap spaces polls, so that a burst of notices polls once. Uploads
// further apart are told apart.
const minPollGap = 15 * time.Second

// Restore replaces the database at path with the latest backup in st, if
// there is one, and returns its number. A local database from an earlier
// run is never reused, since another archiver may have written newer
// backups since.
func Restore(st billy.Filesystem, path string) (int64, error) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}
	entries, err := st.ReadDir("db")
	if err != nil && !os.IsNotExist(err) {
		return 0, errors.Wrap(err, "listing backups")
	}
	var n int64
	for _, e := range entries {
		if v, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".sqlite.gz"), 10, 64); err == nil {
			n = max(n, v)
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, sqlitex.Fetch(st, backupName(n), path)
}

// Run claims the archive and archives until ctx is done, then writes a last
// backup. It returns ErrClaimLost when another archiver claims the archive.
// The filesystems' own contexts must outlive ctx for the last backup.
func (a *Archiver) Run(ctx context.Context) error {
	if err := a.start(); err != nil {
		return err
	}
	wake := make(chan struct{}, 1)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.pollLoop(gctx, wake) })
	g.Go(func() error { return a.fetchLoop(gctx, wake) })
	if a.MQTT != "" {
		g.Go(func() error { return a.listenMQTT(gctx) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return a.backup()
}

// Once claims the archive, polls once, archives everything due and backs up.
func (a *Archiver) Once(ctx context.Context) error {
	if err := a.start(); err != nil {
		return err
	}
	a.poll(ctx)
	for a.fetchPackages(ctx)+a.fetchLogs(ctx) > 0 {
	}
	if err := a.backup(); err != nil {
		return err
	}
	return a.publishUploads()
}

func (a *Archiver) start() error {
	a.mu.Lock()
	a.startedAt = time.Now()
	a.mu.Unlock()
	a.trigger = make(chan struct{}, 1)
	if err := os.MkdirAll(a.TmpDir, 0o755); err != nil {
		return err
	}
	if err := a.backup(); err != nil {
		return errors.Wrap(err, "claiming the archive")
	}
	return a.publishUploads()
}

// pollLoop polls on upload notices and every Every, and backs up. It
// returns ErrClaimLost when the backup moves under it.
func (a *Archiver) pollLoop(ctx context.Context, wake chan<- struct{}) error {
	for {
		last := time.Now()
		if _, err := a.State.Stat(backupName(a.Backup + 1)); err == nil {
			return ErrClaimLost
		}
		changed := a.poll(ctx)
		if changed {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		if changed || (a.DB.TotalChanges() != a.backedUp && time.Since(a.lastBackup) >= backupEvery) {
			if err := a.backup(); errors.Is(err, ErrClaimLost) {
				return err
			} else if err != nil {
				logError(err)
			}
		}
		if changed {
			if err := a.publishUploads(); err != nil {
				logError(errors.Wrap(err, "publishing uploads"))
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.Every):
		case <-a.trigger:
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Until(last.Add(minPollGap))):
			}
		}
	}
}

// listenMQTT subscribes to the archived builders' statuses and upload
// notices until ctx is done, reconnecting with backoff.
func (a *Archiver) listenMQTT(ctx context.Context) error {
	var topics []string
	for _, r := range a.Repositories {
		for _, t := range []string{"build/" + r.Builder(), "rsync/dl-master.alpinelinux.org/" + r.Branch + "/" + r.Arch} {
			if !slices.Contains(topics, t) {
				topics = append(topics, t)
			}
		}
	}
	host, _ := os.Hostname()
	clientID := fmt.Sprintf("oss-rebuild-alpine-snapshots-%s-%d", host, time.Now().Unix())
	for wait := 5 * time.Second; ctx.Err() == nil; wait = min(wait*2, 5*time.Minute) {
		start := time.Now()
		err := Subscribe(ctx, a.MQTT, clientID, topics, time.Minute, a.onMessage)
		if ctx.Err() != nil {
			break
		}
		if time.Since(start) > 10*time.Minute {
			wait = 5 * time.Second
		}
		log.Printf("mqtt: %v (reconnecting in %s)", err, wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return nil
}

// onMessage records a broker message and triggers a poll on an upload
// notice for an archived repository, whose payload is
// "<branch>/<repo>/<arch>".
func (a *Archiver) onMessage(m Message) {
	now := time.Now()
	m.Payload = bytes.TrimSpace(m.Payload)
	if err := a.DB.InsertMQTTEvent(now.Unix(), m); err != nil {
		logError(errors.Wrap(err, "recording mqtt message"))
	}
	if !strings.HasPrefix(m.Topic, "rsync/") || !slices.ContainsFunc(a.Repositories, func(r alpine.Repository) bool { return r.String() == string(m.Payload) }) {
		return
	}
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

// poll collects every repository's index and reports whether any changed.
func (a *Archiver) poll(ctx context.Context) (changed bool) {
	ok := true
	for _, r := range a.Repositories {
		c, err := a.pollOne(ctx, r)
		if err != nil {
			ok = false
			logError(errors.Wrapf(err, "polling %s", r))
		}
		changed = changed || c
	}
	if ok {
		a.mu.Lock()
		a.polledAt = time.Now()
		a.mu.Unlock()
	}
	return changed
}

// pollOne records r's index if it changed since the last observation.
func (a *Archiver) pollOne(ctx context.Context, r alpine.Repository) (bool, error) {
	prev, err := a.DB.LatestObservation(r)
	if err != nil {
		return false, err
	}
	var etag, lm string
	if prev != nil {
		etag, lm = prev.ETag, prev.LastModified
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := a.IndexMirror.Index(ctx, r, etag, lm)
	if err != nil {
		return false, err
	}
	// NOTE: Some servers ignore conditional headers.
	if resp.NotModified || (prev != nil && prev.ETag == resp.ETag && prev.LastModified == resp.LastModified) {
		return false, nil
	}
	idx, err := apk.ParseIndex(bytes.NewReader(resp.Body))
	if err != nil {
		return false, errors.Wrap(err, "parsing index")
	}
	sum := sha256.Sum256(resp.Body)
	sha := hex.EncodeToString(sum[:])
	if err := putOnce(a.Public, snapshot.BlobObject(sha), bytes.NewReader(resp.Body), nil); err != nil {
		return false, err
	}
	log.Printf("%s: new index %q (%s), %d packages", r, idx.Description, resp.LastModified, len(idx.Records))
	return true, a.DB.Ingest(Observation{
		Repository:   r,
		ObservedAt:   time.Now().Unix(),
		LastModified: resp.LastModified,
		ETag:         resp.ETag,
		SHA256:       sha,
		Size:         int64(len(resp.Body)),
		Description:  idx.Description,
		NPackages:    int64(len(idx.Records)),
	}, idx.Records)
}

// put writes a file, setting its object metadata with attrs on GCS. With
// create, it fails with fs.ErrExist if the file exists.
func put(fsys billy.Filesystem, name string, r io.Reader, create bool, attrs func(*storage.ObjectAttrs)) error {
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if create {
		flag |= os.O_EXCL
	}
	f, err := fsys.OpenFile(name, flag, 0o644)
	if err != nil {
		return err
	}
	if o, ok := f.(billyx.ObjectAttrsFile); ok && attrs != nil {
		attrs(o.ObjectAttrs())
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return errors.Wrapf(err, "writing %s", name)
	}
	return f.Close()
}

// putOnce writes an immutable file unless it exists.
func putOnce(fsys billy.Filesystem, name string, r io.Reader, attrs func(*storage.ObjectAttrs)) error {
	if err := put(fsys, name, r, true, attrs); !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// fetchLoop archives due packages and logs, waiting for a new upload or a
// minute when none is due.
func (a *Archiver) fetchLoop(ctx context.Context, wake <-chan struct{}) error {
	for ctx.Err() == nil {
		if a.fetchPackages(ctx)+a.fetchLogs(ctx) == 0 {
			select {
			case <-ctx.Done():
			case <-wake:
			case <-time.After(time.Minute):
			}
		}
	}
	return nil
}

// backoff doubles from base per attempt, capped at limit.
func backoff(attempts int64, base, limit time.Duration) time.Duration {
	return min(base<<min(attempts-1, 20), limit)
}

// fetchPackages archives a batch of due packages and returns its size.
func (a *Archiver) fetchPackages(ctx context.Context) int {
	due, err := a.DB.DuePackages(time.Now().Unix(), 16*workers)
	if err != nil {
		logError(errors.Wrap(err, "reading package queue"))
		return 0
	}
	var g errgroup.Group
	g.SetLimit(workers)
	for _, q := range due {
		g.Go(func() error {
			a.archivePackage(ctx, q)
			return nil
		})
	}
	g.Wait()
	return len(due)
}

func (a *Archiver) archivePackage(ctx context.Context, q QueuedPackage) {
	var errs []string
	notFound := 0
	for _, m := range a.Mirrors {
		sha, err := a.fetchFrom(ctx, m, q)
		if err == nil {
			if err := a.DB.PackageDone(q.ID, sha, 0, false, ""); err != nil {
				logError(err)
			}
			return
		}
		if alpine.IsNotFound(err) {
			notFound++
		}
		errs = append(errs, err.Error())
	}
	if ctx.Err() != nil {
		return // interrupted, not failed
	}
	// A package every mirror deleted after it was superseded is gone for
	// good. A listed package can 404 briefly while the builder's upload is
	// still running, since the index is uploaded first.
	attempts := q.Attempts + 1
	gone := (notFound == len(a.Mirrors) && !q.Listed) || attempts >= 20
	next := time.Now().Add(backoff(attempts, time.Minute, 6*time.Hour)).Unix()
	if err := a.DB.PackageDone(q.ID, "", next, gone, strings.Join(errs, "; ")); err != nil {
		logError(err)
	}
	log.Printf("%s: %s not archived (attempt %d, gone=%v): %s", q.Repository, q.Filename(), attempts, gone, strings.Join(errs, "; "))
}

// fetchFrom downloads, verifies and stores a package and returns its
// SHA-256.
func (a *Archiver) fetchFrom(ctx context.Context, m alpine.Mirror, q QueuedPackage) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	body, err := m.Package(ctx, q.Repository, q.Filename())
	if err != nil {
		return "", err
	}
	defer body.Close()
	// Packages reach 1.4 GB, so they go through a file rather than memory.
	// Verification reads the download to its end, so it also catches a
	// truncated one.
	f, err := os.CreateTemp(a.TmpDir, "fetch-*.apk")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sh := sha256.New()
	if err := apk.Verify(io.TeeReader(body, io.MultiWriter(f, sh)), q.Checksum); err != nil {
		return "", errors.Wrapf(err, "verifying %s", q.Filename())
	}
	n, _ := f.Seek(0, io.SeekCurrent)
	sha := hex.EncodeToString(sh.Sum(nil))
	name := snapshot.PackageObject(q.Repository, q.Filename())
	f.Seek(0, io.SeekStart)
	if err := put(a.Public, name, f, true, nil); !errors.Is(err, fs.ErrExist) {
		return sha, err
	}
	// The path is taken, by these bytes from an earlier attempt or, rarely,
	// by a file Alpine replaced without a release bump (see the snapshot
	// package), whose size will differ but for a coincidence.
	st, err := a.Public.Stat(name)
	if err != nil || st.Size() == n {
		return sha, err
	}
	log.Printf("%s: %s holds other bytes, keeping %s", q.Repository, name, snapshot.BlobObject(sha))
	f.Seek(0, io.SeekStart)
	return sha, putOnce(a.Public, snapshot.BlobObject(sha), f, nil)
}

// fetchLogs archives a batch of due build logs, LogGap apart, and returns
// its size.
func (a *Archiver) fetchLogs(ctx context.Context) int {
	due, err := a.DB.DueLogs(time.Now().Unix(), 16)
	if err != nil {
		logError(errors.Wrap(err, "reading log queue"))
		return 0
	}
	for _, l := range due {
		if ctx.Err() != nil {
			break
		}
		a.archiveLog(ctx, l)
		time.Sleep(logGap)
	}
	return len(due)
}

func (a *Archiver) archiveLog(ctx context.Context, l QueuedLog) {
	name := snapshot.LogObject(l.Builder, l.Repo, l.Origin, l.Version)
	err := a.storeLog(ctx, l, name)
	if ctx.Err() != nil {
		return
	}
	state, retry := LogArchived, time.Duration(0)
	attempts := l.Attempts + 1
	switch {
	case err == nil:
	case alpine.IsNotFound(err):
		// A log is complete before its package is uploaded, so a 404 is final
		// unless the build is recent and its package raced the log. Builds
		// from before a branch's builder existed have no log on it.
		state, retry = LogMissing, 0
		if l.BuildDate > time.Now().Add(-48*time.Hour).Unix() && attempts < 5 {
			state, retry = LogPending, backoff(attempts, 10*time.Minute, 6*time.Hour)
		}
	case attempts >= 12:
		state = LogFailed
	default:
		state, retry = LogPending, backoff(attempts, 5*time.Minute, 12*time.Hour)
	}
	var object, msg string
	if err == nil {
		object = name
	} else {
		msg = err.Error()
		log.Printf("build log %s: %v", name, err)
	}
	if err := a.DB.LogDone(l.ID, state, object, time.Now().Add(retry).Unix(), msg); err != nil {
		logError(err)
	}
}

// storeLog fetches a build log and stores it gzip-compressed.
func (a *Archiver) storeLog(ctx context.Context, l QueuedLog, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	text, err := a.BuildLogs.Get(ctx, l.Builder, l.Repo, l.Origin, l.Version)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(text)
	zw.Close()
	// GCS serves the log decompressed to clients that do not accept gzip.
	return putOnce(a.Public, name, &buf, func(o *storage.ObjectAttrs) {
		o.ContentType, o.ContentEncoding = "text/plain; charset=utf-8", "gzip"
	})
}

// backup writes a consistent copy of the database to the state
// filesystem as the next backup.
func (a *Archiver) backup() error {
	changes := a.DB.TotalChanges()
	tmp := filepath.Join(a.TmpDir, fmt.Sprintf("backup-%d.sqlite", time.Now().UnixNano()))
	defer os.Remove(tmp)
	if err := a.DB.Backup(tmp); err != nil {
		return err
	}
	next := a.Backup + 1
	if err := sqlitex.Publish(exclusive{a.State}, backupName(next), tmp); errors.Is(err, fs.ErrExist) {
		return ErrClaimLost
	} else if err != nil {
		return err
	}
	if next > keepBackups {
		a.State.Remove(backupName(next - keepBackups))
	}
	a.Backup, a.backedUp, a.lastBackup = next, changes, time.Now()
	return nil
}

// publishUploads writes the upload table to the public archive.
func (a *Archiver) publishUploads() error {
	obs, err := a.DB.Observations()
	if err != nil {
		return err
	}
	var u snapshot.Uploads
	for _, o := range obs {
		lm := o.ObservedAt
		if t, err := http.ParseTime(o.LastModified); err == nil {
			lm = t.Unix()
		}
		u = append(u, snapshot.Upload{
			Branch:       o.Repository.Branch,
			Repo:         o.Repository.Repo,
			Arch:         o.Repository.Arch,
			LastModified: lm,
			ObservedAt:   o.ObservedAt,
			SHA256:       o.SHA256,
			Size:         o.Size,
			NPackages:    o.NPackages,
			Description:  o.Description,
		})
	}
	slices.SortStableFunc(u, func(x, y snapshot.Upload) int { return cmp.Compare(x.LastModified, y.LastModified) })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, up := range u {
		if err := enc.Encode(up); err != nil {
			return err
		}
	}
	// NOTE: GCS lets caches keep public objects for an hour by default,
	// which would hide new uploads. Gzip encoding would disable ranges.
	return put(a.Public, snapshot.UploadsObject, &buf, false, func(o *storage.ObjectAttrs) {
		o.ContentType, o.CacheControl = "application/jsonl", "no-cache"
	})
}

// ServeHTTP serves /healthz, healthy while every repository was polled
// within three poll intervals, or since the archiver started.
func (a *Archiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	last := a.polledAt
	if last.IsZero() {
		last = a.startedAt
	}
	a.mu.Unlock()
	if age := time.Since(last); !last.IsZero() && age > 3*a.Every {
		http.Error(w, fmt.Sprintf("no successful poll for %s", age.Round(time.Second)), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func logError(err error) { log.Printf("error: %v", err) }
