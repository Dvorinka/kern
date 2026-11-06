# Backups and restore

Two things carry state: the Postgres database (all of it — runs, jobs,
secrets, members, settings, queue) and the blob store (step logs,
summaries, artifacts, source bundles). Everything else — images, the
runner work-root, `_cache`, `_node` — is regenerable and excluded.

A backup is only real once a restore has been run. Do the drill below at
least once, then quarterly.

## What to protect

| State | Location | Method |
| --- | --- | --- |
| Postgres | `kern-db` compose volume (or wherever `KERN_DATABASE_URL` points) | `pg_dump` custom format |
| Blobs — filesystem driver | `KERN_BLOB_DIR` (`/var/lib/kern/blobs`, the `kern-blobs` volume in compose) | `rsync`/`tar` snapshot |
| Blobs — S3 driver | the `KERN_BLOB_S3_BUCKET` bucket | bucket versioning or your provider's backup; nothing to copy locally |
| Env + App key | `/opt/kern/.env`, `KERN_GITHUB_APP_PRIVATE_KEY_FILE` | copy with the dump — secrets are AES-256 at rest but the key to open them is in `.env` |

The encrypted `secrets`/`github_apps` rows are useless without
`KERN_SECRET_KEY` — losing `.env` is losing the data even with a perfect
dump. Back it up to somewhere safer than the host, and nowhere public.

## Nightly script

`deployments/backup.sh`-equivalent — drop in `/etc/cron.d/kern-backup` or a
systemd timer on the DB host:

```sh
#!/bin/sh
set -eu
DEST=/var/backups/kern
mkdir -p "$DEST"
STAMP=$(date +%Y%m%d-%H%M%S)

# Postgres — custom format, compressed, consistent while live.
docker exec kern-postgres-1 pg_dump -U kern -Fc kern > "$DEST/kern-$STAMP.dump"

# Blobs — only when the filesystem driver is in use.
tar -C /var/lib/kern -czf "$DEST/blobs-$STAMP.tar.gz" blobs

cp /opt/kern/.env "$DEST/env-$STAMP"

# Keep 14 days.
find "$DEST" -name 'kern-*.dump' -mtime +14 -delete
find "$DEST" -name 'blobs-*.tar.gz' -mtime +14 -delete
find "$DEST" -name 'env-*' -mtime +14 -delete
```

`pg_dump -Fc` inside the container avoids a version-skew fight with the
host's client. Adjust the container name (`docker ps` will tell you).

## Restore drill

On a scratch host or throwaway DB — never against live until the drill
has passed once:

```sh
# 1. Fresh postgres
docker run -d --name kern-pg-restore -p 55433:5432 \
  -e POSTGRES_USER=kern -e POSTGRES_PASSWORD=kern -e POSTGRES_DB=kern \
  postgres:16-alpine

# 2. Restore the dump
docker cp /var/backups/kern/kern-<stamp>.dump kern-pg-restore:/tmp/
docker exec kern-pg-restore pg_restore -U kern -d kern --clean --if-exists /tmp/kern-<stamp>.dump

# 3. Point a test api at it (blobs dir restored alongside if filesystem driver)
KERN_DATABASE_URL=postgres://kern:kern@localhost:55433/kern?sslmode=disable \
KERN_BLOB_DIR=/tmp/kern-blobs-restore \
KERN_ENV=development ./kern-api

# 4. Verify: /healthz, a run list, one step log, one artifact download.
```

Migrations run at api boot, so a dump from an older version restores
cleanly and upgrades itself.

## Failure modes to know

- **Dump exists but blobs are gone** — runs show but logs/artifacts 404.
  The DB is the directory; blobs are the content. Both are required.
- **`.env` lost** — secrets and GitHub App credentials are unrecoverable
  ciphertext. Re-mint the App key and re-enter secrets by hand.
- **Restoring mid-run** — in-flight jobs resume from their leases;
  runners re-register on their own. Expect a few spurious retries, not
  corruption.
