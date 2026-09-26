// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpinesnap

import (
	"os"
	"sync"

	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// schemaVersion is the database's schema era (PRAGMA user_version). New
// tables and indexes need no new era, since the schema's statements run on
// every open. A change to existing tables bumps the era and migrates from
// the previous one in init.
//
// NOTE: Era 1 is the schema of the archiver that preceded this package,
// which set no user_version and whose tables have more columns than these.
// Its databases open as era 1 so that its backups can seed a deployment.
// That is also why times are INTEGER Unix seconds rather than
// sqlitex.TimeFormat text: this database is private working state.
const schemaVersion = 1

// schema holds the archive's state. index_observation has a row per index
// upload seen, package a row per distinct index entry with the range of
// observations listing it and, once archived, its SHA-256. apk_fetch
// records failed attempts to archive a package, and gone marks one no
// mirror has any more. build_log has a row per origin version seen: the
// official build log to archive and what became of it. mqtt_event keeps the
// broker's messages: upload notices and the builders' status lines, which
// date every build and failed pass. The broker resends retained statuses on
// every connect, marked retained.
const schema = `
CREATE TABLE IF NOT EXISTS index_observation (
  id INTEGER PRIMARY KEY,
  branch TEXT NOT NULL, repo TEXT NOT NULL, arch TEXT NOT NULL,
  observed_at INTEGER NOT NULL,
  last_modified TEXT, etag TEXT,
  sha256 TEXT NOT NULL, size INTEGER NOT NULL,
  description TEXT,
  n_packages INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS io_key ON index_observation(branch, repo, arch, observed_at);
CREATE TABLE IF NOT EXISTS package (
  id INTEGER PRIMARY KEY,
  branch TEXT NOT NULL, repo TEXT NOT NULL, arch TEXT NOT NULL,
  pkgname TEXT NOT NULL, version TEXT NOT NULL,
  control_sha1 TEXT NOT NULL,
  builddate INTEGER, origin TEXT,
  first_seen_obs INTEGER NOT NULL, last_seen_obs INTEGER NOT NULL,
  apk_sha256 TEXT,
  UNIQUE(branch, repo, arch, pkgname, version, control_sha1)
);
CREATE INDEX IF NOT EXISTS pkg_unarchived ON package(first_seen_obs DESC, id) WHERE apk_sha256 IS NULL;
CREATE TABLE IF NOT EXISTS apk_fetch (
  package_id INTEGER PRIMARY KEY,
  attempts INTEGER NOT NULL,
  next_attempt_at INTEGER NOT NULL,
  gone INTEGER NOT NULL DEFAULT 0,
  last_error TEXT
);
CREATE TABLE IF NOT EXISTS build_log (
  id INTEGER PRIMARY KEY,
  branch TEXT NOT NULL, repo TEXT NOT NULL, arch TEXT NOT NULL,
  builder TEXT NOT NULL, origin TEXT NOT NULL, version TEXT NOT NULL,
  builddate INTEGER,
  first_seen_obs INTEGER NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  object TEXT,
  UNIQUE(builder, repo, origin, version)
);
CREATE INDEX IF NOT EXISTS build_log_due ON build_log(state, first_seen_obs DESC, id);
CREATE TABLE IF NOT EXISTS mqtt_event (
  id INTEGER PRIMARY KEY,
  received_at INTEGER NOT NULL,
  topic TEXT NOT NULL,
  payload TEXT NOT NULL,
  retained INTEGER NOT NULL DEFAULT 0
);
`

// Build log states.
const (
	LogPending  = "pending"
	LogArchived = "archived"
	LogMissing  = "missing" // 404 long after the build: never published or since removed
	LogFailed   = "failed"  // gave up after repeated other errors
)

// DB is the archive's SQLite database. Its methods are safe for concurrent
// use.
type DB struct {
	mu   sync.Mutex
	conn *sqlite3.Conn
}

// OpenDB opens or creates the database at path.
func OpenDB(path string) (*DB, error) {
	conn, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "opening database")
	}
	d := &DB{conn: conn}
	if err := d.init(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	if err := d.conn.Exec("PRAGMA busy_timeout = 10000; PRAGMA journal_mode = WAL;" + schema); err != nil {
		return errors.Wrap(err, "creating schema")
	}
	if v, err := sqlitex.Version(d.conn); err != nil {
		return err
	} else if v == 0 {
		if err := sqlitex.SetVersion(d.conn, schemaVersion); err != nil {
			return err
		}
	}
	return sqlitex.CheckVersion(d.conn, schemaVersion)
}

// Close closes the database.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.Close()
}

// exec runs a statement. d.mu must be held.
func (d *DB) exec(sql string, args ...any) error {
	return d.query(sql, args, nil)
}

// query calls fn for each row of a statement. d.mu must be held.
func (d *DB) query(sql string, args []any, fn func(*sqlite3.Stmt)) error {
	stmt, _, err := d.conn.Prepare(sql)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, a := range args {
		switch v := a.(type) {
		case string:
			err = stmt.BindText(i+1, v)
		case int64:
			err = stmt.BindInt64(i+1, v)
		case bool:
			err = stmt.BindBool(i+1, v)
		default:
			panic(errors.Errorf("unsupported bind type %T", a))
		}
		if err != nil {
			return err
		}
	}
	for stmt.Step() {
		if fn != nil {
			fn(stmt)
		}
	}
	return stmt.Err()
}

// Observation is one index upload the archive saw.
type Observation struct {
	Repository   alpine.Repository
	ObservedAt   int64
	LastModified string
	ETag         string
	SHA256       string
	Size         int64
	Description  string
	NPackages    int64
}

const obsCols = "branch, repo, arch, observed_at, last_modified, etag, sha256, size, description, n_packages"

func scanObservation(s *sqlite3.Stmt) Observation {
	return Observation{
		Repository:   alpine.Repository{Branch: s.ColumnText(0), Repo: s.ColumnText(1), Arch: s.ColumnText(2)},
		ObservedAt:   s.ColumnInt64(3),
		LastModified: s.ColumnText(4),
		ETag:         s.ColumnText(5),
		SHA256:       s.ColumnText(6),
		Size:         s.ColumnInt64(7),
		Description:  s.ColumnText(8),
		NPackages:    s.ColumnInt64(9),
	}
}

// LatestObservation returns the newest observation of r, or nil.
func (d *DB) LatestObservation(r alpine.Repository) (*Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out *Observation
	err := d.query("SELECT "+obsCols+" FROM index_observation WHERE branch=? AND repo=? AND arch=? ORDER BY observed_at DESC, id DESC LIMIT 1",
		[]any{r.Branch, r.Repo, r.Arch}, func(s *sqlite3.Stmt) {
			o := scanObservation(s)
			out = &o
		})
	return out, err
}

// Observations lists every observation by repository, then in time order.
func (d *DB) Observations() ([]Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Observation
	err := d.query("SELECT "+obsCols+" FROM index_observation ORDER BY branch, repo, arch, observed_at, id", nil,
		func(s *sqlite3.Stmt) { out = append(out, scanObservation(s)) })
	return out, err
}

// Ingest records an index upload: a new observation, a package row for
// each entry not seen before and a longer listing range for the others.
func (d *DB) Ingest(o Observation, records []apk.Record) (err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.conn.Exec("BEGIN"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			d.conn.Exec("ROLLBACK")
		}
	}()
	r := o.Repository
	if err := d.exec("INSERT INTO index_observation(branch,repo,arch,observed_at,last_modified,etag,sha256,size,description,n_packages) VALUES (?,?,?,?,?,?,?,?,?,?)",
		r.Branch, r.Repo, r.Arch, o.ObservedAt, o.LastModified, o.ETag, o.SHA256, o.Size, o.Description, o.NPackages); err != nil {
		return errors.Wrap(err, "inserting observation")
	}
	id := d.conn.LastInsertRowID()
	for _, rec := range records {
		if err := d.exec("UPDATE package SET last_seen_obs=? WHERE branch=? AND repo=? AND arch=? AND pkgname=? AND version=? AND control_sha1=?",
			id, r.Branch, r.Repo, r.Arch, rec.Name(), rec.Version(), rec.Checksum()); err != nil {
			return errors.Wrap(err, "extending package")
		}
		if d.conn.Changes() > 0 {
			continue
		}
		if err := d.exec("INSERT INTO package(branch,repo,arch,pkgname,version,control_sha1,builddate,origin,first_seen_obs,last_seen_obs) VALUES (?,?,?,?,?,?,?,?,?,?)",
			r.Branch, r.Repo, r.Arch, rec.Name(), rec.Version(), rec.Checksum(), rec.BuildDate(), rec.Origin(), id, id); err != nil {
			return errors.Wrap(err, "inserting package")
		}
	}
	if err := d.exec(`INSERT OR IGNORE INTO build_log(branch, repo, arch, builder, origin, version, builddate, first_seen_obs)
	  SELECT branch, repo, arch, ?, COALESCE(NULLIF(origin, ''), pkgname), version, MAX(builddate), MIN(first_seen_obs)
	  FROM package WHERE first_seen_obs = ?
	  GROUP BY COALESCE(NULLIF(origin, ''), pkgname), version`, r.Builder(), id); err != nil {
		return errors.Wrap(err, "enqueueing build logs")
	}
	return errors.Wrap(d.conn.Exec("COMMIT"), "committing")
}

// QueuedPackage is a package whose file is not archived yet.
type QueuedPackage struct {
	ID         int64
	Repository alpine.Repository
	Name       string
	Version    string
	Checksum   string
	Attempts   int64
	Listed     bool // the newest observation of its repository lists it
}

// Filename is the package's mirror file name.
func (q QueuedPackage) Filename() string { return apk.Filename(q.Name, q.Version) }

// DuePackages returns up to limit unarchived packages due for an attempt at
// now. Packages the newest index no longer lists come first, since only a
// mirror's cache may still have them, then the newest.
func (d *DB) DuePackages(now int64, limit int) ([]QueuedPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []QueuedPackage
	err := d.query(`WITH latest AS (SELECT branch, repo, arch, MAX(id) AS obs FROM index_observation GROUP BY branch, repo, arch)
	  SELECT p.id, p.branch, p.repo, p.arch, p.pkgname, p.version, p.control_sha1, COALESCE(f.attempts, 0), p.last_seen_obs >= l.obs
	  FROM package p JOIN latest l USING (branch, repo, arch)
	  LEFT JOIN apk_fetch f ON f.package_id = p.id
	  WHERE p.apk_sha256 IS NULL AND (f.package_id IS NULL OR (f.gone = 0 AND f.next_attempt_at <= ?))
	  ORDER BY 9, p.first_seen_obs DESC, p.id LIMIT ?`, []any{now, int64(limit)}, func(s *sqlite3.Stmt) {
		out = append(out, QueuedPackage{
			ID:         s.ColumnInt64(0),
			Repository: alpine.Repository{Branch: s.ColumnText(1), Repo: s.ColumnText(2), Arch: s.ColumnText(3)},
			Name:       s.ColumnText(4),
			Version:    s.ColumnText(5),
			Checksum:   s.ColumnText(6),
			Attempts:   s.ColumnInt64(7),
			Listed:     s.ColumnBool(8),
		})
	})
	return out, err
}

// PackageDone records a package attempt: archived with its SHA-256, or
// failed to retry at nextAttemptAt, or gone for good.
func (d *DB) PackageDone(id int64, sha256 string, nextAttemptAt int64, gone bool, msg string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sha256 != "" {
		return d.exec("UPDATE package SET apk_sha256=? WHERE id=?", sha256, id)
	}
	return d.exec(`INSERT INTO apk_fetch(package_id, attempts, next_attempt_at, gone, last_error) VALUES (?, 1, ?, ?, ?)
	  ON CONFLICT(package_id) DO UPDATE SET attempts = attempts + 1, next_attempt_at = excluded.next_attempt_at, gone = excluded.gone, last_error = excluded.last_error`,
		id, nextAttemptAt, gone, msg)
}

// QueuedLog is a build log not archived yet.
type QueuedLog struct {
	ID        int64
	Repo      string
	Builder   string
	Origin    string
	Version   string
	BuildDate int64
	Attempts  int64
}

// DueLogs returns up to limit pending build logs due at now, newest first.
func (d *DB) DueLogs(now int64, limit int) ([]QueuedLog, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []QueuedLog
	err := d.query(`SELECT id, repo, builder, origin, version, COALESCE(builddate, 0), attempts FROM build_log
	  WHERE state = 'pending' AND next_attempt_at <= ? ORDER BY first_seen_obs DESC, id LIMIT ?`, []any{now, int64(limit)}, func(s *sqlite3.Stmt) {
		out = append(out, QueuedLog{ID: s.ColumnInt64(0), Repo: s.ColumnText(1), Builder: s.ColumnText(2), Origin: s.ColumnText(3),
			Version: s.ColumnText(4), BuildDate: s.ColumnInt64(5), Attempts: s.ColumnInt64(6)})
	})
	return out, err
}

// LogDone records a build log attempt: archived with its object name, or
// failed with state LogPending to retry at nextAttemptAt, LogMissing or
// LogFailed.
func (d *DB) LogDone(id int64, state, object string, nextAttemptAt int64, msg string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exec("UPDATE build_log SET state=?, object=NULLIF(?, ''), attempts=attempts+1, next_attempt_at=?, last_error=NULLIF(?, '') WHERE id=?",
		state, object, nextAttemptAt, msg, id)
}

// InsertMQTTEvent records a broker message.
func (d *DB) InsertMQTTEvent(receivedAt int64, m Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exec("INSERT INTO mqtt_event(received_at,topic,payload,retained) VALUES (?,?,?,?)", receivedAt, m.Topic, string(m.Payload), m.Retained)
}

// TotalChanges counts rows changed through this connection since it
// opened. A backup is due when it moves.
func (d *DB) TotalChanges() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.TotalChanges()
}

// Backup writes a consistent, compacted copy of the database to path, which
// must not exist. VACUUM INTO reads one snapshot of the database, WAL
// included, so unlike a file copy it is safe while the database is in use.
func (d *DB) Backup(path string) error {
	if _, err := os.Stat(path); err == nil {
		return errors.Errorf("backup target %s exists", path)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return errors.Wrap(d.exec("VACUUM INTO ?", path), "backing up database")
}
