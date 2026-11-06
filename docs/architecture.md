# KERN — Architecture

This document describes the architecture as implemented. For the product-level
specification see `project.md`; for milestone status see `plan.md`.

## Processes

| Process | Binary | Role |
| --- | --- | --- |
| Control plane | `cmd/api` | HTTP API, auth, workflow compile, scheduler loop, orchestrator, log ingestion |
| Runner agent | `cmd/runner` | Registers, long-polls for jobs, executes them in ephemeral containers |
| CLI | `cmd/kern` | Operator client (`kern`): auth, projects, runs, runners, artifacts, cache, `exec`, `split` |
| Migrator | `cmd/migrate` | Applies goose migrations and exits (also runs inside `cmd/api` at boot) |
| Updater | `deployments/kern-updater.sh` | Compose-profile sidecar that polls `GET /update/plan` and applies signed releases (git fast-forward + rebuild). External runners self-update via `KERN_RUNNER_UPDATE=1` |

The scheduler and orchestrator run in-process inside `cmd/api`. They move to a
separate `cmd/scheduler` only when multi-instance control planes are tested; the
store layer uses `FOR UPDATE SKIP LOCKED` claiming so a split is safe later.

## Request flow

```
GitHub ──webhook──▶ api (HMAC verify → persist delivery → dedupe → 202)
                        │
        local sync ─────┤  project.local_path → .github/workflows/*.yml
                        ▼
        compile: parse → validate → model → matrix expand → Plan (+diagnostics)
                        ▼
        PostgreSQL: workflow_versions (immutable, keyed by commit SHA)
                    workflow_runs → jobs ('waiting') → steps
                        │
        orchestrator.Tick: promote queued runs (concurrency groups),
          reap expired leases → requeue or fail (KERN_MAX_JOB_ATTEMPTS),
          mark stale runners offline, AdvanceRun per active run
                        ▼
        jobs 'waiting' → needs terminal? → evaluate if: → 'queued'
                        ▼
        runner POST /runners/claim (long-poll) → ClaimJob SKIP LOCKED → lease
                        │
        runner: jobSecrets (blocked for fork/pull_request trust levels)
                prepare: git clone → docker pull → docker create+start
                per step: start → docker exec → stream logs → finish
                finishJob → orchestrator.AdvanceRun → FinishRunIfDone
```

## Execution isolation

Each job runs in one ephemeral container created with:

- `--user 1000:1000`, `--cap-drop ALL`, `no-new-privileges`
- read-only root filesystem; `tmpfs` for `/tmp` and `/run` (noexec, nosuid)
- `--memory`/`--cpus` limits (`KERN_JOB_MEMORY`, `KERN_JOB_CPUS`; unset CPUs are an equal share of the host, so concurrency 1 gives the job every CPU)
- mounts: workspace, `/github/cmd` (file commands), `/kern/scripts` (read-only)
- no host Docker socket

Steps run via `docker exec` so each step reports its own exit code. File
commands (`GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY`)
are files under `/github/cmd`; the runner merges them into the env builder
between steps, matching GitHub's toolkit semantics.

Sibling containers share the job boundary but not its privileges:

- `services:` entries run on a per-job Docker network where the service name
  is a DNS alias; `--health-*` options pass through and the runner waits for
  healthy before starting steps.
- `docker://` and Dockerfile actions run as separate containers under the
  same hardening (dropped caps, no-new-privileges, read-only root).
- `node*` actions execute inside the job container against a pinned Node
  distribution the runner downloads once, verifies against `SHASUMS256.txt`,
  and mounts read-only from `<work-root>/_node`.

Checkout happens on the runner host into the workspace dir (`git clone` from
`repo_url` or `local_path`, or an uploaded source bundle from `kern exec`),
so job containers never need credentials.

## Scheduler

`ClaimJob` is a single statement: pick the oldest queued job whose `runs_on`
labels are a subset of the runner's labels, `FOR UPDATE SKIP LOCKED`, flip to
`in_progress` with a lease, insert a `job_leases` row. A least-loaded guard
makes a runner yield to any eligible online runner carrying fewer in-progress
jobs — placement prefers idle runners inside the matching label set.

Heartbeats carry a live utilization snapshot (`cpu_percent`, `mem_used_bytes`,
`disk_free_bytes`, `active_jobs`) stored on the runners row; the runners API
and dashboard read it back. The authoritative active-job count stays in the
jobs table — the payload field only corroborates it.

- Lease: `KERN_JOB_LEASE` (default 90s), renewed every 20s by the runner,
  including during prepare (checkout/pull) since the first implementation let
  long image pulls expire the lease mid-job.
- Reaper: expired lease → requeue, or `completed/failure` at max attempts.
- Concurrency groups: `PromoteQueuedRuns` serialises runs sharing a group;
  `cancel-in-progress` is not implemented yet (see compatibility.md).
- Cancellation: run/job cancel flips status; the runner's `cancel-check` and
  lease renewal both observe it and kill the in-flight step.

## Secrets

AES-256-GCM at rest (`KERN_SECRET_KEY`, exactly 32 bytes). Values are delivered
to runners only over the per-job `/secrets` endpoint, never in the claim
payload. Runs with trust level `fork` or `pull_request` get `{blocked: true}`
and no values. Log masking runs server-side at ingestion (`secrets.Masker`)
against every secret value for the run's project + org.

## Auth

- Users: argon2id passwords; sessions are random tokens stored SHA-256 hashed,
  carried in an HttpOnly cookie (`kern_session`, 14d). Cookie writes are origin
  checked (CSRF). Login is rate limited in-process.
- API tokens (`kern_…`) and runner tokens (`kernr_…`): random, stored hashed.
- The `kern` CLI authenticates with an API token. `POST /auth/token` exchanges
  email+password for one (rate-limited, no cookie) — `kern login` uses it.
- Runner registration accepts either a one-time enrollment token (`kerne_…`,
  table `runner_enroll_tokens`, expiring, single-use, minted via
  `POST /enroll-tokens` or `kern runners enroll`) or the shared static join
  token (`KERN_RUNNER_JOIN_TOKEN`), which remains as a dev escape hatch.
  `kern-runner join` performs the exchange on the target host and writes an
  env file plus a systemd user unit; `kern-runner --config envfile` (or
  `--token`) runs on the stored credential.

## Storage

PostgreSQL is the single source of truth for run/job state. Logs and artifacts
live in the blob store (`blobs.Store` interface); steps reference them by
`log_ref`. The filesystem driver writes under `KERN_BLOB_DIR`; an S3/MinIO
driver belongs behind the same interface when needed.

## ADR-001: pgx with hand-written scans instead of sqlc

The skill default is sqlc. The query surface moved fast during M0–M3 (claim
CTEs, reaper UPDATEs, joined run/job reads) and hand-written SQL with explicit
`Scan` lists made mismatches visible at code-review time. Revisit when the
schema stabilises; the store is one package, so adoption is local.

## ADR-002: local auth instead of Better Auth

Better Auth is a Node service; the control plane is one Go binary with no Node
runtime. Argon2id + hashed opaque tokens covers the same ground with zero new
dependencies and keeps runner/API/user credentials on one code path
(`internal/auth`). A hosted identity provider is a deployment concern, not an
M0 one.
