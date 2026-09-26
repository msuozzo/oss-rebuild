// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpinesnap

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk/apktest"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// fakeAlpine serves an origin mirror (/origin), a CDN mirror (/cdn) and a
// build log host (/logs).
type fakeAlpine struct {
	index    []byte
	mtime    time.Time
	apks     map[string][]byte // on both mirrors
	cdnOnly  map[string][]byte
	logs     map[string]string // path below /logs/ to text
	requests map[string]int
}

func (f *fakeAlpine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests[r.URL.Path]++
	site, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	file := path.Base(rest)
	switch {
	case site == "origin" && file == "APKINDEX.tar.gz":
		w.Header().Set("ETag", `"`+f.mtime.Format("150405")+`"`)
		http.ServeContent(w, r, file, f.mtime, bytes.NewReader(f.index))
	case (site == "origin" || site == "cdn") && f.apks[file] != nil:
		w.Write(f.apks[file])
	case site == "cdn" && f.cdnOnly[file] != nil:
		w.Write(f.cdnOnly[file])
	case site == "logs" && f.logs[rest] != "":
		io.WriteString(w, f.logs[rest])
	default:
		http.NotFound(w, r)
	}
}

func readObject(t *testing.T, fsys billy.Filesystem, name string) []byte {
	t.Helper()
	b, err := util.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return b
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newTestArchiver(t *testing.T, srv *httptest.Server) *Archiver {
	t.Helper()
	logGap = 0
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "apksnap.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mirror := func(site string) alpine.Mirror {
		return alpine.Mirror{Client: srv.Client(), URL: urlx.MustParse(srv.URL + "/" + site + "/alpine/")}
	}
	return &Archiver{
		DB:           db,
		Public:       osfs.New(filepath.Join(dir, "public")),
		State:        osfs.New(filepath.Join(dir, "state")),
		TmpDir:       filepath.Join(dir, "tmp"),
		Repositories: []alpine.Repository{{Branch: "v3.24", Repo: "main", Arch: "x86_64"}},
		IndexMirror:  mirror("origin"),
		Mirrors:      []alpine.Mirror{mirror("origin"), mirror("cdn")},
		BuildLogs:    alpine.BuildLogs{Client: srv.Client(), URL: urlx.MustParse(srv.URL + "/logs/")},
		Every:        time.Minute,
	}
}

func logText(origin, version string) string {
	return ">>> " + origin + ": Building main/" + origin + " " + version + " (using abuild 3.17.0-r0) started Tue, 22 Sep 2026 16:00:00 +0000\n"
}

func TestArchiverOnce(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	a1 := apktest.NewPackage("a", "1.0-r0", []byte("a one"))
	a1doc := apktest.NewPackage("a-doc", "1.0-r0", []byte("a one docs"))
	b2 := apktest.NewPackage("b", "2.0-r0", []byte("b two"))
	c1 := apktest.NewPackage("c", "1.0-r0", []byte("c one"))
	d1 := apktest.NewPackage("d", "1.0-r0", []byte("d one"))
	fake := &fakeAlpine{
		index: apktest.Index("v20260805-1-gaaaaaaaaaaa",
			apktest.Entry{Package: a1, Origin: "a", BuildDate: old},
			apktest.Entry{Package: a1doc, Origin: "a", BuildDate: old},
			apktest.Entry{Package: b2, Origin: "b", BuildDate: old},
			apktest.Entry{Package: c1, Origin: "c", BuildDate: old},
			apktest.Entry{Package: d1, Origin: "d", BuildDate: old},
		),
		mtime:    time.Date(2026, 9, 23, 0, 1, 0, 0, time.UTC),
		apks:     map[string][]byte{a1.Filename(): a1.Bytes, a1doc.Filename(): a1doc.Bytes, b2.Filename(): b2.Bytes},
		cdnOnly:  map[string][]byte{c1.Filename(): c1.Bytes},
		logs:     map[string]string{"build-3-24-x86_64/main/a/a-1.0-r0.log": logText("a", "1.0-r0")},
		requests: map[string]int{},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	a := newTestArchiver(t, srv)
	repo := a.Repositories[0]
	// b's package path already holds other bytes, as when Alpine replaced a
	// file without a release bump. The path is left alone.
	bPath := snapshot.PackageObject(repo, b2.Filename())
	if err := util.WriteFile(a.Public, bPath, []byte("an older b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Once(ctx); err != nil {
		t.Fatalf("Once() = %v", err)
	}
	c, err := a.DB.Counts()
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Observations: 1, Packages: 5, Archived: 4, Pending: 1, LogsArchived: 1, LogsMissing: 3}
	if diff := cmp.Diff(want, c); diff != "" {
		t.Errorf("Counts() mismatch (-want +got):\n%s", diff)
	}
	for _, p := range []apktest.Package{a1, a1doc, c1} {
		if got := readObject(t, a.Public, snapshot.PackageObject(repo, p.Filename())); !bytes.Equal(got, p.Bytes) {
			t.Errorf("%s at its package path differs", p.Filename())
		}
	}
	if got := readObject(t, a.Public, bPath); string(got) != "an older b" {
		t.Errorf("the taken package path of b was overwritten")
	}
	if got := readObject(t, a.Public, snapshot.BlobObject(sha256hex(b2.Bytes))); !bytes.Equal(got, b2.Bytes) {
		t.Errorf("b is not kept by content")
	}
	if n := fake.requests["/cdn/alpine/v3.24/main/x86_64/"+c1.Filename()]; n != 1 {
		t.Errorf("CDN fetches of %s = %d, want 1", c1.Filename(), n)
	}
	zr, err := gzip.NewReader(bytes.NewReader(readObject(t, a.Public, snapshot.LogObject("build-3-24-x86_64", "main", "a", "1.0-r0"))))
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := io.ReadAll(zr); string(text) != logText("a", "1.0-r0") {
		t.Errorf("stored log of a = %q", text)
	}
	u, err := snapshot.ParseUploads(bytes.NewReader(readObject(t, a.Public, snapshot.UploadsObject)))
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0].LastModified != fake.mtime.Unix() || u[0].Description != "v20260805-1-gaaaaaaaaaaa" {
		t.Errorf("uploads = %+v", u)
	}
	if got := readObject(t, a.Public, snapshot.BlobObject(u[0].SHA256)); !bytes.Equal(got, fake.index) {
		t.Errorf("index blob differs from the served index")
	}
	restored := filepath.Join(t.TempDir(), "restored.sqlite")
	if n, err := Restore(a.State, restored); err != nil || n != a.Backup {
		t.Fatalf("Restore() = (%d, %v), want backup %d", n, err, a.Backup)
	}
	rdb, err := OpenDB(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if rc, err := rdb.Counts(); err != nil || rc != c {
		t.Errorf("restored Counts() = (%+v, %v), want %+v", rc, err, c)
	}
	// d is listed but missing everywhere. It is retried rather than given
	// up while the index lists it.
	due, err := a.DB.DuePackages(time.Now().Add(time.Hour).Unix(), 10)
	if err != nil || len(due) != 1 || due[0].Name != "d" || !due[0].Listed || due[0].Attempts != 1 {
		t.Fatalf("DuePackages() = (%+v, %v), want d listed with one attempt", due, err)
	}
	due[0].Listed = false
	a.archivePackage(ctx, due[0])
	if c, _ := a.DB.Counts(); c.Gone != 1 || c.Pending != 0 {
		t.Errorf("Counts() after d is gone = %+v", c)
	}
}

func TestArchiverClaim(t *testing.T) {
	ctx := context.Background()
	fake := &fakeAlpine{index: apktest.Index("v1"), requests: map[string]int{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	first, second := newTestArchiver(t, srv), newTestArchiver(t, srv)
	second.State = first.State
	if err := first.start(); err != nil {
		t.Fatalf("first claim = %v", err)
	}
	if err := second.start(); !errors.Is(err, ErrClaimLost) {
		t.Errorf("second claim over a newer backup = %v, want ErrClaimLost", err)
	}
	second.Backup = first.Backup
	if err := second.start(); err != nil {
		t.Fatalf("second claim from the latest backup = %v", err)
	}
	if err := first.pollLoop(ctx, nil); !errors.Is(err, ErrClaimLost) {
		t.Errorf("first pollLoop() after the second claim = %v, want ErrClaimLost", err)
	}
}

func TestArchiverHealth(t *testing.T) {
	now := time.Now()
	tests := []struct {
		Name      string
		StartedAt time.Time
		PolledAt  time.Time
		Want      int
	}{
		{Name: "NotStarted", Want: http.StatusOK},
		{Name: "Starting", StartedAt: now.Add(-time.Minute), Want: http.StatusOK},
		{Name: "RecentPoll", StartedAt: now.Add(-time.Hour), PolledAt: now.Add(-time.Minute), Want: http.StatusOK},
		{Name: "StalePoll", StartedAt: now.Add(-time.Hour), PolledAt: now.Add(-5 * time.Minute), Want: http.StatusServiceUnavailable},
		{Name: "NeverPolled", StartedAt: now.Add(-time.Hour), Want: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			a := &Archiver{Every: time.Minute, startedAt: tc.StartedAt, polledAt: tc.PolledAt}
			rr := httptest.NewRecorder()
			a.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
			if rr.Code != tc.Want {
				t.Errorf("GET /healthz = %d, want %d", rr.Code, tc.Want)
			}
		})
	}
}

func TestArchiverMessages(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := newTestArchiver(t, srv)
	a.trigger = make(chan struct{}, 1)
	tests := []struct {
		Name        string
		Message     Message
		WantTrigger bool
	}{
		{Name: "BuilderStatus", Message: Message{Topic: "build/build-3-24-x86_64", Payload: []byte("failed"), Retained: true}},
		{Name: "UploadOfOtherRepo", Message: Message{Topic: "rsync/dl-master.alpinelinux.org/v3.24/x86_64", Payload: []byte("v3.24/community/x86_64")}},
		{Name: "UploadOfArchivedRepo", Message: Message{Topic: "rsync/dl-master.alpinelinux.org/v3.24/x86_64", Payload: []byte("v3.24/main/x86_64\n")}, WantTrigger: true},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			a.onMessage(tc.Message)
			select {
			case <-a.trigger:
				if !tc.WantTrigger {
					t.Error("onMessage() triggered a poll")
				}
			default:
				if tc.WantTrigger {
					t.Error("onMessage() did not trigger a poll")
				}
			}
		})
	}
	var got []string
	a.DB.mu.Lock()
	a.DB.query("SELECT topic || ' ' || payload || ' ' || retained FROM mqtt_event ORDER BY id", nil, func(s *sqlite3.Stmt) { got = append(got, s.ColumnText(0)) })
	a.DB.mu.Unlock()
	want := []string{
		"build/build-3-24-x86_64 failed 1",
		"rsync/dl-master.alpinelinux.org/v3.24/x86_64 v3.24/community/x86_64 0",
		"rsync/dl-master.alpinelinux.org/v3.24/x86_64 v3.24/main/x86_64 0",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("recorded messages mismatch (-want +got):\n%s", diff)
	}
}

// Counts summarizes the archive and its queues.
type Counts struct {
	Observations int64 `json:"observations"`
	Packages     int64 `json:"packages"`
	Archived     int64 `json:"archived"`
	Pending      int64 `json:"pending"`
	Gone         int64 `json:"gone"`
	LogsArchived int64 `json:"logs_archived"`
	LogsPending  int64 `json:"logs_pending"`
	LogsMissing  int64 `json:"logs_missing"`
}

// Counts returns archive-wide counts.
func (d *DB) Counts() (Counts, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var c Counts
	err := d.query(`SELECT
	  (SELECT COUNT(*) FROM index_observation),
	  (SELECT COUNT(*) FROM package),
	  (SELECT COUNT(*) FROM package WHERE apk_sha256 IS NOT NULL),
	  (SELECT COUNT(*) FROM package p LEFT JOIN apk_fetch f ON f.package_id=p.id WHERE p.apk_sha256 IS NULL AND COALESCE(f.gone,0)=0),
	  (SELECT COUNT(*) FROM apk_fetch f JOIN package p ON p.id=f.package_id WHERE f.gone=1 AND p.apk_sha256 IS NULL),
	  (SELECT COUNT(*) FROM build_log WHERE state='archived'),
	  (SELECT COUNT(*) FROM build_log WHERE state='pending'),
	  (SELECT COUNT(*) FROM build_log WHERE state='missing')`, nil, func(s *sqlite3.Stmt) {
		c = Counts{s.ColumnInt64(0), s.ColumnInt64(1), s.ColumnInt64(2), s.ColumnInt64(3), s.ColumnInt64(4), s.ColumnInt64(5), s.ColumnInt64(6), s.ColumnInt64(7)}
	})
	return c, err
}
