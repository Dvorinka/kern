# HANDOFF 06 — macOS runners: executor modes, on-demand Hackintosh, Mac CI

**Status:** queued — disjoint from agent 04 (live-verify, ops/quota) and merged
agent 05 (compat tail, `068af41`). This is the biggest remaining platform
feature: KERN executing Mac/iOS workflows on real macOS hardware.
**Source:** user request 2026-11-08 — Hackintosh VM 904 `macos-dev` on Proxmox
Nab9 (`10.0.0.53`, x86_64 Sequoia, 4 vCPU / 12 GB after this session's reboot),
wanted for Xcode/iOS CI, powered on only when needed.
**Scope ownership:** `internal/runner` executor + agent labels,
`internal/store` runners schema + claim query, `internal/api` (small queue
endpoint if needed), `deploy/macos/` + `deploy/` scripts, `docs/`. Do NOT touch
workflow compilation semantics, `apps/web`, or quota code (agent 04).

## Mission

Three deliverables, in order:

1. **Executor modes** — the runner stops being docker-only. `host` mode runs
   steps as local shell processes on the runner host (bare metal). This is the
   Railway-inspired generalization: execution substrate is a runner property,
   not a workflow assumption. Docker stays the default and the only mode for
   untrusted work.
2. **macOS runner packaging** — `kern-runner` on darwin/amd64 (and arm64 for
   Apple-silicon Macs/rentals), launchd-managed, ephemeral, labelled for
   `runs-on: macos` workflows, self-shutting-down when idle.
3. **On-demand power** — a scaler on Nab9 starts VM 904 when macOS work
   queues and stops it after idle. The VM already has `onboot: 0`.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `docs/architecture.md`, `docs/security-isolation.md`,
   `docs/compatibility.md`, `docs/autoscaling.md`, `docs/production.md`.
3. **Branch off `origin/master`** in a worktree:
   `git worktree add ~/.t3/worktrees/KERN/mac-runners -b feat/mac-runners origin/master`.
   Master tip `068af41`. Migration counter **0016** — you will likely need
   **0017** for `runners.executor`; RE-CHECK `migrations/` on master first —
   agent 04's quota fix may have taken it (then you take 0018).
4. Fleet access: `ssh -i ~/.ssh/proxmox_devin root@100.77.51.73` (Nab9).
   macOS guest: `sshpass -p 'hackintosh' ssh tdvorak-dev@10.0.0.53`
   (SSH enabled, password sudo). Proxmox VM control: `qm start|shutdown|stop 904`.

## Design decisions (already made — do not relitigate)

### Executor model

- `KERN_RUNNER_EXECUTOR=docker|host`, default `docker` on linux, `host` on
  darwin. Explicit env wins over GOOS default so a Linux host-exec runner is
  possible for testing (still trusted-only — see gate below).
- Host executor reuses everything except the container boundary: `renderScript`,
  env-file mechanism (adapt to process env or a sourced env file — pick the
  smaller correct diff), `logPump`, file-command harvest, secret masking,
  diagnosis, step lifecycle API calls. Steps run `shell -c script` with
  `workdir = <workRoot>/host-<jobID>` (or reuse the workspace layout minus the
  container mount).
- On host mode these become **explicit diagnostics**, never silent:
  `container:`/`services:`/`runs.image:`/`uses: docker://`/service credentials.
  `uses:` JS/composite actions: composite works (its run steps are shell);
  JS actions need the nodejs downloader extended to darwin — see stretch.
- Runner labels fix (REAL BUG, do first): `agent.go` `labels()` hardcodes
  `"linux-"+GOARCH` and appends `linux-x64|arm64`, `ubuntu-*`, `docker` —
  a darwin runner would claim Linux workflows. Make labels GOOS-aware:
  `darwin-amd64`/`darwin-arm64`/`macos` on darwin; `docker` label only when the
  active executor is docker AND a daemon answers. Add a unit test asserting a
  darwin agent never emits `linux-*`/`ubuntu-*`/`docker` labels in host mode.

### Trust gate (security boundary — non-negotiable)

Host execution has NO isolation: the job runs as the runner's user with full
host access. Fork/untrusted jobs must never land there.

- Add `runners.executor text NOT NULL DEFAULT 'docker'` (migration).
- Runner registration accepts `executor` (validated `docker|host`); the agent
  reports its mode; `ClaimJob` gains a condition equivalent to:
  host-exec runners are ineligible for jobs whose trust tier is fork/sandboxed
  (check how trust is stored on jobs — reuse that field, do not invent a second
  trust model). Trusted/protected jobs may use either executor.
- Store test: seed a fork-tier job + host runner → assert no claim; trusted
  job + host runner → assert claim. This is the test that proves the boundary.
- `docs/security-isolation.md` gains a row: host-exec = trusted-only, same
  tier rationale as `runs.image: Dockerfile`.

### macOS runner

- Build: `GOOS=darwin GOARCH=amd64 go build ./cmd/kern-runner` (arm64 too —
  same code path, needed for Apple-silicon rentals later). Verify it is
  CGO-free or document the build tags.
- Service: `/Library/LaunchDaemons/ai.kern.runner.plist`, `UserName` = the
  build user (`tdvorak-dev`), `KeepAlive=true`, env: `KERN_API_URL`,
  `KERN_RUNNER_TOKEN` (or join flow), `KERN_RUNNER_EXECUTOR=host`,
  `KERN_RUNNER_EPHEMERAL=1`, `KERN_RUNNER_EPHEMERAL_IDLE=120`,
  `KERN_RUNNER_CONCURRENCY=1` (Xcode builds are heavy; raise later),
  labels `macos,xcode,darwin,darwin-amd64`.
- Work root: `$HOME/kern-work` style path under the user — NOT /tmp
  (bind-mount rule is docker-only, but keep work dirs persistent for cache).
- Idle poweroff (backup to the scaler): a second LaunchDaemon polling every
  5 min — if `kern-runner` is not running AND no active job for N min →
  `/sbin/shutdown -h now` (ACPI → Proxmox stops the VM). Keep it dead simple.
- First real workflow to prove it: a `runs-on: [self-hosted, macos]` job that
  runs `sw_vers`, `xcodebuild -version`, `xcode-select -p` and echoes
  `::notice::` (dogfoods the annotations path end-to-end on darwin).

### On-demand scaler (Nab9)

- `/usr/local/sbin/kern-mac-scaler.sh` + systemd timer (60s) or cron —
  whichever the host already uses for similar jobs (check first).
- Needs KERN API access: check what exists for "queued jobs by required
  labels". `GET /api/v1/runs` + per-run jobs may suffice; if not, add a
  minimal `GET /api/v1/jobs?status=queued` (add to openapi.yaml + regen
  api.gen.ts if the spec covers it — check whether jobs routes are in spec).
  Auth: a read-scoped API token for the scaler — document minting it.
- Logic (keep it ~50 lines of bash):
  - VM stopped AND pending jobs requiring macos-only labels → `qm start 904`
  - VM running AND zero pending macos jobs AND runner offline/idle > 15 min
    → `qm shutdown 904` (ACPI; falls back `qm stop` after timeout if needed —
    prefer graceful always)
- Label matching must be precise: only jobs whose required labels include
  `macos` (or the custom label set) trigger power-on — a queued Linux job
  must NOT wake the Mac. Encode the trigger label as a scaler env var.
- Boot latency ~2 min (macOS POST + launchd + runner enroll) — document;
  optional `KERN_MAC_WARM_CRON` pre-warm schedule if the user wants it.
- This script IS the automated form of the `docs/autoscaling.md` status-quo
  decision for this one VM — add a line there pointing at it.

### Stretch (only if core lands cleanly)

- `nodejs.go` darwin targets: the downloader already fetches pinned node +
  SHASUMS — add `darwin-amd64`/`darwin-arm64` tarballs so JS actions run on
  macOS runners. Skip silently? NO — keep the existing non-linux diagnostic
  until implemented.
- `xcodebuild`-aware caching: derived-data cache key under `_cache` — only
  if the first real workflow shows it matters. Do not pre-build.

### Explicitly out of scope

- macOS runner pools / multiple Macs — one VM proves the model; the scaler
  pattern repeats per-VM later.
- KERN-native capacity-provider API (webhook/queue-driven `qm` calls from the
  control plane) — the host-side script covers it; revisit if VMs multiply.
- Rented Macs (Scaleway/MacStadium/EC2) — the same runner binary + labels
  work there; documented in `docs/` as the scale-out path, no code needed.
- GitHub-hosted macOS runners — cannot be driven by KERN (they are GitHub's
  ephemeral fleet, not registerable agents). Do not design around them.

## Ops already done this session (context, not work)

- `PerfPowerServices` runaway killed (SIGSTOP) + `launchctl disable` persisted.
- VM 904 cores 8→4 applied; graceful reboot verified. ISOs `sata0`/`sata2`
  still attached — leave them (boot order safety); detach later via GUI if
  desired. `cpuunits` deliberately unset.

## Traps (inherited)

- `go test ./internal/store` TRUNCATES dev DB — use
  `KERN_TEST_DATABASE_URL=postgres://kern:kern@localhost:5432/kern_test?sslmode=disable`
  and migrate first if you add 0017.
- `GOTMPDIR=$HOME/.cache/go-tmp` for Go builds.
- `pkill -f` kills your own shell — `pkill -x`/PIDs.
- macOS guest: SIP blocks `launchctl bootout` on platform daemons but NOT
  `kill` signals or `launchctl disable` (writes disabled-services DB).
- sshpass is installed locally for guest SSH; password sudo works.
- `qm guest exec` needs qemu-guest-agent which macOS lacks — use SSH.
- Rebuild `.data/kern-*` before trusting local runtime.

## Done means

- PR-A merged: host executor + trust gate + label fix, all with tests
  (claim-exclusion test is mandatory).
- PR-B/C merged: runner binary runs under launchd on VM 904; a
  `runs-on: macos` workflow completes end-to-end with captured run URL.
- PR-D merged: scaler script live on Nab9 — demonstrate VM off → queue
  macos job → VM starts → job runs → VM stops after idle (capture evidence).
- Fork-tier job proven NOT claimable by the host runner (test + ideally live).
- `docs/security-isolation.md`, `docs/architecture.md`,
  `docs/compatibility.md` (macos row), `docs/autoscaling.md`,
  `docs/production.md` updated to match reality.
- openapi.yaml/api.gen.ts regenerated if a route was added.
- All gates green; memory updated with the darwin-executor design decisions
  (future agents will need them).
- JARVIS-style report: shipped / deferred-and-why. Then STOP.
