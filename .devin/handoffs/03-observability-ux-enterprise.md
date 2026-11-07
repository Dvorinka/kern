# HANDOFF 03 — Observability, UX & enterprise surface

**Status:** queued. Start after 01's metrics baseline lands (it feeds your dashboards) —
or take the non-overlapping items first (quotas, leader gate, OpenAPI, sessions, audit).
**Source:** distilled from the "KERN CI/CD Overview" report (2026-11-07) + `docs/production.md`.
**Scope ownership:** api handlers/metrics (`internal/api`, `internal/telemetry`),
web dashboard (`apps/web` — COORDINATE: Plot Soft port uncommitted on
`feat/console-interactivity`), notifications, quotas, leader election, OpenAPI,
session/audit UX, SSO scoping. Do NOT touch runner executor internals (01/02).

## Mission

Make the platform legible and governable as it grows: real metrics + dashboards,
delivery notifications, quotas, multi-replica safety, an API contract, and the
enterprise gaps in `docs/production.md`.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `project.md`, `plan.md` (§2 stack table — OpenAPI is a promised deviation),
   `docs/architecture.md`, `docs/production.md` (your gap ledger),
   `docs/compatibility.md`.
3. Branch off `origin/master` in a worktree. **UI warning:** the Plot Soft port
   sits uncommitted on `feat/console-interactivity` — check `git status` and ask
   the user whether it has landed before touching `apps/web` visuals. Backend,
   metrics, and API-contract work is safe regardless.
4. Local loop: `just dev` (api :8080), `just runner`, `just test`;
   `cd apps/web && pnpm dev` (:5173). Login `admin@kern.local` / `kern-admin-password`.

## Rules (non-negotiable)

- J.A.R.V.I.S. voice: polite, dry, brief. No emojis.
- Ponytail: smallest correct diff, stdlib before deps, no speculative scaffolding.
- Never fake/silence behaviour — unsupported features emit diagnostics.
- Pre-push gate: `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- PR-first; never push to main. Commits carry trailer
  `Authored By: TDvorak <info@tdvorak.dev>`.
- No GLM subagents.
- Memory loop: append durable facts to `~/agent-memory/projects/kern.md`, commit, push.

## Verified state — already done, do not rebuild

- Prometheus `/metrics` exists, auth-gated (Bearer read scope).
- zap structured logs; OTel hooks exist per plan.md (verify what's wired before
  claiming a gap).
- Outbound notifications: org `notify_url` + `notify_secret` → POST
  `run.completed` + `X-Kern-Signature` HMAC, fire-and-forget, secret write-only.
  Settings → Notifications card exists.
- Audit log: `audit_events` table + `GET /audit`; covers approve, member changes,
  settings writes.
- Sessions table exists; single cookie auth, argon2id.
- Members CRUD with last-owner guards (PR #41); roles owner/admin/member.
- Usage page exists with GHA-cost-equivalent savings card (PR #45).
- Fleet map, runs/day chart, live logs, run DAG — all in the dashboard.
- Retention sweeps: artifacts 90d, cache 7d, logs, webhook deliveries 30d.
- `KERN_VERSION` stamping exists (was "dev" once — check `.env` on deploys).

## Scope — ordered queue

1. **Metrics completion (pairs with handoff 01 item 1 — whoever lands first
   owns the series).** You consume: queue wait, claim latency, runner
   utilization, step durations, cache hit/miss, webhook latency. Add what's
   still missing on the api side: HTTP handler latency histograms, GitHub API
   call counters/rate-limit gauges, scheduler Tick duration. LOW cardinality —
   no run ids/PR numbers/SHAs as labels. Then ship `deployments/grafana/` (or
   `docs/grafana.md` + JSON): dashboard JSON for queue depth, runner util,
   job duration percentiles, cache hit rate, webhook latency; alert rules doc
   (queue wait p95, runner offline, disk >80%).
2. **Notification delivery.** `notify_url` exists but is fire-and-forget.
   Add: retry with backoff (3 attempts), event filter (run status subset),
   and a Slack-compatible payload mode OR a documented 20-line shim — pick the
   smaller honest option. Per-project override of org `notify_url`.
3. **Quotas** (`docs/production.md` gap). Per-project artifact/blob byte cap
   (store already knows sizes — account on write, reject over cap with a clear
   error) + optional run-rate limit. Settings UI fields. Start with artifacts
   only; cache quota is a follow-up.
4. **Leader gate for HA.** Scheduler/orchestrator/cron/sweeps run in-process;
   `SKIP LOCKED` claims are multi-instance safe but ticks double-run.
   Postgres advisory lock around `orchestrator.Tick` + sweeps — production.md
   says "a few dozen lines, not an election system". Verify: run two api
   replicas locally, show ticks execute once.
5. **Session management UI.** List active sessions + revoke (table exists —
   needs `GET/DELETE /sessions` + settings card). Small, real security UX.
6. **Audit export + retention.** JSONL export endpoint (`GET /audit/export`,
   admin scope, date-range filter) + retention sweep knob like the other tables.
7. **OpenAPI contract** (plan.md §2 deferred item — the biggest mechanical win).
   Write `openapi.yaml` for `/api/v1` (auth, projects, runs, runners, secrets,
   artifacts, cache, environments, members, settings, audit, metrics excluded).
   Generate TS client (`openapi-typescript` + `openapi-fetch`); migrate
   `apps/web/src/lib/api.ts` incrementally — spec first, client swap per
   domain, never big-bang. The spec doubles as an error-shape audit.
8. **SSO/OIDC — scoping doc first.** The real enterprise gate. Write
   `docs/sso-oidc.md`: generic OIDC flow (Dex/Keycloak/Authentik), group→role
   mapping onto existing `org_members` roles, local admin as break-glass,
   session issuance path. Do NOT implement until the user picks an IdP —
   the doc is the deliverable.
9. **UI analytics (blocked on Plot Soft).** Once `feat/console-interactivity`
   lands: per-project build-time trends, pass-rate over time, runner health
   strip, queue-wait display on run detail, build filtering by label/branch/
   status. All read-only additions on existing endpoints — extend API first,
   then UI, matching whatever design language the merged port establishes.
10. **M7 stays ledger.** Registry/releases/packages/deployment tracking is
    `plan.md` M7 — do not start without user direction.

## Explicitly out of scope

- Runner executor, trust model, egress, ephemeral registration — handoffs 01/02.
- Scheduler algorithm changes — handoff 01.
- Slack app/bot, chatops commands — notify webhook + shim covers v1.
- Multi-org UI — audit `org_id` filters only if SSO doc proceeds.

## Traps

- `pkill -f` kills your own shell — `pkill -x` or PID kill.
- `go test ./internal/store` TRUNCATES the dev DB — `KERN_TEST_DATABASE_URL`
  gated tests use `kern_test`; never point them at dev.
- Store tests race when two packages truncate `kern_test` — PR #16 holds
  `pg_advisory_lock(8424242)`; preserve that pattern.
- Login rate limit is in-process — fine single-replica, weak multi-replica;
   your leader-gate work should note it (production.md already flags).
- `blobs.ReadFrom` caps 256KiB — `blobs.Open` for full objects.
- Unqualified column lists in JOINs → SQLSTATE 42702; prefix table aliases.
- Rebuild `.data/kern-*` before trusting local runtime.
- Playwright on this box: headless chromium GPU errors kill sessions — use
  t3 `preview_*` tools or `playwright-cli`; React login needs the
  native-setter + separate-click trick (see `design/HANDOFF.md`).
- vite+api die when spawned via `nohup ... &` — use background exec terminals.

## Done means

- Grafana dashboard JSON + alert rules exist; every panel reads a real series.
- Notification retries + filters live (evidence: killed api mid-send recovers).
- Per-project artifact quota enforced with a clear rejection (test evidence).
- Two api replicas run locally with ticks/sweeps executing once (logs as proof).
- Session list/revoke UI works end to end.
- Audit JSONL export returns correct rows for a date range.
- `openapi.yaml` covers the v1 surface; generated client compiles; at least
  one domain migrated off the hand-written client.
- `docs/sso-oidc.md` written and user-reviewed.
- `docs/production.md` rows flipped to DONE as they land; memory updated.
