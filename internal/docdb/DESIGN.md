# docdb: published SQLite views of document streams

## Objective

Serve relational queries over the operational OSS Rebuild data in a format amenable to local development and workflows.

## Background

Firestore holds the operational data: rebuild attempts, runs, agent sessions and iterations, scratch VMs and execs, repo metrics. The store is shaped for transactional writes.

The questions asked of that data are relational. Success rates over time, cost joined across attempts, sessions, and VMs, coverage per package and version. Each consumer answered them with fan-out document scans: slow, metered, online-only, and each re-derived its own flattening of the documents.

## Design

- One published SQLite database: a daily full build plus periodic delta segments allow for fast load and periodic refresh.
- Firestore still defines the schema: each row holds its document in a column, with relational columns deriving from it, ensuring consistency.
- Writes are timestamped: all views of the db converge using that total order.
- Compatibility is enforced by era: every db and delta is named by its schema version, with readers releasing to take up breaking changes.

Out of scope: replacing Firestore as the system of record, prompt delete propagation, real-time freshness.

### Overview

A daily rollup scans Firestore and publishes a complete database under an era-versioned name, `rebuild-v1.db.gz`. Every five minutes an exporter publishes the documents written since its last segment as a delta under `deltas-v1/`. Destinations are filesystems, a GCS bucket or a local directory. Readers hold a local copy that follows both, and benchmark runs write the same tables into a database of their own.

```
           ┌─ daily full scan ───> rebuild-v1.db.gz ────┐
Firestore ─┤                                            ├──> dashboard cache, ctl --db
           └─ 5m since-queries ──> deltas-v1/*.jsonl.gz ┘
```

### Document tables

Each row is defined as its corresponding Firestore JSON document. A doc table keeps each document whole in its `raw` column and exposes it relationally. The write statement itself extracts the few real columns, the primary key and the update clock, and every other column is GENERATED from `raw`, so a column can never disagree with the document it derives from. A new column definition applies to every stored document, so history backfills at the next rollup with no export-time code.

One registry entry is the entire schema declaration:

```go
{Name: "attempts",
 Cols: []docdb.Col{
    {Name: "ecosystem", Type: "TEXT", Expr: docdb.Doc("$.Ecosystem")},
    // ...
    {Name: "updated", Type: "TEXT", Expr: docdb.DocTime("$.Updated")}},
 PK: []string{"ecosystem", "package", "version", "artifact", "run_id"},
 GenCols: []docdb.GenCol{
    {Name: "build_id", Type: "TEXT", Expr: docdb.Raw("$.BuildID")},
    {Name: "setup_seconds", Type: "REAL", Expr: docdb.RawSeconds("$.BuildTimings.Setup")},
    // ...
 }}
```

Derived tables are a secondary mechanism allowing for stored aggregations: a SELECT over the doc tables, materialized once at build. The rollup aggregates (package stats, daily activity, cost observations) live here as SQL beside the tables they read, refreshed by rebuild and up to a day stale.

### Propagation

All writes need to carry a timestamp: a document is applied only if its `updated` clock is at least the stored row's, NULL treated as oldest. Writes are therefore idempotent and order-independent. Rollup scans, delta replay, cache catch-up, and local benchmark writes all execute the same statement, so any interleaving or replay of them converges on the same rows. This one primitive is why the pipeline needs no coordination: exporter queries can overlap, a segment can apply twice, and a local database can take upstream deltas over its own writes.

Segments are gzip'd JSONL files of bare documents, named by fixed-width UTC write time so lexicographic order is chronological. The exporter is stateless and resumes from the newest segment name in the destination. The cache fingerprints the published base on each tick, rehydrating a fresh copy when the base was replaced and otherwise applying only the segments newer than the last one applied. Replay after rehydrate starts from the base's recorded watermark. A failed refresh keeps serving the current copy and retries next tick.

### Evolution and enforcement

The schema era is recorded in the database header (`PRAGMA user_version`) and in the artifact names. A breaking change, dropping, renaming, or retyping a column, bumps the era: new objects publish under the new names, readers on the old era keep consuming the old ones until redeployed, and deploy order does not matter. Additive columns do not bump. There are no migrations anywhere. The registry is the migration, and the next rollup rebuilds all history under it.

Deriving the schema from JSON trades away compile-time shape safety, and the silent failure mode is a renamed Go field whose column quietly extracts NULL, indistinguishable from unmeasured data. A guard suite closes the gap:

- A golden shape test compares the materialized tables against a committed per-era shape file and refuses breaking rewrites without a bump.
- A saturation test reflect-fills every source struct, runs it through the pipeline, and asserts every column extracts non-NULL, catching broken paths.
- Skeleton probes store hand-written keys-only documents to pin absence semantics.
- Stored column types are checked against declared affinities, and duplicated extraction paths are rejected.

Table names and column expressions are trusted SQL fragments, sourced only from compile-time registries, never from data.

## Alternatives

- Flat row structs exported as JSONL tables. Rejected for the three-place field problem above.
- Serving Firestore directly. The status quo being replaced: metered fan-out reads, no SQL, online-only.
- BigQuery. Strong at query time, but no local or offline story, and heavier to operate than a file.
- A hosted read replica or OLAP service. A new service to run, against the objective.

## Limitations

- Additive skew within an era is invisible. A reader expecting a newly added column can meet an older base that lacks it, and the era check cannot tell that base from a current one. Writer-first deploys and the daily rollup bound the window to a day.
- Documents are stored whole, so the database trades size for the single declaration. A live dump measures about 4KiB per attempt, gzipping about 8x, which projects to roughly 40MB at 10k attempts and 4GB at 1M. The levers (virtual columns, slimming raw, a retention bound on the scan) are known but not designed.
- Deletes do not propagate between rollups. The segment format reserves a delete op but nothing emits it. The daily rebuild is the tombstone mechanism, which suits append-mostly operational data and rules out anything needing prompt removal.
