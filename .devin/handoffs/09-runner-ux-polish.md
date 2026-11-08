# Handoff — Runner UX polish, CLI completeness, fleet cleanup

Repo: `~/Desktop/PROG+HTML/KERN` (Go 1.24 monolith + React 19/Vite web).
Rules: JARVIS voice, ponytail diffs, PR-first, trailer
`Authored By: TDvorak <info@tdvorak.dev>`, pre-push gate =
`GOTMPDIR=$HOME/.cache/go-tmp GOCACHE=$HOME/.cache/go-build gofmt/go build/go
test ./.../go vet` + `cd apps/web && pnpm lint && pnpm build`. Tests need
`KERN_TEST_DATABASE_URL=postgres://kern:kern@localhost:5432/kern_test?sslmode=disable`
(must contain `kern_test`; `store.Open` auto-migrates via goose). Never
`pkill -f`. Prod = `kern.tdvorak.dev` on CT908 (nab9), api token in
`~/.config/kern/config.json`. Memory file: `~/agent-memory/projects/kern.md`
(read first).

## In flight

- PR #124 (`feat/runner-auto-update`) — server-side `runners.auto_update`
  opt-in (migration 0021), `POST /runners/:id/auto-update`,
  `kern runners self-update <id> on|off`, runner-card toggle, enroll docker
  one-liner, `KERN_AUTO_UPDATE` env override. Merge when checks pass.
- ci-runner-901/903 report version `dev` — their image was locally
  compose-built; the VERSION build-arg path needs verifying
  (`deployments/docker-compose.yml` → `runner.Dockerfile` `ARG VERSION`
  → ldflags). Self-updated runners stamp correctly from release artifacts.
- Open bug: jobs can seed with **empty labels** and sit unclaimable in the
  queue forever (36 were cancelled 2026-11-08, runs on sha 7027591c).
  Seed path should reject zero-label jobs or fail the run.

## Tasks

### 1. Runner icon picker polish
`apps/web/src/routes/runners.tsx` → `IconEditor` (used via the ImageIcon pill
on each runner card). Currently a bare text input taking an emoji or image
URL/path. Make it a real picker:
- Preset grid of sensible runner glyphs (OS/hardware/status emojis —
  linux/windows/apple/docker/cloud flags) + recent/common picks.
- Live preview against the runner name row (`RunnerIcon` component).
- Keep the free-text path input for image URLs; validate lightly.
- Save/Clear/Cancel already exist — keep behavior.

### 2. `kern` CLI — install + full remote management
Goal: one CLI manages the whole control plane from any machine. Audit
`cmd/kern/*.go` for existing surface first (login, runs, runners, secrets,
settings, fleet, artifacts already exist).

- `kern install` / first-run wizard: prompt server URL + bootstrap/admin
  auth, write `~/.config/kern/config.json` (same shape `kern login` writes).
  Detect existing config, offer update.
- `kern status`: dashboard summary — api meta, runs in flight, queue depth,
  runner counts (online/draining/offline), last heartbeat. This is the
  "monitor" tool; `--watch` to refresh.
- `kern runner install`: generate + install a `kern-runner` systemd unit on
  Linux hosts (the documented Windows path is a `schtasks` KERN-Runner —
  mirror it): enroll token mint → env file → unit enable/start. Reuse the
  enroll-tokens API.
- Polish pass over all subcommands: consistent `--json`, table output,
  name→id resolution where ids are currently required (e.g.
  `runners drain` takes raw ids today — accept names), `--help` text.

### 3. Remove `kern-908` runner entry
The retired compose runner row is dead weight in the fleet view.
`DELETE /api/v1/runners/:id` exists — but first verify the
`deployments-runner` service in `deployments/docker-compose.yml` is actually
stopped on CT908; if it restarts it will re-register and the row returns.
Correct fix may be disabling that compose service, then deleting the row.
Decide based on `pct exec 908 -- docker ps`.

### 4. Wake-on-demand runner scaling — settings surface
VM904 (macos) powers on via a scaler when a `macos`-labeled job queues.
Find the scaler path (likely `internal/orchestrator` or a scheduler goroutine —
grep `macos|scaler|power`). Make it configurable:
- Per-runner flag (e.g. `runners.wake_on_demand` or org setting
  `runner_autostart`) in Settings UI + `kern settings set` + PATCH route.
- Document which runners it applies to; keep default = current behavior.

### 5. Follow-ups worth folding in
- `pnpm format` must NOT run (no oxfmt config; reformats the tree).
- When #124 merges, apply the auto-update flag to ci-runner-901/903 via the
  new endpoint (they currently rely on env opt-in).
- `docs/compatibility.md` / `docs/porting.md` — update if runner enroll or
  settings semantics change.
