# Alpine snapshots

`alpine-snapshots` archives Alpine Linux package repositories so that any
past repository state can be served again, the way snapshot.debian.org does
for Debian. Alpine mirrors keep only the newest version of each package, so
without an archive a build cannot be repeated against the dependencies it
originally saw.

The archiver polls each repository's `APKINDEX.tar.gz` on the origin mirror
whenever Alpine's message broker (`msg.alpinelinux.org`) announces an upload,
and every five minutes in case a notice is lost. It stores every index it
sees, every package those indexes list and the official build log of every
origin version. It also records the broker's builder status lines, which date
every build and failed pass and are published nowhere else. The public layout
is documented in
[`pkg/registry/alpine/snapshot`](../../pkg/registry/alpine/snapshot/snapshot.go).

A private SQLite database records what was observed and what remains to
fetch. It lives on local disk and is backed up to a private state bucket
after every new upload and at least every 15 minutes, as numbered gzipped
backups created and never overwritten. On start the archiver restores the latest
backup and claims the archive by creating the next one. An archiver whose
next number is taken stops, so at most one keeps writing.

## Running locally

Directories work in place of buckets. A full repository is several
gigabytes of packages.

```
go run ./cmd/alpine-snapshots --once \
  --public=/tmp/alpine/public --state=/tmp/alpine/state --db-dir=/tmp/alpine/db \
  --repositories=v3.24/main/x86_64
```

## Deployment

`enable_alpine_snapshots` (which requires `enable_vpc`) creates:

- `${host}-rebuild-alpine-snapshots`, the archive, readable by all users when
  `public` is set.
- `${host}-rebuild-alpine-snapshots-state`, the database backups.
- A managed instance group of one instance running the archiver, recreated
  when `/healthz` fails.

Both buckets are protected from `terraform destroy`. The repositories are set
by `alpine_snapshots_repositories`.

### Seeding from an existing archive

An archive written by the archiver this one replaced can seed a deployment.
Copy it before the archiver first starts:

```
OLD=gs://old-archive NEW=gs://HOST-rebuild-alpine-snapshots STATE=gs://HOST-rebuild-alpine-snapshots-state
gcloud storage cp -r "$OLD/apk" "$OLD/buildlogs" "$NEW/"
gcloud storage cat "$OLD/db/apksnap.sqlite" | gzip | gcloud storage cp - "$STATE/db/0000000001.sqlite.gz"
gcloud storage cat "$OLD/uploads.json" | jq -r '.uploads[].sha256' | sort -u |
  sed "s|.*|$OLD/blobs/sha256/&|" | gcloud storage cp -I "$NEW/blobs/sha256/"
```

The old layout also keeps packages under `blobs/sha256/`, so only the index
blobs are copied. The archiver republishes the upload table on start.
