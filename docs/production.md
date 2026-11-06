# Production & enterprise readiness

Honest gap ledger, ordered by leverage. "Done" means verified; everything
below is *remaining* work. For what already works see `docs/compatibility.md`
and `plan.md`.

## Security and access control

These are the difference between "self-hosted for a team that trusts itself"
and "multi-user production". Ordered first because two of them are small
diffs with a large security delta.

| Gap | State | What it takes |
| --- | --- | --- |
| ~~API token scopes not enforced~~ | **DONE** — `requireScope` gates routes: `read` for member GETs, `write` for run/project operations, `admin` for secrets/runners/settings. Sessions are scope-free; MCP tools mirror the HTTP gates (`kern_cancel_run` → write, `kern_approve_run` → admin) | — |
| ~~Org roles not enforced~~ | **DONE** — `requireRole("admin")` on the admin route group: project create, GitHub App flow, fork-gate approve, enroll tokens, runner drain/geo/delete, environments, org+project settings, secrets write, audit | Remaining: nothing enforcement-side; member UX gets 403s, not hidden controls |
| ~~No user management~~ | **DONE** — `GET/POST/PATCH/DELETE /members` (admin group): create with role, change role, remove. Removal revokes sessions + API tokens; the last owner can neither be demoted nor removed; only an owner grants owner. Settings → Members card | Remaining: no invite-by-email flow (no mailer exists — admin sets the initial password and hands it over out-of-band) |
| ~~`/api/v1/metrics` unauthenticated~~ | **DONE** — moved inside `requireAuth`; scrape with `Authorization: Bearer <token>` (read scope suffices) | — |
| ~~No security headers~~ | **DONE** — CSP (`default-src 'self'` + Google Fonts allowances), `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` on every response | — |
| Login rate limit is in-process | `loginLimiter` lives in api memory — correct for one replica, silently weakens per-replica in a multi-replica future | Move to Postgres or the shared cache when multi-instance lands; not before |
| No session management UI | One cookie, no "active sessions" list, no revocation | Sessions table exists — list + delete endpoints and a settings card |
| No native TLS | The api speaks HTTP; TLS terminates at the tunnel/proxy | Acceptable while ingress is the tunnel. Optional `KERN_TLS_CERT`/`KERN_TLS_KEY` is a small `ListenAndServeTLS` switch if an operator ever needs it |

## Operations and reliability

| Gap | State | What it takes |
| --- | --- | --- |
| Backup story is one line | `docs/deploy.md` shows a manual `pg_dump`. No schedule, no restore drill, no blob-dir backup note | A `backups.md` runbook: cron'd `pg_dump` + `KERN_BLOB_DIR` sync, retention, and a written restore procedure that has actually been executed once |
| Blob store is filesystem-only | `blobs.Store` interface exists; only the filesystem driver does | S3/MinIO driver behind the same interface. Unblocks off-host state, DR, and multi-replica reads |
| Single control-plane replica | Scheduler, orchestrator, cron and sweeps run in-process. `SKIP LOCKED` claiming is already multi-instance safe, but ticks/sweeps would double-run | Either document "exactly one api replica" (current truth) or add a Postgres advisory-lock leader gate around `orchestrator.Tick` and the sweeps — a few dozen lines, not an election system |
| Notifications are pull-only | `/notifications` is a derived in-app feed; nothing leaves the box | Outbound webhook target (URL + event filter + HMAC sign — the GitHub-inbound code already proves the shape) is the 80% case; email can wait |
| No quotas | Artifacts expire at 90d, cache ages out, but there is no per-project storage cap, run-rate limit, or concurrency quota | Start with per-project artifact/blob bytes accounting (the store already knows sizes) + a reject threshold |
| Runner drain is one-way | `drain` stops new claims; nothing cordons the api's queue or reports drain progress | Minor: a "draining" runner badge exists; an in-flight-job ETA would be nice, not required |

## Enterprise surface

| Gap | State | What it takes |
| --- | --- | --- |
| No SSO/OIDC | Local argon2id only (ADR-002). The biggest real enterprise blocker | OIDC login flow against a generic provider (Dex/Keycloak/Authentik), group→role mapping, local accounts kept as break-glass |
| Single-org effectively | `orgs`/`org_members` exist; no org switching, creation, or cross-org admin in UI or API | Multi-org UI is mostly frontend; untested multi-tenancy means scoping bugs are likely — audit `org_id` filters when it matters |
| No OpenAPI contract | `plan.md` §2 promises OpenAPI 3.1 → generated TS client. The spec was never written; `apps/web/src/lib/api.ts` is hand-maintained | Writing the spec is documentation work, not plumbing; it also forces an error-shape audit. The generated client can replace the hand one incrementally |
| Audit log has no export or retention knob | `audit_events` + `GET /audit` exist; nothing ages out or leaves the box | Retention sweep like the other tables, and a JSONL export endpoint for compliance pulls |
| ~~MCP endpoint in flight~~ | **DONE** — merged as PR #36; scope/role gates applied in the same PR as HTTP enforcement | — |

## Compatibility tail (workflow surface)

Carried over from `docs/compatibility.md` — none of these block production,
they bound which workflows run unmodified.

- Reusable-workflow callers: `strategy`/`concurrency`/`secrets:` maps →
  explicit diagnostics today.
- Service containers: `credentials`/`ports`/`volumes` warn-and-ignore; no
  service log streaming.
- `permissions:` recorded, not enforced.
- `::error::`/`::warning::` annotations pass through logs only — not
  surfaced in the UI or check runs.
- Job `container:` options beyond `image` ignored.
- OIDC `id-token`, expression arithmetic, object filters `a.*`, some
  string functions — diagnostics, not silence.
- `actions/cache`: workspace-relative paths only, no size quotas.

## Milestones not started

| Milestone | Scope |
| --- | --- |
| M6 | Distributed cache backend, BuildKit builds, warm runner pools, cache-aware placement |
| M7 | Registry, releases, packages, deployment tracking, rollback |
| M8 | Backup/restore automation, security audit, upgrade runbooks (this document is the first artefact) |

## Suggested order

1. ~~Token-scope enforcement + role middleware + `/metrics` auth + security
   headers~~ — **DONE**.
2. ~~User management (create/invite/list/role)~~ — **DONE**. The instance is
   multi-user in fact; a member invite email flow can wait for an IdP.
3. Backup runbook + S3 blob driver — makes the instance recoverable.
4. OpenAPI spec — contract for everything that integrates next.
5. OIDC — the real enterprise gate; schedule it against an actual IdP,
   not in the abstract.
