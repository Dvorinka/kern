# Changelog

All notable changes to KERN. Format follows [Keep a Changelog](https://keepachangelog.com/);
versions follow [SemVer](https://semver.org/) — minor bumps for features, patch for fixes.

## [Unreleased]

### Added
- Critical-tier reclaim now also sweeps stale tool scratch dirs under
  the job temp root (`pytest-of-*`, `trivy-*`, `playwright-*` older than
  24h) — co-located fleets leak multi-GB residue in /tmp that docker
  prune never reaches. Operator paths (`gha-*`, `kern-job-*`) are
  excluded by pattern, not by age.
- `kern optimize [dir]` — compiles each workflow against the live
  fleet and prints ranked suggestions: unclaimable `runs-on` labels
  (job waits forever), missing `priority` on PR pipelines, serial
  test lanes that should take `strategy.split`, and services without
  `--health-*` options that race first steps.
- Dashboard: `GET /runs/day-counts` + `GET /jobs` — per-day run
  aggregates and the live queue now come from the server, so the
  runs-per-day chart and activity grid show real history (not the tail
  of a 100-run fetch) and queue panels show the true claim order.
  `?day=<iso>` filters the runs list server-side for chart drill-downs.
- Trace/queue interactivity: MiniTrace lanes and queue rows deep-link to
  `/runs/<id>?job=<key>`, landing on that job's log.
- `runner.offline` outbound alert: when the stale-runner sweep flips a
  runner offline, the control plane enqueues a notification event on the
  org's `notify_url` (slack format honoured; `notify_runner_offline=off`
  in org settings disables it). One delivery per transition, retried via
  the same queue as `run.completed` (#181).
- `docs` CI job runs `docsync check` against a committed baseline —
  new docs-vs-code drift (env vars, paths, commands, routes) fails the
  build while the 80 known findings stay suppressed (#193).
- Runner pauses job claims while the work-root filesystem is below
  `KERN_RUNNER_DISK_MIN_FREE_PCT` — the same threshold that drives the
  docker reclaim sweep, now also a claim gate so a full runner stops
  taking jobs it can only fail (#210).
- `kern runners list` shows a VERSION column per runner and prints a fleet
  version histogram when runners disagree — the drift view from #181 without
  opening the UI (#181).
- `docs/runbook.md` — operations runbook: symptom → check → fix tables for
  webhook delivery failures, stuck queues, runner offline/drift, runner disk
  pressure, Postgres maintenance, retention knobs and deploy/rollback (#192).
- `kern completion <bash|zsh|fish>` prints a shell completion script
  covering the top-level commands and subcommand trees — source it from
  your shell rc to get tab completion (#189).
- Control plane emits a `runner.disk_pressure` audit event when a
  heartbeat's free-space sample crosses below the org's
  `disk_alert_pct` setting (default 10, matching the runner's own claim
  gate) and `runner.disk_recovered` once free space climbs back above
  it (+5 points of headroom so a runner hovering at the line doesn't
  flap). Latched per runner — one event per episode, not per heartbeat
  (#212).
- `kern runs rerun <id> --failed` re-runs a finished run's failed and
  cancelled jobs in place (plus their dependents), and `--job KEY`
  re-runs a single job and its dependents — matching `gh run rerun`
  instead of seeding a whole new run (#179).
- Job containers now provision the pinned Node runtime at job setup, so
  `node`/`npm`/`npx` work in plain `run:` steps even when no JS action
  runs first — the baseline toolset GitHub's ubuntu image ships on PATH
  (curl/jq/git already live in the `kern-ubuntu` parity image). The
  provision is once per runner host and best-effort (#220).
- `services.<name>.ports` host:container remaps are now honoured instead
  of warned away — on bridge jobs a loopback forwarder container binds
  the declared host port inside the job's netns; on trusted host-net
  jobs the real `127.0.0.1:host:container` publish applies (#219).
- `docs/runner-contract.md` — the job/service networking model, reserved
  kern ports, health-gate semantics, and job-container hardening in one
  place (#223).
- Critical-tier disk reclaim: below `KERN_RUNNER_DISK_CRITICAL_FREE_PCT`
  (default 5) or `KERN_RUNNER_DISK_CRITICAL_FLOOR_BYTES` (default 2 GiB),
  whichever trips first, the idle runner also removes ledger-tracked
  images unused >24h, sweeps workspaces older than 2h, and evicts
  `_cache` down to half its cap — before reaching for the docker prune
  the warn tier already runs (#211).

### Fixed
- Runner: the `_cache` size bound now evicts whole atomic entries —
  extracted `<mod>@<ver>` trees, `_toolcache` installs (marker included),
  `_actions` checkouts, playwright browsers — instead of loose files.
  Per-file eviction corrupted atomic trees: go stamps module files with
  uniformly old mtimes, so the sweep shredded `gomod` first and builds
  failed on missing `.go`/`.s` files under intact-looking dirs (#211).
- Prepare/checkout failures that die before any step (ENOSPC, image
  pull) now get a classified job error with the runner's live free
  space instead of a raw `no space left on device` string, and step
  errors are scanned for the same signatures during diagnosis (#209).
- Control-plane postgres no longer publishes on host `:5432` — the api
  reaches it over the compose network anyway, and a host-published
  `:5432` collides with job services on hosts where jobs share the host
  netns. Operator psql stays available on `127.0.0.1:55432` (#221).

## [v0.6.3] - 2026-11-09

### Fixed
- Runner: privileged jobs now mount the workspace at a per-job path
  (`/github/workspaces/<job-id>`) with a matching host symlink. The
  single shared `/github/workspace` link raced whenever
  `KERN_RUNNER_CONCURRENCY > 1` — a sibling job rewrote it mid-run and
  `docker compose ./` binds resolved to the wrong or wiped workspace
  (primora e2e-smoke mount failure). Stale links are removed in cleanup
  and swept by the 15-min GC loop (#164).
- Runner: `github.ref_name` and `github.ref_type` are now populated in
  the expression context — `${{ github.ref_name }}` previously
  interpolated empty, producing invalid image tags like
  `ghcr.io/dvorinka/kern-api:` and failing the release images job
  (#164).
- Web UI on phones: the 11-item nav no longer crushes into a 20px
  scroll sliver — it collapses into a hamburger menu (badges included)
  with a scrim, search becomes an icon that still opens ⌘K, and the
  version chip hides under 560px so the header fits. Login page brand
  now contrasts with the hero panel in both themes (was `--ink` on
  `--ink`: invisible tile and wordmark), and the ⌘K palette no longer
  overflows narrow viewports.

### Added
- Project configuration variables — the GitHub `vars` expression context.
  `project_vars` store with plaintext values (configuration, not
  credentials), `GET/PUT/DELETE /api/v1/vars?project=<slug>`, vars resolve
  in step `env`/`if`/`run`/`with`, job `if:` conditions and `runs-on`
  seed-time resolution, delivered to every run including untrusted
  `pull_request`/`fork` like GitHub. CLI `kern vars list|set|delete` and a
  Variables card on the secrets page (#160).
- Run routes accept short run-id prefixes: `kern runs show 99540d68`,
  `/runs/<prefix>`, logs and diagnose all resolve unambiguous prefixes
  server-side (#161).
- Command palette (⌘K): results carry unique keys — every runner entry
  shared the `/runners` destination key, producing React duplicate-key
  warnings whenever the palette listed multiple runners (#165).

## [v0.6.2] - 2026-11-09

### Added
- `kern runs wait <id> [--interval] [--timeout]` blocks until a run
  reaches a terminal state and prints failed jobs — the monitoring
  primitive for scripts and agents gating on CI (#159).

### Fixed
- Runner self-healing: crashed or restarted runners no longer leak
  `kern-svc-*`/`kern-job-*` containers and `kern-svc-*`/`kern-ego-*`/
  `kern-egro-*` networks. The disk GC loop (startup + every 15 min) now
  sweeps KERN-owned resources whose job is not active on that runner —
  leaked postgres containers were squatting host ports (`0.0.0.0:5432`
  bind conflicts) and serving stale databases to later jobs (#159).
- Idle dangling-image prune on every GC sweep: layer creep no longer
  accumulates between disk-pressure events (ci-runner-902 hit 2 GB free)
  (#159).

## [v0.6.1] - 2026-11-08

### Fixed
- Docker action steps (`uses: docker://`, `runs.using: docker`) now receive
  multi-line env values (service-account JSON, PEM keys, heredoc exports)
  via `-e` args instead of silently dropping them (#146, #147).
- Env layering precedence: a later layer resolving a key to a file-safe
  value no longer loses to a stale `-e` entry from an earlier layer (#147).

## [v0.6.0] - 2026-11-08

### Added
- CLI parity: `kern doctor` (health/readiness + authenticated probes),
  `kern diagnose` (pasteable run/runner/failed-log bundle), `kern migrate`
  (import GitHub workflows into a project), `kern logs` alias with
  `-f`/`--follow` (#68, #128).
- `kern hooks install` generates a git pre-push hook that runs
  `kern lint` offline; `--dry-run`/`--force`, refuses overwrite (#70, #131).
- MCP server coverage: run lifecycle (trigger/rerun/cancel/approve),
  project detail, offset-paged step logs, runner list + drain, secret
  list — scope and role gates mirror the HTTP routes (#69, #129).
- workflow_dispatch inputs, release + repository_dispatch + workflow_run
  events, tags/paths push filters, case-insensitive repo matching,
  workflow_call reusable workflows, concurrency groups (#107-#122 range).
- Windows + macOS host runners live; docker-in-job routing via
  kern-docker host executors; fleet wake-on-demand controls, runner
  self-update opt-in, enroll one-liner (#105-#125 range).

### Fixed
- Boolean CLI flags no longer demand a value (`parseFlagsEx`).
- Runner self-update no longer inherits the heartbeat's request context
  (v0.5.1); signed-manifest fetch falls back to the API release-asset
  endpoint on CDN 504s.
- GitHub App: installation permission cache expires after 15m;
  manifest requests `contents:write`.
- Deploy: fail loudly when KERN_DEFAULT_IMAGE is unset; updater binds
  /opt/kern dir, not the .env file.


## [v0.5.1] - 2026-11-08

### Fixed
- Update checks no longer inherit the caller's request context: a runner
  heartbeat's ~5s client budget could cancel the release fetch mid-flight
  and cache `context canceled` for a full hour, permanently starving the
  auto-update plan. Failed checks now also cache for 5 minutes instead
  of an hour.
- Signed-manifest download falls back to the `api.github.com` release-asset
  endpoint when the browser download URL fails — the release CDN serves
  cached 504s in bursts (same fallback the runner image already uses).

## [v0.5.0] - 2026-11-08

### Added
- Runner icons: emoji, text, image URL/path per runner, editable by admins,
  rendered across runner lists and run views (#93).
- Project icon auto-discovery: `GET /projects/{project}/icons` ranks likely
  image candidates from repo source (local checkout or GitHub contents API)
  and `GET /projects/{project}/icon` serves the chosen file (#93).
- Per-user UI theme persisted server-side: `GET /auth/me` returns `prefs`,
  `PUT /auth/me/prefs` stores up to 8 KiB of JSON, applied after login with
  localStorage kept as first-paint fallback (#97).
- Interactive runner geo editor: click-to-pin world map (same projection as
  the fleet map, wheel zoom + drag pan), debounced Nominatim reverse
  geocoding for city/country, manual coordinates, and egress-IP Auto
  resolution via `PATCH /runners/{id}/geo` (#98).
- Stale-runner cleanup: runners offline for over 7 days surface in a
  dismissible banner with one-click remove (#98).
- Public run page (`/ci/{id}`) stats strip: live-ticking elapsed time, job
  tallies, project slug (#99).
- Provider-agnostic VM scaler (`deployments/vm-scaler.sh`): one env-file
  per scalable group, label-overlap queue detection, idle shutdown,
  stop→kill escalation, optional warm cron — works with Proxmox, libvirt,
  cloud CLIs or ssh, not just macOS (#100).

### Fixed
- Content Security Policy: theme bootstrap moved out of inline script,
  `form-action` allows the GitHub App manifest flow, Cloudflare Insights
  beacon handled explicitly, Nominatim reverse geocoding allowed — GitHub
  connect, repo icons and the geo picker unblocked (#92, #98).
- Heartbeat `context canceled` client-disconnect noise no longer logged as
  request failures (#91).

### Changed
- Fleet map: real Natural Earth land geometry under the dot matrix, zoom to
  country scale with cursor-centered zoom and clamped pan (#94).
- Dashboard honesty pass: queued/waiting states render gray and dashed,
  running/failure/success carry distinct colors across gantt, fingerprint
  and timeline; runs list rows show a hover preview card with quick
  rerun/cancel actions; projects grid is responsive with bounded metrics;
  runs-per-day tooltip adds totals, failures, cancellations and per-project
  breakdown; activity grid gains month labels and a cancelled state;
  time-by-project donut is interactive (#95).
- Brand mark unified: header `FanMark`, `logo.svg` and `favicon.svg` now
  share the login-page hub-fan artwork (#101).
- README refreshed to v0.4.2 status with dashboard screenshots; CHANGELOG
  backfilled through v0.4.2 (#96).

## [v0.4.2] - 2026-11-08

### Added
- Security isolation: verified egress allowlist for job containers, JIT
  single-job runner registration, and scoped `GITHUB_TOKEN` per job (#81).
- Org governance surface: observability endpoints, audit and member
  management polish, project `pr_trust` setting that promotes same-repo PR
  runs to trusted (#79, #82).
- Workflow-command annotations (`::error::`, `::notice::`, …) surfaced on
  steps in the UI (#83).
- GitHub Actions compatibility tail: remaining expression/context and
  workflow features (#84).
- Host executor mode for runners plus honest platform labels — jobs can run
  natively on the host instead of inside Docker (#86).
- Web console port and project icons with dashboard interactivity (#78).

### Fixed
- Atomic artifact quota enforcement — concurrent uploads can no longer race
  past the limit (#85).
- Scheduler cost, runner disk hygiene, action cache and metrics-series
  improvements (#80).
- Bounded persistent runner cache with LRU eviction
  (`KERN_RUNNER_CACHE_MAX_BYTES`), prune deferral with `reclaimed_bytes`
  logging, and `deployments/ci-cleanup.sh` for host-side Docker hygiene
  including orphaned buildx state volumes (#88).
- macOS VM autoscaler no longer shuts down a manually started VM on a stale
  idle stamp (#90).
- Client-disconnect contexts (runner heartbeat timeouts) no longer logged as
  request failures (#91).

### Verified
- Live egress, ephemeral-runner and restore-drill evidence documented
  (#87); native macOS runner lifecycle proven end to end on a Proxmox VM —
  scaler starts the VM on queued `runs-on: macos` work, launchd runner
  executes the job, VM powers off after idle.

## [v0.4.1] - 2026-11-07

### Fixed
- Host-published service ports are serialized per runner — concurrent jobs
  can no longer collide on the same host port (#76).

### Changed
- CI lanes moved to self-hosted runners; GitHub-hosted runners are no longer
  used (#75).

### Performance
- Playwright browsers are cached across jobs; fake run axis removed from
  charts (#77).

## [v0.4.0] - 2026-11-07

### Added
- Interactive dashboard: clickable overview KPIs and panels, pan/zoom fleet map with
  live job arcs and runner hover details, runs-per-day chart with per-project breakdown,
  project filters and color coding in the runs list.
- GitHub App management: org-scoped app registration with unique manifest names,
  status endpoint, disconnect, and a Settings card; environment app shown alongside
  registered apps.
- Cross-page drill-downs: project links inside run rows and artifact rows, filtered
  run lists from projects/environments/usage charts, clickable gantt lanes that open
  a job's steps, scope-aware secret management with rotation, expandable webhook
  payloads with copy, and linkified audit-trail targets.

### Fixed
- Runner `active_jobs` reporting: column mismatch made every runner report zero live jobs.
- Orphaned queued/waiting jobs under finished runs are now cancelled by the scheduler;
  remediation approvals on terminal runs are refused and stale proposals withdrawn.
- Queue-duration metrics no longer report negative values.
- Self-heal no longer proposes package installs for daemon/socket/service commands
  that cannot be fixed that way; `self_heal=auto` retries allow-listed fixes once.
- Runner: writable action directory for trusted jobs; job-spawned containers reaped.
- Updater: no longer idles on a stale binary when the checkout already matches the
  release SHA.
