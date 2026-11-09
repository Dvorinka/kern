# Changelog

All notable changes to KERN. Format follows [Keep a Changelog](https://keepachangelog.com/);
versions follow [SemVer](https://semver.org/) — minor bumps for features, patch for fixes.

## [Unreleased]

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
