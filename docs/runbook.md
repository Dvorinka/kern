# Operations runbook

Symptom → check → fix. Start every investigation with `kern doctor` — it already
probes `/healthz`, `/readyz`, `/meta`, auth, queue pressure, webhook failures and
per-runner liveness. `kern status --watch` is the live dashboard; the same data is
in `GET /api/v1/overview`.

## Webhooks not arriving / runs never created

| Check | Where |
| --- | --- |
| Delivery persisted? | `GET /api/v1/webhooks/deliveries` (or `kern doctor` → `webhooks` row counts 24h failures + last error). Rows carry `status`, `error`, `processed_at`; payload is excluded from the list, fetch `GET /webhooks/deliveries/:id` for detail. |
| Signature failure? | The `error` column says so. Signature is HMAC over the raw body with the GitHub App webhook secret — check the App's webhook config in deploy.md §3 and that `KERN_GITHUB_APP_*` env matches the installed App. |
| App suspended / permissions? | GitHub App → Advanced → Recent Deliveries shows GitHub's side; a 4xx/5xx there means KERN rejected or was unreachable. |
| Retention | `webhook_deliveries` rows older than `KERN_WEBHOOK_RETENTION` (default 30d) are pruned — old deliveries are simply gone. |

Fix: correct the secret/permissions and redeliver from GitHub; or
`kern runs trigger` / the UI to create the run manually.

## Run stuck `queued`

The scheduler ticks every `KERN_SCHEDULER_TICK` (2s) and claims the oldest queued
job whose `runs_on` labels match an online runner. A queued job means one of:

- **No eligible runner** — `kern runners list` shows labels per runner; a
  `runs-on: macos` job with no macOS runner waits forever by design.
- **All runners busy or draining** — `draining` runners take no new claims
  (`kern runners drain|undrain <id>`); in-flight jobs finish normally.
- **`needs_docker` with no `kern-docker` runner** — docker-invoking jobs prefer
  host-executor runners advertising that label; without one they fall back to
  ordinary runners, but a fleet of only docker-executor runners still works.
- **Waiting, not queued** — environment approvals park jobs in
  `waiting`/`waiting_approval` (`kern deployments list`, approve/reject); fork
  PRs park at run level under `fork_gate`. These are intentional, not stuck.
- **Orphaned rows** — jobs whose run already concluded are reaped by
  `CancelOrphanedJobs`; if the queue count still looks wrong, check
  `kern status` vs `SELECT count(*) FROM jobs WHERE status='queued'`.

Diagnose a specific run with `kern diagnose <run-id>` — it bundles statuses,
labels and the failing step's log tail.

## Runner offline / flapping

- Runners heartbeat every 10s; the orchestrator marks a runner `offline` after
  `StaleRunnerTimeout` (60s) of missed beats, every scheduler tick.
- `kern runners list` shows status, last heartbeat, load, disk, version.
  `kern doctor` warns per offline runner.
- Ephemeral runners offline >24h are deleted outright (credentials already dead).
- Systemd unit: `systemctl --user status kern-runner` (or the unit name from
  `kern runners install`); journal shows heartbeat failures. Common causes:
  control plane unreachable (DNS/TLS/proxy), host suspend, OOM kill.
- A runner that heartbeats but claims nothing is usually `draining` or failing
  the load gate (`KERN_RUNNER_LOAD_GATE`) — check the runner's own log for
  `load gate` skips.
- Version drift / self-update: runners with `auto_update` on pull the signed
  `kern-update.json` manifest published with each GitHub release; the compose
  deployment uses the `kern-updater` sidecar instead. `kern runners list`
  shows the reported version — a runner stuck on an old version either has
  auto-update off or its manifest fetch is failing (check runner logs for
  `update` lines, and that the release actually published
  `kern-update.json` + `.sig`).

## Runner host disk full

Symptoms: jobs die with `ENOSPC` mid-step, `docker` errors, runner GC warnings.

- Job containers write under `KERN_RUNNER_WORK`; per-job dirs are swept when
  stale, remote actions cache by SHA under `_cache/_actions`, node/tool caches
  under `_node`/`_toolcache`.
- Below `KERN_RUNNER_DISK_MIN_FREE_PCT` (default 10, `0` disables) the runner
  prunes reclaimable Docker space — but only while idle; active jobs defer it.
- `KERN_RUNNER_IMAGE_MAX_AGE` GCs pulled job images; `KERN_RUNNER_PREPULL`
  keeps hot images local.
- Manual: `docker system df` then `docker image prune` / `docker builder prune`
  on the runner host; delete stale `_work`-style dirs older than your job
  retention.
- Co-located fleets share the filesystem: GH Actions `_work` dirs and KERN job
  workspaces on the same host add up fast — see issue #206 for the known
  capacity ceiling on 79G CTs.

## Postgres maintenance

- Backups and restore drill: [backups.md](backups.md). The dump alone is not a
  backup until a restore has been rehearsed.
- `KERN_SECRET_KEY` lives in `.env` — secrets are AES-256 at rest; losing `.env`
  loses the data even with a perfect dump.
- Autovacuum defaults are adequate at current scale; under heavy
  `jobs`/`steps` churn watch `pg_stat_user_tables.n_dead_tup` and tighten
  per-table `autovacuum_vacuum_scale_factor` (deploy.md §6).
- Connection sanity: `docker exec <pg> psql -U kern -d kern -c 'select count(*)
  from jobs'` — the compose service is `kern-db`/`postgres` depending on your
  `.env`; check `docker compose ps` for the real name.

## Log / artifact retention

| Data | Knob | Default |
| --- | --- | --- |
| Step logs + run summaries | `KERN_LOG_RETENTION` | 30d |
| `webhook_deliveries` rows | `KERN_WEBHOOK_RETENTION` | 30d |
| `audit_events` rows | `KERN_AUDIT_RETENTION` | 365d |
| `notify_deliveries` rows | `KERN_NOTIFY_RETENTION` | 30d |
| Artifacts | fixed expiry | 90d |
| Action cache entries | `KERN_CACHE_MAX_AGE` / `KERN_CACHE_MAX_BYTES` | 7d / 1 GiB |

Sweeps run in the orchestrator loop; there is no manual "delete run" — retention
is the only deletion path for logs.

## Deploy / rollback

Redeploy: `cd /opt/kern/src && git pull --ff-only && docker compose
--env-file /opt/kern/.env -f deployments/docker-compose.yml up -d --build`
(deploy.md §6). The updater sidecar recreates `api` on new images — check
`docker ps` for stale `kern-api` processes after an update (a second api
process once caused split-brain, #195/#178). Rollback = check out the previous
tag and redeploy; schema migrations are forward-only, so read
`docs/backups.md` before assuming a downgrade is safe.
