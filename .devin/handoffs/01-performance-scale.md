# HANDOFF 01 — Performance, caching & scale

**Status:** queued — pick up when 02/03 are not blocking you. Suggested order: this first.
**Source:** distilled from the "KERN CI/CD Overview" report (2026-11-07) + `docs/production.md`.
**Scope ownership:** scheduler (`internal/orchestrator`), store queries/migrations, runner cache/GC (`internal/runner`, `gc.go`, `disk.go`), blob/cache backends. Do NOT touch `apps/web` styling — the Plot Soft port is in flight on `feat/console-interactivity`.

## Mission

Make KERN faster under load and cheaper at idle: measured cache locality, automated
disk hygiene, bounded scheduler cost, and a credible answer to "how do runners scale".
Profile before optimizing — several report recommendations are already done.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `project.md`, `plan.md`, `docs/architecture.md`, `docs/production.md`,
   `docs/compatibility.md`.
3. **Branch state:** `feat/console-interactivity` holds the uncommitted Plot Soft UI
   port. Branch all work off `origin/master` in a git worktree
   (`git worktree add ~/.t3/worktrees/KERN/<name> -b feat/<name> origin/master`).
   If the UI branch has merged by the time you start, rebase onto it instead.
4. Local loop: `just dev` (api :8080), `just runner`, `just test`;
   pg in docker `kern-pg-dev` :5432; web `cd apps/web && pnpm dev` (:5173).
   Login `admin@kern.local` / `kern-admin-password`.

## Rules (non-negotiable)

- J.A.R.V.I.S. voice: polite, dry, brief. No emojis.
- Ponytail: smallest correct diff, stdlib before deps, no speculative scaffolding.
  New non-trivial logic leaves a runnable check.
- Never fake/silence behaviour — unsupported features emit diagnostics.
- Pre-push gate: `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- PR-first workflow; never push to main. Commits carry trailer
  `Authored By: TDvorak <info@tdvorak.dev>`.
- No GLM subagents; `subagent_explore`/`subagent_general` only if unavoidable.
- Memory loop: append durable facts to `~/agent-memory/projects/kern.md`, commit, push.

## Verified state — already done, do not rebuild

- Least-loaded claim tiebreaker exists (runner yields to less-loaded eligible peer).
- `KERN_RUNNER_CONCURRENCY`, `KERN_RUNNER_LOAD_GATE` (loadavg pause), claim backoff
  1s→30s exponential, per-runner job caps.
- Persistent runner cache: `<work-root>/_cache` → `/kern/cache` in every job
  (`KERN_CACHE_DIR`); measured warm kern.yml run 13s vs 100s cold on CT 908.
- Image GC: `image-usage.json` tracking, `KERN_RUNNER_IMAGE_MAX_AGE` (168h) idle prune.
- `actions/cache` + restore/save, restore-keys, deferred post-job save, trust-scoped
  (PR reads trusted scope, saves own), 7d eviction sweep (`KERN_CACHE_MAX_AGE`).
- Persistent tool cache `_tool`; Playwright browsers cached across jobs.
- Service containers get per-port locks + per-job network; `localhost:<port>` works.
- `strategy.split` fan-out + `kern split` advisory (fnv-hash) exist.
- Blob store: filesystem driver + S3/MinIO driver (`internal/blobs/s3.go`,
  `KERN_BLOB_S3_*`, Append = read-modify-write ≤32MiB).
- Artifact expiry 90d sweep; log retention `KERN_LOG_RETENTION`; webhook retention
  `KERN_WEBHOOK_RETENTION` (30d); audit events exist.
- Prometheus `/metrics` exists and is auth-gated (Bearer read token).
- Scheduler claims via single-statement `FOR UPDATE SKIP LOCKED` + leases — already
  multi-instance safe at the claim level.
- Watchdog fails jobs past `timeout_seconds`; `CancelOrphanedJobs` reaper in Tick.

## Scope — ordered queue

1. **Metrics baseline (do this first, it gates everything else).**
   Audit what `/metrics` actually exports today. Add only what's missing, with
   LOW cardinality (never PR numbers, SHAs, or run ids as labels):
   queue wait (job created → claimed), claim latency, webhook→run latency,
   runner utilization, step-duration histogram, cache hit/miss counters.
   Verify series populate under a real run on the dev stack.
2. **Cache hit rate + locality.** Measure current hit rate on real repos
   (the 13-repo porting fleet generates traffic). Confirm `cache_entries` ride
   `blobs.Store` so the S3 driver covers them; if cache has a filesystem-only
   path, route it through the store. Document/verify a MinIO-on-LAN option for
   the Proxmox fleet (RustFS already proven at invico-minio:19000 pattern).
   Audit which `setup-*`/tool downloads bypass `_tool`; add the cheap ones.
3. **Image pull cost.** Cold job = image pull dominates. Add per-runner
   pre-pull list (env `KERN_RUNNER_PREPULL` or config) seeded with the fleet's
   actual image set; measure pull time before/after on CT 908. Evaluate a
   LAN pull-through registry mirror — document the winner in `docs/deploy.md`,
   implement only if the win is >2×.
4. **Disk hygiene automation.** `gc.go`/`disk.go` exist — wire them: disk-pressure
   threshold → aggressive prune (docker system/builder prune, old workspaces,
   keep `_tool`+`_cache`), container-log rotation check, workspace delete post-job.
   Induce pressure in a test and show reclaim numbers.
5. **Scheduler cost check (measure, then decide).** Benchmark claim + Tick under
   simulated backlog (insert N pending jobs, time claims). `SKIP LOCKED` is fine;
   bin-packing/backfill from the report is ONLY justified if p95 claim latency or
   utilization gaps show up in the data — do not implement speculatively.
   Cheap win available now: cap `strategy.split`/matrix fan-out by eligible-runner
   count so a 50-way matrix on 5 runners doesn't churn the queue.
6. **DB hygiene.** `EXPLAIN ANALYZE` the claim query and the hot list queries
   (runs list, runners list). Add indexes only where plans show seq scans —
   migration file, not live DDL. Check autovacuum pressure on `jobs`/`steps`
   under churn; document postgres tuning for the compose deployment.
7. **Autoscaling — decision, not code (yet).** Fleet is 5 static Proxmox runners;
   no K8s, no cloud ASG. Deliverable: a short `docs/autoscaling.md` comparing
   (a) actions-runner-controller for operators who run K8s, (b) Proxmox pct-clone
   scaler keyed on queue depth (heavy: needs API + orchestration), (c) status quo
   + enroll-token runbook (exists: `kerne_` tokens, `kern-runner join`, systemd
   unit). Recommend (c) now + (a) as documented path; revisit (b) only if queue
   wait p95 becomes a real complaint. Get user sign-off before building (b).
8. **KPI evidence.** Report wall-clock numbers: warm vs cold run, cache hit rate,
   disk reclaim, claim p95 — before/after, on CT 908 production or clearly-labeled
   dev. Numbers, not adjectives.

## Explicitly out of scope

- Runner autoscaler implementation (decision doc only, item 7).
- gVisor/Firecracker/microVM isolation — that's handoff 02.
- UI/dashboard work — handoff 03, and the Plot Soft port is uncommitted anyway.
- Multi-org, SSO/OIDC, quotas — handoff 03 / `docs/production.md` ledger.
- Reusable-workflow `strategy`/`concurrency`/`secrets:` maps, `permissions:`,
  OIDC `id-token` — compat tail, separate effort.

## Traps (already bitten)

- `pkill -f X` kills your own `sh -c` — use `pkill -x` or PID from `ss -tlnp`.
- `go test ./internal/store` TRUNCATES the dev DB — rejoin runner + resync after.
- Job containers: `--user 1000`, `--read-only`, work dirs need explicit 0777
  chmod (umask eats MkdirAll mode); git needs `safe.directory=*` env.
- Docker Desktop bind mounts only under `$HOME`; on CT 908 work-root paths must
  match inside/outside the runner container.
- Rebuild `.data/kern-*` binaries before trusting local runtime behaviour.
- Fleet SSH rotates — check `~/agent-memory/projects/kern.md` for current access
  (last known: Proxmox host via `~/.ssh/proxmox_devin`; NetBird/LAN state varies).
- expressions evaluator only indexes `map[string]any`, never `map[string]string`.
- `blobs.ReadFrom` caps 256KiB — `blobs.Open` for full objects.

## Done means

- `/metrics` exports the new series and they populate on a real run (evidence:
  scrape output).
- Cache hit rate measured and reported; any filesystem-only cache path routed
  through `blobs.Store`.
- Disk-pressure GC demonstrated (before/after `df` numbers).
- Claim latency p95 measured under backlog; fan-out cap shipped if justified.
- `docs/autoscaling.md` written; user decision recorded.
- `docs/compatibility.md`/`docs/deploy.md` updated where behavior changed.
- All PRs green through the pre-push gate; memory file updated.
