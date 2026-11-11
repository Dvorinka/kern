# Changelog

All notable changes to KERN. Format follows [Keep a Changelog](https://keepachangelog.com/);
versions follow [SemVer](https://semver.org/) — minor bumps for features, patch for fixes.

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
