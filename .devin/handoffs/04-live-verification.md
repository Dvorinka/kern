# HANDOFF 04 — Live verification & production hardening

**Status:** queued — runs parallel to 05 (disjoint zones).
**Source:** deferred-evidence list from handoffs 01/02/03 + `docs/production.md`.
**Scope ownership:** fleet/CT908 ops, `internal/store` quota path, `docs/`, backup/restore.
Do NOT touch workflow-engine internals (`internal/workflows`, `internal/expressions`),
check-run posting (`internal/github`), or `apps/web` — agent 05 owns those.

## Mission

Everything shipped by agents 01–03 and the annotations follow-up is verified by
unit tests and local runs — none of it has been proven on the real fleet or in
production. This handoff converts "implemented" into "evidenced", and closes the
small known correctness gaps that need no product decision.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end —
   it carries the fleet SSH route, deployment layout, and every trap the last
   three agents hit.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `docs/production.md`, `docs/security-isolation.md`, `docs/autoscaling.md`,
   `docs/backups.md`, `docs/compatibility.md`.
3. **Branch off `origin/master`** in a worktree:
   `git worktree add ~/.t3/worktrees/KERN/live-verify -b feat/live-verify origin/master`.
   Master tip is `84fc9e4` (annotations merged). Migration counter is at **0016** —
   if you need a migration take **0017** and note it in your PR; agent 05 may also
   need one, so check `migrations/` on master again before naming yours.
4. Production: CT 908 on Proxmox/Nab9 — `ssh -i ~/.ssh/proxmox_devin root@100.77.51.73`,
   source `/opt/kern/src`, env `/opt/kern/.env`, deploy via
   `docker compose -f deployments/docker-compose.yml --env-file /opt/kern/.env up -d --build`.
   Metrics endpoint is auth-gated (Bearer read token — see memory/env).

## Rules (non-negotiable)

- J.A.R.V.I.S. voice: polite, dry, brief. No emojis.
- Ponytail: smallest correct diff, stdlib before deps, no speculative scaffolding.
- Evidence over claims: every "verified" item needs captured output (command +
  result) in the PR description or `docs/`. A feature is not done because a unit
  test passed.
- Never fake/silence behaviour — unsupported features emit diagnostics.
- Pre-push gate: `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- PR-first; never push to main. Trailer `Authored By: TDvorak <info@tdvorak.dev>`.
- No GLM subagents.
- Memory loop: append durable facts to `~/agent-memory/projects/kern.md`, commit, push.

## Work queue (ordered)

### 1. Live-verify fork-tier egress (PR #81)

The allowlist proxy exists and is unit-tested; it has never run against a real
fork pull_request job. On dev or prod: trigger a `pull_request` run from a fork
(or simulate the `fork` trust tier on an existing repo — check
`internal/workflows` trust classification), have a step attempt
`curl https://example.com` (must fail) and `curl https://github.com` (must pass).
Capture container network inspection (`docker network inspect` showing
`--internal`) and proxy logs. If same-repo `pull_request` runs are still
sandboxed when `pr_trust` would promote them, that is expected — verify the
documented behaviour, not the ideal one. Record results in
`docs/security-isolation.md` under a "live verification" note with date + evidence.

### 2. Live-verify ephemeral runner lifecycle (PR #81)

On one fleet runner host: set `KERN_RUNNER_EPHEMERAL=1` +
`KERN_RUNNER_EPHEMERAL_IDLE=60s`, let it register, confirm heartbeat-driven
self-deregister when idle, confirm the row is deleted server-side, confirm a
runner with an active job does NOT deregister. Watch for the parked-claim race
the security agent fixed — verify the fix holds under a slow claim. Capture
runner log lines + the runners table state before/after.

### 3. Capture the deferred KPIs (PR #80)

- **Cache hit rate**: run a real workflow twice on the fleet; scrape
  `kern_cache_lookups_total` (or whatever the agent named it — check
  `internal/telemetry` + `/metrics` output) and compute hit rate from counter
  deltas. Workspace-relative `actions/cache` only — document that ceiling.
- **CT908 production metrics**: scheduler tick p95, claim latency, queue-wait,
  disk usage — pull `/metrics` from prod, record baseline numbers in
  `docs/production.md` or a new `docs/kpi-baseline.md`.
- **Disk-pressure GC**: if prod disk is nowhere near the threshold, demonstrate
  on dev (agent 01 already did the 100%-threshold demo — cite it) rather than
  manufacturing pressure on prod.

### 4. Artifact quota race → transactional fix

`ArtifactBytesForProject` check-then-insert is not transactional — concurrent
uploads can exceed `artifact_quota_mb` by ~one object (documented ceiling in
`docs/production.md`). Fix it properly: take the project row
`SELECT ... FOR UPDATE` inside the upload transaction (or equivalent —
serializable tx / advisory lock on project id), so quota check + insert are
atomic. Add a store test that fires two concurrent uploads near the cap and
asserts the sum never exceeds quota. This is the only code change expected to
need a store-layer test.

### 5. Restore drill (docs/backups.md)

The runbook exists; nobody has run it. Execute the restore procedure against a
scratch database + scratch blob dir (NOT prod data), verify the restored
instance serves runs, document actual-vs-documented gaps in `docs/backups.md`.
Timebox it — if the drill exposes a real gap, fix the small ones, file the rest.

### 6. Revisit `docs/autoscaling.md` with evidence

Agent 01 recorded "status quo" as the decision for lack of data. With items
1–3's real numbers, either confirm the decision with evidence or reopen the
fan-out/bin-packing question. Update the doc's decision record — a decision
backed by measurement is the deliverable, not new scaler code.

### 7. Optional, only if time remains

- Runner drain ETA: `drain` is one-way today; a remaining-jobs count on the
  runner row is a small additive change.
- Login rate limiter is in-process — under multi-replica it multiplies. Document
  the ceiling in `docs/production.md` (do not build a shared limiter unprompted).

## Traps (inherited — read memory first)

- `go test ./internal/store` TRUNCATES the dev DB — use
  `KERN_TEST_DATABASE_URL=postgres://kern:kern@localhost:5432/kern_test?sslmode=disable`
  and run `KERN_DATABASE_URL=<same> go run ./cmd/migrate` first if migrations moved.
- `GOTMPDIR=$HOME/.cache/go-tmp` for all Go builds — `/tmp` quota kills compile.
- `pkill -f` kills your own shell — use `pkill -x` or PIDs.
- Rebuild `.data/kern-*` binaries before trusting local runtime behaviour.
- Job containers are uid 1000 + read-only — work dirs need explicit 0777 chmod.
- Docker Desktop bind mounts only work under `$HOME` — work roots not under `/tmp`.
- Dynamic local Go binaries are CGO-enabled — scratch images need the
  busybox/alpine fallback the egress code already implements.
- Fleet SSH details rotate — verify against `~/agent-memory/projects/kern.md`
  before use; if the route is dead, that itself is a finding to report.

## Done means

- Items 1–3 each have captured live evidence (commands + output) recorded in
  the relevant docs — not "should work".
- Item 4 merged with a concurrency test proving the quota holds.
- Item 5 drill executed; `docs/backups.md` reflects reality.
- Item 6 decision record updated with the measured numbers.
- All gates green; memory updated.
- PR(s) merged or open-with-evidence; end with a JARVIS-style report:
  verified / fixed / deferred-and-why. Then STOP.
