# Changelog

All notable changes to KERN. Format follows [Keep a Changelog](https://keepachangelog.com/);
versions follow [SemVer](https://semver.org/) — minor bumps for features, patch for fixes.

## [0.21.0] - 2026-11-25

### Added
- Agent offload surface: `kern_trigger_run` (MCP) now accepts `workflow_yaml`,
  `workflow_path`, and `bundle_ref` — the same inline/bundle trigger core as
  `POST /projects/:p/runs` (extracted into a shared helper). MCP agents can
  offload heavy builds (Android/Gradle, Rust, docker images, big suites) to the
  fleet end-to-end: REST `PUT /projects/:p/source` for the binary upload,
  `kern_trigger_run` to start, `kern_wait_run`/`kern_step_log` to watch,
  `GET /artifacts/:id/download` for results. New `offload_heavy_build` MCP
  prompt documents the exact sequence for CLI-less agents.
- `kern exec` infers the project from the directory's git remote when
  `--project` and config are unset — inside a clone it needs no flags.
- `kern exec` Ctrl-C now cancels the remote run instead of leaking fleet time
  (fire-and-forget remains `kern runs trigger`).

### Fixed
- Inline-trigger compile failures keep their diagnostics in the error message
  instead of a bare "workflow does not compile".
- Deployment: postgres service gains `restart: unless-stopped` in
  `deployments/docker-compose.yml` — a missing restart policy turned a host
  reboot into a multi-hour control-plane outage (2026-11-25 incident).

## [0.20.1] - 2026-11-23

### Fixed
- Credential lookups no longer report a store/database failure as
  `401 invalid_token`: a missing row is still a 401, but a real store error is
  now `500 internal`. A postgres outage previously invalidated every API token,
  session, and runner token fleet-wide (CT908 full-disk incident, which wedged
  the v0.20.0 release build for ~25 minutes). Password-login paths (`POST
  /auth/login`, `/auth/cli/token`) reported store errors as
  `invalid_credentials` the same way — fixed too.
- `gh-incoming` cache-upload chunks no longer leak: the chunk object is
  deleted when its `InsertGHChunk` row write fails, and the 10-minute upload
  sweeper now also reclaims rowless `gh-incoming/*` objects left behind by
  crashes between blob write and row commit. The blob store gained `List`
  for this (filesystem + S3 drivers).
- CLI device-auth `verification_url` now uses the configured public origin
  (`KERN_PUBLIC_URL`) instead of the request origin, so it advertises
  `https://` when the API sits behind a TLS-terminating proxy.

## [0.20.0] - 2026-11-23

### Added
- Browser-backed CLI authentication (`kern auth login` / `kern login`) via
  short-lived device verification codes (`/auth/cli`, `/api/v1/auth/cli/*`):
  interactive server URL picker with connectivity probe, automatic browser
  launch + one-click web approval page, plus `--no-browser`, `--with-token`, and
  `--email`/`--password` automation modes.
- Multi-server CLI contexts (`kern context list|use|current|set-url|rm`,
  `kern auth status|switch`, global `-u`/`--url`, `-c`/`--context`, `-p`/`--project`,
  `-j`/`--json`, `KERN_URL`/`KERN_CONTEXT`), `kern open` web dashboard launcher,
  TTY-aware status colors, command typo suggestions, and expanded `runs trigger`
  (`--split`, `--split-auto`, `--runners`, `-f key=value`, `--watch`/`-w`, `--open`),
  `runs list` (`--branch`/`-b`, `--cause`, duration column), `runs remediate --dismiss`,
  and `settings` (`auto_concurrency`, `prime_workflow`, `prime_auto`) flags.
- Runtime workflow dispatch controls on project detail pages and the trigger API:
  support `each_runner` fan-out, explicit `runners` targeting, and dynamic runtime
  sharding (`split` / `split_auto`) for jobs without workflow-declared splits.
  Sharded jobs now also receive 0-based `KERN_SHARD_INDEX` and `KERN_SHARD_TOTAL`
  alongside `KERN_SHARD` and `KERN_SHARDS`.
- Exposed `auto_concurrency`, `max_transient_retries`, `disk_alert_pct`, `toolset`,
  `prime_workflow`, and `prime_auto` across the settings API and web UI.
- Expanded step diagnosis and autoheal coverage: `cache_permissions` (`EACCES`
  under `/kern/cache`) is now fixable via autoheal, plus new autohealable
  diagnoses for `stale_lock` (`.git/index.lock`), `pkg_lock` (`dpkg` interrupted),
  and `npm_cache` (`EINTEGRITY`). Run detail now displays a structured Autoheal
  Remediation card with one-click Approve & retry and Dismiss actions, plus
  single-job re-run (`Re-run job`) and `config_warnings` surfacing.
- Runs page search input and server-side failure `classification` filter, linked
  directly from Usage failure-cause rows.
- Fleet Health KPI strip, filter pills (`All`/`Online`/`Busy`/`Issues`), search,
  accurate concurrency slot display, executor/label/drift badges, and structured
  runner cards on the Runners page.

### Changed
- Full web UI visual overhaul across light (`paper`) and dark (`ink`) themes:
  rebalanced dark-mode KPI and login surfaces, eliminated topbar navigation
  clipping at 1440px, fixed `.runrow` action alignment and mobile webhook banner
  wrapping, added FleetMap label collision avoidance, collapsed shard suffixes in
  Overview legend, improved plaintext variable and top-event KPI readability, and
  route-level `React.lazy()` code splitting (reducing main bundle from 842 kB to
  246 kB).
- Improved `ClaimJob` auto-assign scheduling tiebreak: when two eligible runners
  have equal slot utilization and CPU within margin, jobs yield to the peer with
  >15% more free memory headroom.
- Resolved Dependency Dashboard (#409) safe updates: removed deprecated
  `@types/dompurify`, updated `oxfmt` to `0.79.0`, `doublestar/v4` to `v4.10.7`,
  and `klauspost/compress` to `v1.20.5`.

### Fixed
- GHA chunked cache commits (`POST .../artifactcache/caches/:id`): atomically
  read uploaded chunk metadata in a transaction before deleting the reservation
  row, preventing PostgreSQL `ON DELETE CASCADE` from wiping chunk rows prior to
  commit (`ClaimGHUpload`).
- Multi-runner fan-out (`each_runner`) and explicit `runners` targeting:
  `ClaimJob` and `UnmatchedQueuedJobs` now implicitly include each runner's own
  name in its effective label set, and `expandEachRunner` always assigns unique
  `job @ runner` keys even when a runner's stored labels omit its own name
  (preventing `jobs_run_id_key_key` unique constraint violations).

## [0.19.3] - 2026-11-22

### Fixed
- The shared `kern` buildx builder is now registered under the runner
  home (`<WorkRoot>/home/.docker`) that job containers mount at
  `/github/home`. Previously the instance lived in the agent's own
  home, so `BUILDX_BUILDER=kern` failed inside every privileged job
  with `no builder "kern" found` (observed: cloudreve Release `server`
  GoReleaser docker builds). Orphaned `buildx_buildkit_kern0`
  containers from agent-home registrations are removed on recreate.
- Mirror checkout now passes `--tags --force` when fetching, matching
  `git clone` tag visibility. GoReleaser and other release tooling
  require tags to exist locally; the mirror previously fetched only the
  target ref, so release jobs failed with `git doesn't contain any
  tags` (observed: cloudreve Release `server` job). Worktrees share
  mirror refs, so fetched tags are visible inside the job.

## [0.19.2] - 2026-11-22

### Fixed
- Git checkouts on native docker-executor runners: `checkoutViaMirror`
  ran git against the literal container path `/kern/cache`, which on
  hosts where the agent is not itself containerized resolved to a stale
  unrelated directory — mirrors and worktree admin dirs never reached
  the tree job containers mount, so every in-job git command failed with
  `fatal: not a git repository` (observed: cloudreve Release
  prepare/server jobs, CI `dorny/paths-filter`). The agent now links
  `/kern/cache` to `<WorkRoot>/_cache` at startup; stale real
  directories are moved aside, never deleted.
- Untrusted docker jobs (per-job cache mount) now skip mirror checkout
  and take the direct-clone path — a shared `_repos` mirror can never be
  visible inside their container.
- `_repos` mirrors are exempt from the bounded-cache sweep: they back
  live job worktrees, and the idle gate cannot be trusted across agent
  restarts.


## [0.19.1] - 2026-11-22

### Fixed
- Runner disk autoheal: images pulled by steps through the docker socket
  or left by in-job `docker build` have no ledger entry and accumulated
  unbounded (observed: 254 images / ~38G on one runner at 95% disk). The
  critical tier now also runs `docker image prune -af`, idle-gated, so
  unreferenced images are reclaimed instead of only dangling layers.
- Fleet self-update convergence: the offer lease held its full 15-minute
  window even after the holder reached the target version — the lease is
  now released when a heartbeat reports the runner at/past the release,
  and runners wait up to 4 minutes for a transient busy state (claim
  long-poll, job teardown) before failing the attempt and backing off.


## [0.19.0] - 2026-11-22

### Added
- Automatic concurrency grouping for workflows that declare none: CI
  events (`pull_request*`, `push`) get a platform default group per
  (workflow, PR|branch) so a new commit supersedes the stale run
  instead of piling up — `pull_request*` cancels in-progress, `push`
  cancels on non-default branches and serializes on the default branch.
  `auto_concurrency: "off"` in project or org settings disables;
  explicit `concurrency:` always wins.

### Fixed
- Privileged jobs declaring `services:` failed every retry with
  `network kern-svc-<job> not found` — the per-job bridge was only
  created on the unprivileged path (#410).
- Root-run docker actions poisoned the shared `/kern/cache` with
  root-owned files, breaking later uid-1000 `npm ci`/`pnpm` steps; the
  runner now hands cache ownership back after each action, docker
  actions mount the job-scoped cache, and `EACCES` cache failures
  classify as `cache_permissions` instead of `missing_binary` (#411).
- CLI/diagnose polish: single `v` version prefix everywhere, jobs stamp
  runner name/version at claim time so `kern runs show`/`diagnose`
  report historical identity, successful `continue-on-error` steps no
  longer dump 8KB log tails, and undefined `secrets.*`/`vars.*`
  references warn in step setup and `kern diagnose` (#413).


## [0.18.1] - 2026-11-22

### Fixed
- Startup panic: `0037_runner_concurrency.sql` and
  `0037_webhook_delivery_org.sql` shared goose version 37 after merging
  from parallel PRs, crash-looping the API at migrate. Renumbered to
  0042; neither file was applied anywhere yet.

## [0.18.0] - 2026-11-22

### Security
- Enforced `org_id` tenant isolation across the store layer and every
  API/MCP handler. Foreign resources resolve to not-found (no existence
  oracles), run/cache/artifact/job prefix lookup is org-scoped, and grant
  scoping applies before ambiguity decisions (#368).
- Fork and pull-request trust boundaries: fork/PR runs are untrusted by
  default — interactive exec is denied, fork cache writes are isolated
  per pull request, and untrusted runs can never reach the privileged
  service-registry path (#368).
- Secrets, masking, grants and RBAC: masked token display everywhere
  (including `kern auth` output), grant-scoped tokens can no longer learn
  about out-of-grant projects, and path-safety checks block traversal in
  artifact/cache/job/run prefixes (#368).

### Added
- Streamable-HTTP MCP transport with real sessions: `initialize` mints a
  `Mcp-Session-Id`, `GET /mcp` serves the session SSE channel, `DELETE`
  closes it, and `kern_wait_run` emits `notifications/progress` for
  clients that pass `_meta.progressToken`. Stateless POST remains valid
  (#377).
- MCP tool expansion: `kern_diagnose_run`, `kern_search_logs`,
  `kern_wait_run`, `kern_sync_project`, `kern_list_artifacts`, plus ops
  surface — `kern_doctor`, `kern_predict_run`, `kern_optimize_project`,
  `kern_mint_agent_token`, remediation/deployment approvals and
  `kern_exec_job` sandboxed exec on trusted live jobs (#377).
- MCP resources and prompts for agent-native CI workflows (#377).
- `kern optimize --profile <run>`: queue-vs-exec waterfall, longest
  `needs`-DAG critical path, and `--flamegraph` speedscope JSON.
  `kern top` ships a dependency-free fleet dashboard. `kern upgrade`
  verifies sha256 against the release checksum asset. `kern init`
  bootstraps projects (#376).
- `kern testbox --with-needs` executes the transitive `needs:` DAG in
  dependency order; `-i`/`--interactive` on `kern diagnose` opens an
  alternate-screen pager. `kern runs watch` multiplexes run/job/step/log
  SSE (#376).
- `GET /runs/:id/diagnose` single-bundle endpoint feeding `kern
  diagnose`, `kern optimize --profile`, MCP diagnosis and web UI
  (#376).
- Web UI: job/step deep links (`?job=&step=`), `#L<n>` log anchors with
  group collapse and incremental `LogView`, gantt queue-wait and
  critical-path segments, bulk runner drain/undrain/delete, Command-K
  quick actions, copy-AI-diagnosis, URL-synced filters (#378).
- Runner protocol additions: `GET /runs/:id/stream` multiplexed SSE,
  runner-side `/cache/:key/prepare|commit` presigned upload pair,
  `cache/restore?direct=1` signed-download handoff, LISTEN/NOTIFY live
  log wakeups, opt-in OpenTelemetry, `/metrics` Prometheus exposition
  (#374, #369).

### Changed
- Scheduling is slot-aware: runners report concurrency, the fleet
  scheduler yields to free slots, longest-pole-first ordering, DAG
  advancement to fixpoint per call, and matrix sibling fail-fast
  cancellation preserved (#372).
- Runner speed: warm toolchain caches, bare-mirror checkouts via
  `git worktree`, persistent BuildKit builder for privileged jobs,
  parallel service image pulls, digest-pinned image skip, passwd probe
  memoization (#372).
- Cache archives default to zstd (klauspost/compress) with magic-sniffed
  restore — gzip blobs and mixed fleets keep working;
  `KERN_CACHE_CODEC=gzip` opts back (#369).
- ghcache chunked uploads are now HA-safe: sessions live in Postgres and
  chunks land as immutable per-offset blob objects, so any API replica
  serves any PATCH and commit claims the row atomically (#369).

### Fixed
- Security: fork cache-scope poisoning, unmasked tokens, grant/RBAC
  bypasses, secret path traversal (#368).
- Workflow engine: orchestrator run/job races, condition-gating holes,
  GHA expression semantics, matrix, reusable-workflow secrets, cron
  `dow=7`, YAML decoding edge cases (#371).
- Runner reliability: live-exec deadlock/truncation, port-forward
  half-close, service registry credentials, zombie containers,
  cancellation/watchdog races, log-loss ordering and chunking (#370).
- Healing: disk/memory GC bugs, reactive mid-run remediation,
  rolling self-update, flaky quarantine, service health recovery
  (#373).
- Performance: hot-path indexes, N+1 collapse, per-request decrypt
  storm, sync webhooks, invalid Prometheus races, hot polling (#374).
- CLI: global flag stripping, 60s timeout kills, log truncation,
  testbox host-container reaping, short-ID traps, `--json`/NDJSON,
  completions and OpenAPI/SKILL parity (#375).
- Web: broken rerun/undrain controls, log truncation, SSE freeze, the
  150-request detail fan-out (replaced by `GET /runs/jobs` batch),
  Command-K semantics, placeholder and legend correctness (#378).
- Cache: cross-scope poisoning, fork host-cache RCE, blob OOMs, mutex
  stalls, symlink preservation, unique blob refs (#369).

## [0.17.0] - 2026-11-21

### Added
- Security scanning dogfoods on every push/PR via
  `.github/workflows/security.yml`: Trivy filesystem scan (vuln +
  misconfig + secret, HIGH/CRITICAL gate), OSV-Scanner dependency CVEs,
  Gitleaks working-tree and git-history secret scans, and a CycloneDX
  SBOM uploaded as a run artifact. A weekly cron rescan surfaces newly
  published CVEs without a code change.
- `kern doctor` warns when a runner's version trails the fleet's newest
  comparable release — a stuck self-update used to surface only as
  unexplained behavioural drift.
- `KERN_RUNNER_PSI_GATE` — optional cgroup-v2 PSI overload gate for Linux
  runners. When set, claiming pauses while the worst PSI `some avg10`
  across cpu/memory/io exceeds the threshold, replacing the loadavg gate.
  PSI is hierarchical per-cgroup, so an LXC runner measures its own
  container instead of the hypervisor host's loadavg — a saturated
  neighbour no longer pauses healthy runners fleet-wide. Unreadable PSI
  falls back to the loadavg gate; `0` keeps existing behaviour.
- Curated install plans for the security scanners `trivy`, `osv-scanner`
  and `gitleaks` — pinned releases with per-arch sha256 digests across
  Linux, Windows and macOS, matching the `gh`/`minisign` self-heal
  contract. Declare them in the org `toolset` setting and host executors
  keep every runner scanner-equipped; `trivy` reports unsupported on
  Windows ARM64 because upstream ships no such asset.

### Fixed
- Prepare-phase git clone/fetch transport deaths (`RPC failed`,
  `Could not resolve host`, `remote end hung up`) now classify as
  transient and auto-requeue under `max_transient_retries` instead of
  failing the job outright. Auth failures still classify `prepare`.
- Host toolset drift can now clear. `missingToolsReport` returned a
  pointer to a nil slice once a sweep came back clean, which marshals to
  JSON `null`; the server reads `null` as "no report" and keeps the
  stored list, so healed drift stayed recorded forever. Empty reports
  now serialize as `[]`.
- Host toolset self-heal now covers macOS executors. The curated `gh`
  and `minisign` install plans only had Linux and Windows branches, so a
  Darwin host reported permanent drift it could never heal (`gh` installs
  from the pinned macOS zip for amd64/arm64 into a writable PATH dir;
  `minisign` has no pinned Intel macOS binary upstream, so it installs
  via Homebrew when present and reports the gap otherwise).
- `npm config get cache` no longer fails inside jobs on host executors.
  npm redacts UUID-looking values and refuses to print them; the per-job
  work root is a UUID, so a HOME-derived cache path made
  `actions/setup-node`'s `cache: npm` lookup exit 1 (seen on the macOS
  runner). `npm_config_cache` now points at the shared uuid-free
  `_cache/npm` dir, which also warms the npm cache across jobs.

## [0.16.0] - 2026-11-19

### Added
- API tokens take optional `projects` and `labels` grants. A project list
  limits the token to those slugs (other projects, secrets, runners and
  MCP return 403; run ids outside the grant look like a 404). A label
  list is a runner pool: dispatch and rerun refuse a job whose `runs-on`
  is not a subset of the list. Empty lists stay org-wide. A scoped token
  cannot mint a wider token or revoke any token but itself.
  `kern token create --projects S --labels pool-a` and
  `kern artifacts delete <id>`. (#356)
- Per-token job concurrency caps for shared-pool tenants.
  `kern token create --concurrency N` (or `max_concurrent` on
  `POST /auth/tokens`) limits how many in-progress jobs that token's
  dispatches may hold fleet-wide; the claim query queues the excess
  behind the token's own running work instead of letting one tenant
  flood the pool. `0` stays uncapped, sessions/webhooks/cron are never
  token-capped, and a capped token cannot mint a higher or unlimited
  cap. Runs record their dispatching token in
  `workflow_runs.dispatch_token_id`. (#356)
- `actions/upload-artifact` honours `retention-days` (clamped to
  1–365, default 90), forwarded as `?retention_days=` on the upload.
  Re-uploading an existing name refreshes its expiry. (#356)

### Fixed
- A runner that loses its job lease (control plane requeued it, or
  renewals fail for longer than the lease window) now kills the local
  job instead of leaving the container to write. Disk-budget samples
  give `du` eight seconds and do not fall through to a full tree walk
  on timeout. `docker stats` samples use the same bound.
- The disk sweep removes containers a job started whose labels still
  name a `/github/workspaces/<job>` that is gone. Compose test stacks
  are not named `kern-*`, so the old sweep left them running.

## [0.15.1] - 2026-11-17

### Fixed
- Windows host-executor toolset heal: curated `gh`/`minisign` install
  plans branch on `uname -s` and pull the pinned win64/win-arm64
  archives into `KERN_TOOL_BIN` — the first writable, non-system,
  absolute PATH directory — instead of hardcoding Linux tarballs and
  `/usr/local/bin`, which do not exist under Git Bash. Uncurated tools
  no longer attempt the Linux-only apt/apk fallback on Windows; they
  stay in `missing_tools` with a named warning, and identical
  still-missing sets back off for 30 minutes instead of re-downloading
  every sweep. (#353)

### Changed
- Dependency bumps: go module group — pgx 5.13.0, minio-go 7.4.1,
  goose 3.30.1, x/crypto 0.59.0, x/term 0.48.0 (#351); apps/web npm
  group — lucide-react 1.73.0, marked 18.2.0, vite 8.3.8,
  @vitejs/plugin-react 6.3.0, oxlint 1.93.0, oxfmt 0.78.0 (#352).

## [0.15.0] - 2026-11-17

### Added
- Per-project job concurrency quota: `max_concurrent_jobs` caps how many
  runner slots one project may hold fleet-wide, enforced atomically
  inside `ClaimJob`'s claim CTE — one repo's CI burst can no longer
  starve the queue. `0` (default) is uncapped. Exposed via project
  settings API, `kern settings set max_concurrent_jobs <n> --project S`,
  openapi, and a Job concurrency field on the project page; capped jobs
  surface `project concurrency cap reached` in QueueReason. (#348)

### Fixed
- Docs: compatibility ledger corrected — explicit reusable-workflow
  caller `secrets:` maps already ship via `callSecretsMap`; only caller
  `strategy`/`concurrency` remain diagnostic. (#348)

## [0.14.0] - 2026-11-17

### Added
- Self-healing job environments: curated install plans for `gh` and
  `minisign` (release tarballs, arch resolved in-shell) replace the
  doomed `apt-get install gh`; `tauri-driver`/`pnpm` report named
  toolchain hints. New diagnosis kinds `build_killed` (transient,
  auto-requeues), `stale_lock` and `pnpm_store` (fixable). Org setting
  `toolset` rides heartbeats so host executors report
  `runner.toolset_drift` audits; `self_heal=auto` installs through the
  same curated plans. `kern runners`/`kern doctor` surface drift.
  `deployments/install-runners.sh` derives `lxc-<CT>` labels instead of
  hardcoding `lxc-901`; `ci-cleanup.sh` sweeps stale ployz-build dirs
  and verifies the pnpm store. (#345, closes #344)
- System-wide web colour grammar: `catColor()` pinned hues for events
  and environments, `jobTint()` ties job/workflow names to their
  fingerprint segment colour, `projectColor()` applies everywhere a
  project slug renders including the usage donut. Applied across runs,
  overview, run detail, gantt, queue, fleet map, public run, projects,
  deliveries, environments, cache, artifacts, secrets and usage. (#341)

## [0.13.0] - 2026-11-12

### Added
- Live per-job check progress: a running job's check title reads
  `name: step X/Y` and the summary narrates the current step, queue wait,
  and a median-history ETA — refreshed at every step boundary. Check
  `started_at`/`completed_at` no longer reset on mid-run updates. (#331)
- Public run JSON progress: `GET /api/v1/public/runs/:id` (org
  `public_ci`) now returns `current_step`, `step_number`, `step_count`,
  `queue_wait_seconds`, `eta_seconds` per job — the session-free surface
  for agents and API consumers. (#331)
- Median-history ETA: `StepMedians` prices each step at p50 wall-clock
  over the workflow's last 90 days; steps without history fall back to
  the job-local median. (#335)
- Release fast path: tag pushes and `release` events seed jobs with
  priority 10 unless the workflow sets one; reruns stay boosted. (#334)
- `KERN_SCALER_WATCH`: vm-scaler ticks in-process every N seconds instead
  of once per timer pass, waking scaled-to-zero hosts near queue time;
  `deployments/kern-scaler.service.example` ships a matching unit, and
  `KERN_VM_START_COOLDOWN` bounds start re-fires during boot. (#334)
- Disk reclaimer hardening: the work-root sampler tightens to 5s past
  70% of budget; an unset `KERN_JOB_DISK` falls back to half the free
  filesystem at job start; host executors get the same sampler as
  containers; `go-build*`/`pip-*`/`cargo-*` join the stale-temp sweeps;
  BuildKit prune keeps the newest cache up to `KERN_BUILDX_KEEP`
  (default 20 GiB) instead of wiping it. (#333)

### Fixed
- `disk_full` and `disk_quota` are transient-retry kinds: the job
  requeues within `max_transient_retries`, warns and audits
  `runner.disk_requeue`, and claim anti-affinity lands the retry on a
  different runner while an eligible peer is online. (#332)

## [0.12.0] - 2026-11-12

### Added
- Check-run `output.text`: failed and timed-out job/run checks now carry a
  masked markdown excerpt — failing step name, exit code, diagnosis, and a
  4 KiB log tail (capped at 8000 chars, inside GitHub's output limits) — so
  a failed lane can be diagnosed without opening the console. (#324)
- `check_run.rerequested` and `check_suite.rerequested` webhook support:
  GitHub's Re-run button now resets the matching KERN jobs through the
  existing rerun machinery — a per-job check reruns that lane plus its
  dependents, a run or suite rerequest resets everything that did not
  succeed. (#323)
- Warn-tier idle reclaim: under non-critical disk pressure with no job in
  flight the runner now also sweeps aged `/tmp` scratch (72h) and tightens
  the persistent cache to a 3/4 floor — previously only the critical tier
  escalated past `docker prune`.
- `kern doctor` fleet advisories: runners below the recommended minimums
  (40 GiB disk total, 10 GiB free, 4 GiB RAM) report a warn with the
  recommended size, so undersized hosts are caught before claim gates flap.
- GitHub App manifest now requests `actions: write` so trusted workflows
  can dispatch follow-up runs (`gh workflow run`) with the per-run
  GITHUB_TOKEN (#322). Apps installed before this grant must accept the
  permission update in their GitHub settings.
- Failure diagnosis recognises "Resource not accessible by integration"
  and names the missing GitHub App scope as the fix, and a missing `gh`
  binary points at the kern baseline image instead of an apt install that
  cannot succeed.

### Changed
- Overview `Live now` renders compact rows instead of one large card per
  run: each row is a single fingerprint bar where every job is a segment
  on the run's clock, tinted by stage hue (lint/build/web) with running
  blue, failure red, and queued hatched — a matrix run no longer stacks a
  lane per job into a wall of cards. The overview is ~44% shorter on a
  busy fleet, and nine live runs fit in one viewport.
- Recent-runs rows gain the same slim fingerprint strip in their
  previously empty rail slot, so every row reads as a pipeline at a
  glance.

### Fixed
- `ACTIONS_RUNTIME_TOKEN` is now a real HS256 JWT carrying `ac`/`exp`/`nbf`
  claims, which buildx's `type=gha` cache backend requires before it will
  call `ACTIONS_CACHE_URL` — docker lanes using `cache-from: type=gha`
  failed in seconds with "token contains an invalid number of segments".
  Legacy `kc1` tokens minted before the switch still validate so running
  jobs keep cache access. (#321)
- Overview at phone widths: commit-graph lanes no longer consume a
  quarter of the viewport under 760px, the fifth KPI cell spans the grid
  instead of orphaning, and activity-map cells are capped so the map
  stops ballooning on small screens.
- Run detail's job-picker chips now wrap instead of pushing the page
  ~220px past the viewport edge on narrow screens; `main` clips
  horizontal overflow defensively.
- Vite dev proxy sets `changeOrigin`, so `KERN_API_ORIGIN` can point at a
  remote/tunnel origin that rejects a `localhost` Host header.
- Phone-width pass on remaining pages: page-header and card-header action
  rows now wrap under the title below 640px instead of clipping off the
  right edge (Projects, Secrets, Environments, Settings). Project detail
  fields go single-column on phones, mono KV values break long URLs, and
  the runners self-heal row wraps instead of crushing its label.
- Login art panel in dark mode: the connector illustration now uses theme
  variables instead of hardcoded white, so it renders dark-on-light when
  the panel inverts — previously an invisible white-on-white logo.

## [0.11.0] - 2026-11-11

### Added
- Host-load-aware scheduling: among equally job-loaded peers a runner now
  yields the claim to a runner whose heartbeat `cpu_percent` is at least
  15 points lower, so saturated hosts lose placement ties to cooler ones.
  Peers that cannot take the job — claim-gate-paused runners, host
  executors on fork/pull_request trust, non-docker runners while a
  `kern-docker` peer exists — no longer suppress claims they could never
  take. (#318, #310)
- Cross-platform host metrics: heartbeat `cpu_percent`/`mem_used_bytes`
  probes now populate on Windows (GetSystemTimes/GlobalMemoryStatusEx)
  and macOS (sysctl `kern.cp_time`/`vm.loadavg` + `vm_stat`), not just
  Linux `/proc`. The disk and memory claim gates therefore work on
  Windows and macOS hosts; the loadavg gate works on Linux and macOS
  (no Windows equivalent). (#318)
- `KERN_JOB_CONTAINER_ID`/`KERN_JOB_CONTAINER` expose the job container's
  id and name to steps for `docker inspect`/`exec` in DinD fixtures.
  (#317, #315)

### Fixed
- Orphan sweeps silently removed nothing: `docker ps -aq --format ...`
  makes the CLI ignore `--format` and print bare ids, so name parsing
  always failed — `kern-svc-*`/`kern-job-*`/`kern-egro-*` containers
  leaked indefinitely. Sweeps now use `docker ps -a --format` (no `-q`)
  and run before service port acquisition with one retry after a
  port-bind failure. (#317, #308)
- Infra-classified prepare failures (port allocation, network setup,
  `signal: killed`, OOM kills) now match transient signatures and
  requeue instead of failing the run. (#317, #309)
- Self-heal `java` remediation installs a real JDK per distro
  (`default-jdk-headless`, `openjdk21`, `java-21-openjdk-headless`)
  instead of the bare `java` package name. (#317, #313)
- Disk claim gate gains hysteresis: claims resume only after free space
  recovers to threshold + 3 points, ending boundary flapping on shared
  hosts. (#317, #311, #312)
- Trusted host-network job containers cannot `docker network connect` —
  documented in `docs/runner-contract.md` with the loopback-port
  workaround for DinD fixtures. (#317, #316)
- Host-executor `$GITHUB_ENV` propagation covered by an end-to-end test.
  (#317, #314)

## [0.10.0] - 2026-11-11

### Added
- Host-executor services: trusted `host` jobs with a working docker
  daemon on the machine (`kern-docker` label) now run declared
  `services:` natively — per-job bridge, declared `ports:` published on
  `127.0.0.1`, sibling services resolving by name. Port-less services
  stay DNS-reachable from sibling services only; no daemon logs an
  explicit skip instead of silently ignoring. (#304)
- Runner labels sync on every heartbeat, so token-mode runners that
  never re-register pick up their configured-name label and
  `each_runner`/`runs-on: <name>` pinning works for the whole fleet.
  (#303)

### Fixed
- Agent panic on failed action pre-resolution: `preFailed` was written
  on every resolution error path but never initialised — a transient
  action fetch failure panicked the entire agent mid-job and killed
  in-flight step processes (the intermittent Windows-runner
  `assignment to entry in nil map` crashes). (#305)
- `overLoaded()` warned on every heartbeat under sustained load; the
  gate now latches and logs transitions like the disk/memory gates.
  (#303)
- Daemon-death classification no longer reports `daemon_gone` on hosts
  with no docker binary at all — a job's own docker-client error stays
  user error when the host never promised a daemon. (#303)

## [0.9.0] - 2026-11-11

### Added
- Prime runs: `each_runner` on trigger bodies (`kern runs trigger
  --each-runner`) fans every job into one pinned copy per online runner,
  and project settings `prime_workflow` + `prime_auto=on` fire a warm-up
  run automatically when a new runner registers — cache/toolchain priming
  for cold hosts through the normal compile→claim→sandbox pipeline, no
  YAML extensions. Runners now advertise their configured name as a
  label, so `runs-on: <runner-name>` pins a host. (#293)
- Per-job container telemetry: peak cpu/memory/disk collected into
  `jobs.metrics` during docker jobs now surfaces on `kern runs show` and
  the web run detail. (#294)
- Fork-tier egress accounting: the job egress proxy's `NetIO` is sampled
  alongside container stats into `jobs.metrics.egress_bytes`, and the
  runners list shows a 24h-per-runner total. (#295)
- `kern doctor` lints the fleet: risky runner `env` overrides
  (`PATH`/`DOCKER_HOST`/proxy vars that leak into every job), project
  workflow health and diagnostics, and recent `missing_binary` runs —
  the empty-toolcache symptom class. (#296)
- Job/step profiling: `runs show` and the run detail show job duration
  and queue wait (`queued_at`→`started_at`), plus per-step durations in
  the CLI. (#297)
- Live claim-gate observability: runners report their engaged claim
  gates (`disk`, `memory`, `overload`) on every heartbeat into
  `runners.claims_paused` (migration 0033). Queued-job diagnostics now
  distinguish "matching runner(s) paused by claim gate" from "no online
  runner matches", split auto-capacity excludes paused runners, the web
  runner list renders `paused (<reasons>)`, and `kern doctor` warns.
  (#298)
- `daemon_gone` failure class: a dead docker daemon mid-step is
  diagnosed by signature plus a lazy `docker version` probe — infra when
  the daemon is gone, user error when a job's own docker client has no
  socket. The kind joins the transient set, so the job auto-requeues
  onto a healthy runner; the prepare-phase form is covered too. (#300)

### Fixed
- Idle-gated toolcache sweep removes markerless and empty
  `_toolcache/<tool>/<ver>` installs that previously counted as
  installed after interrupted fetches or partial GC (#286). The
  `<ver>`+`.complete` eviction unit stays atomic. (#291)
- Triggering a `disabled: true` workflow now returns HTTP 409
  `workflow_disabled` instead of a generic 500 (#289). (#292)
- The persistent cargo cache evicts in per-crate / per-repo units —
  `registry/src`, `git/{db,checkouts}`, and file-level `cache`/`index`
  entries — instead of dropping the whole registry tree as one unit at
  the cache cap. (#299)

### Changed
- `KERN_CACHE_MAX_BYTES` default raised 1 GiB → 10 GiB, matching
  GitHub's own per-entry cache ceiling — the old cap rejected real
  rust-cache saves (~1.4 GB) with `cache_too_large` (#290). The env var
  still lowers it for small deployments. (#301)

## [0.8.3] - 2026-11-11

### Fixed
- Job containers keep the job image's own `PATH` tail when prepending the
  provisioned node bin dir, instead of replacing it with a fixed base list —
  the v0.8.2 prepend broke toolchain images (`golang:1.27` jobs failed with
  `go: command not found`). The image's `Config.Env` `PATH` is probed at
  container create; host executors stop overriding `PATH` at all.
- Provisioned node runtimes now run `corepack enable` at install time, so
  `yarn`/`yarnpkg`/`pnpm`/`pnpx` shims exist in `_node` on every runner.
  setup-node's `yarn --version` probe previously only passed where a stale
  enable had left shims in the writable toolcache. Existing provisions
  self-heal on next job via a shim probe.
- The GitHub cache-service commit path no longer fails `500`: the pending
  upload's temp file was opened `O_WRONLY` but read at commit, so every
  cache save errored `bad file descriptor` (warn-only — restores masked it).

## [0.8.2] - 2026-11-11

### Fixed
- Job containers now prepend the provisioned node20 bin dir to `PATH`, so
  JavaScript actions can resolve `yarn`, `yarnpkg`, and `corepack`. This fixes
  `setup-node@v4` failing `yarn --version` when `cache: yarn` sees a lockfile;
  run steps already received the same path through `GITHUB_PATH`.

## [0.8.1] - 2026-11-10

### Added
- `jobs.<id>.resources` workflow key (KERN extension): `{memory, cpus,
  disk}` folds into the `KERN_JOB_*` env the executor already honours —
  cgroup memory/CPU limits and the work-root disk budget. Explicit env
  values win; trusted tiers only. GitHub ignores the key.

### Fixed
- Runner `version` and work-root disk capacity now ride every heartbeat
  instead of staying at their register-time values. A token-mode runner
  (one that authenticates with a runner token and never re-registers)
  reported its enroll-time version forever, so the control plane kept
  offering the same self-update every hold window — a download/swap/re-exec
  loop that never converged.
- The runner skips a self-update offer that names the version it is already
  running, so a stale `runners.version` can no longer re-install the same
  binary in a loop.
- Binary self-update no longer leaks its `kern-update-*` staging directory:
  `syscall.Exec`/`os.Exit` relaunch skips deferred cleanup, so the dir is
  removed before relaunching. On hosts with a small `/tmp` (tmpfs) the leak
  filled the filesystem until the update download itself ran out of space.

## [0.8.0] - 2026-11-10

### Added
- First-class failure classification: `jobs.classification` rolls the first
  failed step's diagnosis kind up to the job (prepare-phase failures that
  never reached a step get a class derived from the job error —
  `disk_full`, `network`, `timeout`, `infra`, `prepare`; lease-reaper and
  timeout watchdogs classify `infra`/`timeout`). Exposed on `/runs` items
  (run-level rollup of the first classified job), run detail, `kern runs
  list --json`, and `kern_jobs_finished_total{conclusion,classification}`.
- Env/secret audit trail: the runner writes a `KERN: env:` line into the
  first step's log at job start listing every materialized env var, secret,
  and var NAME — never a value — so missing or stale env is a 30-second
  diagnosis instead of a guess. Untrusted runs simply show no secrets.
- Maven mirror insecure-protocol opt-in: the seeded Gradle init script now
  sets `allowInsecureProtocol` when `KERN_MAVEN_MIRROR` is plain http, so a
  LAN pull-through cache no longer fails plugin resolution with "Using
  insecure protocols with repositories, without explicit opt-in, is
  unsupported". https mirrors are unaffected.

### Changed
- Web dashboard mobile + touch polish: horizontally scrollable run/artifact
  tables, always-visible row actions on touch devices, tighter gutters.
- Toolchain pins moved to Go 1.27 across `go.mod`, the KERN CI container,
  `release.yml` setup-go and both build-stage Dockerfiles.
- Release web stage builds on `node:26-alpine` (was 22).
- Baseline job image rebased to `ubuntu:26.04` with the matching package set
  (`libicu78`, `libvpx12`, `libx264-165`); the published tag is now
  `ghcr.io/dvorinka/kern-ubuntu:26.04`. Fleets pinned to `:24.04` keep the
  frozen 24.04 image — upgrading is an operator `.env` change.
- Dependency bumps: actions/upload-artifact v7, actions/download-artifact
  v8, docker/login-action v4, docker/setup-buildx-action v4,
  softprops/action-gh-release v3, apps/web npm group (tailwind 4.3.5,
  react-query 5.107, lucide 1.70, vite 8.3.6, oxlint 1.92, oxfmt 0.77),
  golang.org/x/sys 0.50.0.

## [0.7.9] - 2026-11-10

### Added
- Fleet Maven Central mirror: the runner seeds
  `<work-root>/_cache/gradle/init.d/kern-mirror.init.gradle` and sets
  `GRADLE_USER_HOME` to the shared cache, so every job image resolves
  dependencies through the Google pull-through mirror first (declared
  repositories kept as fallback; `FAIL_ON_PROJECT_REPOS` projects get the
  mirror via `dependencyResolutionManagement` only). `KERN_MAVEN_MIRROR`
  overrides the URL — a LAN pull-through cache slots in with no code change.
  Side effect: Gradle dependencies/wrappers/build-cache persist across jobs.
- Transient-failure auto-retry: a failed job whose failed steps are all
  `network`/`oom` class (or whose prepare-phase error matches the same
  transient signatures) is requeued automatically, bounded by the new
  `max_transient_retries` org/project setting (default 1, 0 disables,
  `inherit` at project level). Script/test/lint failures never retry.
- Diagnosis taxonomy: Gradle daemon disappearance,
  `DaemonDisappearedException`, `OutOfMemoryError`, and `signal: killed`
  classify as `oom` with the job's `KERN_JOB_MEMORY` ceiling in the message;
  registry 429/5xx, connection resets and socket timeouts classify as
  `network` instead of `script_failure`.

- Persistent Rust + global npm caches: `CARGO_HOME` points at
  `_cache/cargo` (registry/git db survives jobs — the Tauri release legs
  re-downloaded gigabytes of crates cold every run) and `NPM_CONFIG_PREFIX`
  at a writable per-job dir so `npm i -g` inside actions (tauri-action)
  stops failing on the image's root-owned prefix.
- Per-job resource hints: trusted workflows may set `KERN_JOB_MEMORY`,
  `KERN_JOB_CPUS` and `KERN_JOB_DISK` in job env to override the runner's
  container ceilings; untrusted runs keep defaults. `KERN_JOB_DISK` (also
  runner-side, e.g. `30g`) arms a work-root sampler that aborts the job
  with a `disk_quota` diagnosis instead of letting it die on ENOSPC.
- No-output watchdog: a step silent for `KERN_STEP_SILENCE_TIMEOUT`
  (default 600s) gets a `ps`/`jstack` dump into its log; at twice the
  interval it is killed and classified `hang` — distinct from wall-clock
  `step_timeout` and diagnosable instead of opaque.

- Per-workflow disable switch: `PATCH /projects/:project/workflows/:id`
  `{disabled}` (project page gets Disable/Enable buttons) gates every run
  path — webhook deliveries report the workflow as skipped, schedules stop
  minting, dispatch and rerun refuse. Sync preserves the flag, so a disable
  survives re-syncs; useful for keeping GitHub-Actions-owned pipelines
  (e.g. `release.yml`) from double-firing on KERN.

### Fixed
- `$GITHUB_ENV` parity: values a step appends to `GITHUB_ENV` now join the
  `env` expression context, so `${{ env.X }}` in a later step's `env:` or
  `run:` resolves the exported value like GitHub does. A step's own `env:`
  map is also part of the context for its expressions and run body.
- Disk reclaim under load: `maybePruneDisk` ran nothing while jobs were
  active, so pressure only ever grew during builds. Docker prunes
  (stopped containers, dangling images, unused networks, build cache) now
  run under load — running resources count as in-use and are skipped —
  while ledger-image/workspace/cache sweeps stay idle-gated.

## [0.7.8] - 2026-11-09

### Added
- Host-runner self-update: binary-installed runners replace their own
  executable when the signed update manifest advertises a newer release
  (`KERN_RUNNER_UPDATE=1`), including Windows builds — the release matrix
  now ships `kern.exe`.
- Per-runtime Node provisioning for JavaScript actions: `node24` actions
  run on Node v24.22.2 instead of the single v20.18.1 pin; `node12`/`node16`
  keep the v20 fallback. Distributions are fetched lazily per release under
  `<work-root>/_node/<release>` with `SHASUMS256.txt` verification.
- `GET /projects/:project/workflows` returns each workflow's latest-version
  compile `diagnostics`; the secrets page surfaces a persistent `ghcr-token`
  card with remediation steps while the warning exists and `GHCR_TOKEN` is
  absent.

### Fixed
- GHCR authentication guidance end to end: workflow lint warns when a
  registry login uses the GitHub token (`ghcr-token` diagnostic), run
  diagnosis reports the `ghs_*` installation-token denial, and
  `docs/porting.md` documents the `GHCR_TOKEN` convention — GitHub App
  tokens cannot push to GHCR regardless of granted scopes.
- `kern.yml` no longer sets `jobs.<id>.priority` — a KERN-only key that
  GitHub Actions rejects during workflow validation, which produced 0s
  `jobs:[]` failures on push and tag events while PR runs passed.

### Removed
- `.github/workflows/ci.yml` — duplicate of the KERN CI lanes; KERN CI
  covers go/web/docs fleet-side.

## [0.7.7] - 2026-11-09

### Added
- `GITHUB_REF_NAME`, `GITHUB_REF_TYPE`, `GITHUB_HEAD_REF`, `GITHUB_BASE_REF`
  (PR runs), `GITHUB_RUN_ATTEMPT`, `GITHUB_REPOSITORY_OWNER`,
  `GITHUB_GRAPHQL_URL` and `GITHUB_TRIGGERING_ACTOR` now reach the step
  environment — previously they existed only in the `github.*` expression
  context, so `echo "version=${GITHUB_REF_NAME#v}" >> $GITHUB_OUTPUT`
  wrote an empty output and `steps.<id>.outputs` resolved empty (kern#247).
- Homebrew distribution: `brew install dvorinka/tap/kern` installs the CLI
  with shell completions; the release workflow bumps the tap formula on
  every tag.
- `/webhooks/deliveries` accepts `status`/`event` filters and returns
  whole-table `total`, `counts` and `events`; `/artifacts` and `/cache`
  return a `stats` aggregate; `/runs` `counts` adds `success`,
  `completed` and `median_seconds`.

### Fixed
- Dashboard pages no longer derive stats from bounded list windows:
  overview "runs today" counted only the newest 100 runs, project cards
  and project health read a global `/runs?limit=200` window, and the
  deliveries/artifacts/cache KPIs counted only the fetched page. All now
  read server-side aggregates.
- `docs` CI job runs docsync from its release binary — its module needs
  go >= 1.27.1 while the project pins 1.26 and the fleet pins
  `GOTOOLCHAIN=local`.
- `kern-ubuntu` parity image now ships Playwright's full Ubuntu 24.04
  dependency set (chromium, firefox, webkit + tools/fonts) and the
  tauri release-lane packages (`libwebkit2gtk-4.1-dev`, `libgtk-3-dev`,
  `libayatana-appindicator3-dev`, `librsvg2-dev`, `patchelf`,
  `desktop-file-utils`, `minisign`, `qemu-user`, `shellcheck`,
  `libev-dev`), so `playwright install --with-deps` and the per-job
  `sudo apt-get install` steps resolve to no-ops instead of reinstalling
  every run. Untrusted lanes could not satisfy these at all (read-only
  rootfs, no privilege escalation); the libs must live in the image.
- Per-job GitHub check runs: every job now posts its own
  `kern/<workflow> / <job>` check (queued on claim-gate, in_progress on
  claim, completed on finish), so a pull request shows per-job status
  instead of only the run-level aggregate. A terminal sweep on run
  completion lands final checks for jobs that never reported mid-run —
  fail-fast sibling cancels, run cancels, and pre-seed skips.
- `KERN_ACTIONS_PRESEED` warms the persistent `_actions` cache at runner
  startup and hourly, so a cold runner's first jobs do not fetch common
  actions mid-step. Unset seeds a built-in fleet set (setup-*, docker,
  release lanes); a comma-separated `owner/repo@ref` list replaces it;
  `off` disables.
- Audit coverage for runner registration (`runner.register`, actorless
  like `runner.enroll`), API-token deletion (`token.delete`), and the
  MCP `kern_set_runner_drain` path; `runner.drain` events now carry the
  direction (`draining=true|false`) so undrain is distinguishable.
- Global log search: `GET /logs/search?q=` greps step-log blobs
  server-side, newest runs first, bounded by candidate count, bytes
  scanned, deadline and hit cap. `kern logs --search <q>` prints hits as
  `run# job step n:line`; `--project`, `--limit`, and an optional run-id
  positional narrow the scan.
- Per-job resource metrics: the runner samples `docker stats` every 15s
  while a job container runs and reports peak CPU% and memory on
  `jobs.metrics` (migration 0029) via the finish payload. Host executors
  leave the field empty.
- Live-job exec channel + `kern ssh` (#132, #133):
  `POST /jobs/:id/exec` queues a command against an in_progress job; the
  leasing runner picks it up on heartbeat and runs it inside the job
  container (`docker exec -i`), streaming output to a session blob
  (migration 0030). `kern ssh <job-id|run-id> [--job KEY] [-c CMD]` opens a
  line-mode shell when `-c` is absent; stdin streams both directions.
  Admin role + trusted runs only — fork/pull_request jobs are refused, and
  host executors have no container to enter. PTY framing is deliberately
  out of scope; full-screen programs will not render.
- `kern testbox [workflow] [--job KEY] [--dir DIR] [--env-file F]` (#143):
  runs one workflow job locally through the real docker executor — bundle
  the working tree, execute inside an in-process shim of the runner API
  (logs to stdout, artifacts to `.kern-testbox/artifacts`, caches always
- `docs/retention.md` + `docs/airgap.md` (#142): operator pages for every
  retention env var (log/cache/webhook/audit/notify TTLs, runner image and
  cache budgets) and a sealed-network install path — mirrored ghcr images,
  `_actions`/`_node` cache preseed, `KERN_ACTIONS_GIT_BASE`, self-update off.
  miss). No control plane, no push; exit code follows the job conclusion.
