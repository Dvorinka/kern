# Handoff 07 — Fleet disk hygiene, macOS runners, control-plane resilience

One agent, one session, three workstreams in order. Repo: `~/Desktop/PROG+HTML/KERN`,
remote `github.com/Dvorinka/kern`. Master tip `068af41`; open PRs #85 (quota race),
#86 (host executor foundation), #87 (verification docs) — branch off `origin/master`,
expect #86 to merge mid-work; rebase onto it before the macOS phase.

## Phase A — fleet disk hygiene (do first; fleet is degrading)

Root cause already established: deployed `kern-runner` images predate the GC code
from PR #80 — production runners run binaries with no disk-GC. Plus unbounded
`/opt/gha` `_work` trees, dual docker+containerd stores, uncapped language caches.

1. Rebuild `kern-runner` image from master, publish, redeploy to every runner host
   (CT901/902/906/908 on Nab9 + any others found registered). Verify the deployed
   binary contains GC/ephemeral strings and startup logs show the new version.
2. Extend runner-side cleanup (smallest correct diff in `internal/runner/gc.go`):
   reap stale `_work`/workspace dirs, bound `_cache`/`_toolcache`, prune dangling
   docker images + buildx cache on pressure, emit metrics/logs for bytes reclaimed
   and skipped-active resources.
3. Fleet policy: containerd image prune cadence, GHA `_work` retention (these are
   GitHub runners, not KERN — a cron/systemd-timer prune script on each CT is fine),
   language-cache caps (npm/go/gradle).
4. Evidence: `df -h` before/after per host, GC log lines, image digest per host —
   all captured in the PR. No claims without output.

## Phase B — macOS runner completion (PR #86 is the foundation)

`feat/mac-runners` worktree `~/.t3/worktrees/KERN/mac-runners` may hold uncommitted
agent-06 edits (its own gate test + label-test fix, both passing) — commit them
first if the thread is dead. Then:

1. Build `kern-runner` darwin/amd64+arm64 (`GOOS=darwin go build`), package with a
   launchd plist (KeepAlive, ephemeral self-deregister via existing
   `KERN_RUNNER_EPHEMERAL`).
2. Install on VM 904 (currently 4 vCPU/12GB, `PerfPowerServices` frozen by
   `/Library/LaunchDaemons/dev.tdvorak.perfpower-stop.plist`). Labels must advertise
   `macos`/`macos-arm64` — PR #86 fixed the label bug; verify on the live runner.
3. Nab9 scaler: ~50-line script + systemd timer — `qm start 904` when jobs
   requiring macos labels sit queued >60s; `qm shutdown 904` after 15min idle
   (guest-side launchd idle-poweroff as backup). May need a small queued-jobs
   API endpoint — check `internal/api` first.
4. Live proof: VM off → push workflow `runs-on: [self-hosted, macos]` → VM on →
   job runs (sw_vers/xcodebuild -version/::notice::) → idle → VM off. Capture
   timestamps in PR.

## Phase C — control-plane resilience (smallest scope)

Observed live: heartbeat `context deadline exceeded`, API 502s during rebuilds,
`load gate: host saturated, pausing claims`. Investigate claim/heartbeat timeouts
under API restart; ensure runners back off rather than stampede; load-gate metrics
should explain pauses. Only fix what evidence supports — no speculative tuning.

## Rules

- JARVIS voice; ponytail smallest correct diff; never weaken the host-executor
  trust gate; secrets never in logs.
- Pre-push: `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- `KERN_TEST_DATABASE_URL=postgres://kern:kern@localhost:5432/kern_test?sslmode=disable`
  for DB tests (migrate first if needed — counter at 0017, take 0018+ only if
  required, recheck migrations/ first). `GOTMPDIR=$HOME/.cache/go-tmp`.
- Never `pkill -f` (self-match kills your shell). PR-first, never push main.
  Trailer: `Authored By: TDvorak <info@tdvorak.dev>`.
- Fleet SSH: `ssh -i ~/.ssh/proxmox_devin root@100.77.51.73` (Proxmox host;
  `pct exec <ct>` into containers). Mac guest: `sshpass -p 'hackintosh'
  tdvorak-dev@10.0.0.53` (VM may be off — start via `qm start 904`).
- Append durable facts to `~/agent-memory/projects/kern.md`, commit+push per phase.

DONE = Phase A evidence per host, Phase B live off→on→workflow→off cycle,
Phase C findings fixed or documented, all gates green, memory updated.
JARVIS-style report per phase. Then STOP.
