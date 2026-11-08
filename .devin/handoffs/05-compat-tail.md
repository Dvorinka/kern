# HANDOFF 05 — GitHub Actions compatibility tail

**Status:** queued — runs parallel to 04 (disjoint zones).
**Source:** `docs/compatibility.md` tail + `docs/production.md` compatibility
section, minus items already shipped (permissions, annotations storage).
**Scope ownership:** `internal/workflows`, `internal/expressions`,
`internal/runner` (services/container/cache paths), `internal/github`
(check-run posting), `openapi.yaml` + generated client for any endpoint you add.
Do NOT touch `internal/store` quota code, fleet ops, or `apps/web` visuals —
agent 04 owns those. If you need a store-layer change, keep it minimal and
flag it in the PR.

## Mission

Close the compatibility gaps that make real-world workflows emit diagnostics
instead of running. Ordered by blast radius: things workflows actually hit
daily first, exotic surface last. Every item either works or keeps producing
an explicit diagnostic — never silent partial behaviour.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `project.md`, `docs/architecture.md`, `docs/compatibility.md` (the
   whole file — it is the contract you are extending), `docs/production.md`.
3. **Branch off `origin/master`** in a worktree:
   `git worktree add ~/.t3/worktrees/KERN/compat-tail -b feat/compat-tail origin/master`.
   Master tip is `84fc9e4`. Migration counter at **0016** — take **0017** only
   if truly needed and re-check `migrations/` before naming; agent 04 may also
   need one. Prefer avoiding a migration entirely.
4. Local loop: `just dev` (api :8080), `just runner`, `just test`;
   pg in docker `kern-pg-dev` :5432. Login `admin@kern.local` /
   `kern-admin-password`.

## Rules (non-negotiable)

- J.A.R.V.I.S. voice: polite, dry, brief. No emojis.
- Ponytail: smallest correct diff, stdlib before deps, no speculative scaffolding.
  Non-trivial logic leaves a runnable test.
- Never fake/silence behaviour — unsupported features emit diagnostics.
  This is the house rule that makes partial implementations acceptable.
- Pre-push gate: `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- PR-first; never push to main. Trailer `Authored By: TDvorak <info@tdvorak.dev>`.
- No GLM subagents.
- Memory loop: append durable facts to `~/agent-memory/projects/kern.md`, commit, push.
- **openapi.yaml**: any new/changed `/api/v1` route must be added to the spec;
  regenerate `apps/web/src/lib/api.gen.ts` with openapi-typescript — it MUST run
  under a TS5 scratch dir (repo has typescript@7 preview which openapi-typescript
  7.x rejects; see memory for the exact recipe).

## Work queue (ordered by blast radius)

### 1. Post annotations to GitHub check runs (finishes #83's deferral)

Steps now store `annotations` (migration 0016, masked). GitHub's check-run
annotations API accepts up to 50 per call with `path`, `start_line`,
`end_line`, `start_column`, `end_column`, `annotation_level`, `message`,
`title`, `raw_details`. Work: at check-run completion (find where KERN
creates/updates check runs — `internal/github` + wherever statuses post as
`kern-ci[bot]`), batch the run's step annotations into check-run annotation
payloads. Map `file`→`path`, `line`/`col`→`start_*`, `endLine`/`endCol`→`end_*`;
annotations without `file` cannot be posted (check-run annotations require a
path) — skip those silently? NO: they stay in KERN UI regardless; document the
path-required ceiling in `docs/compatibility.md`. Needs the check-run id —
verify KERN creates check runs vs commit statuses; if it only posts statuses,
this item becomes "create check runs for annotation support" which is bigger —
scope it honestly and descope to statuses+annotations-in-UI if creation is a
project of its own. Check `docs/compatibility.md` status-posting row first.

### 2. Expression gaps (`internal/expressions`)

`docs/compatibility.md` lists: arithmetic, object filters `a.*`, some string
functions — currently diagnostics. Implement the ones with real usage:
- Arithmetic `+ - * / %` on numbers (GitHub semantics: `+` on strings is
  concat? — check the spec, implement faithfully or diagnostic).
- Object filters `obj.*` → array of values (used by `fromJSON` patterns).
- String functions actually used in the wild: `contains`, `startsWith`,
  `endsWith`, `format`, `join`, `toJSON`, `fromJSON`, `hashFiles` is runner-side
  (probably already handled — verify). Each unsupported function keeps its
  diagnostic; each implemented one gets table tests in the expressions package.

### 3. Service containers (`internal/runner`)

Today `credentials`/`ports`/`volumes` warn-and-ignore; no service log
streaming. Implement in order of value:
- `ports:` — map container port to host port (the per-port lock machinery
  already exists; `localhost:<port>` works per handoff-01 notes — verify what
  is actually missing before building).
- `volumes:` — bind-mount service dirs into the service container only.
- `credentials:` — username/password env injection into the service container.
- Service log streaming — services' stdout into the job log under a
  `service:<name>` marker, or document why deferred.
Anything still unsupported keeps its diagnostic. Service containers run
trusted-tier only today? Verify and document the trust rule.

### 4. Job `container:` options beyond `image`

`credentials`, `env`, `ports`, `volumes` on job containers — same pattern as
services. `env` is probably cheap; volumes need the same bind-mount path the
workspace uses. Anything exotic (`options:` docker run flags) stays a
diagnostic — do NOT pass raw docker flags from workflow YAML (that is a
privilege boundary; the security-isolation doc explains why).

### 5. `actions/cache` size quotas

Workspace-relative paths only today, no size quotas. Add a per-key or
global byte cap (`KERN_CACHE_MAX_BYTES` style, consistent with existing
`KERN_CACHE_MAX_AGE`), enforced at save time — over-cap saves get a diagnostic
in the step log, not a silent drop. Reuse the existing cache backend's
accounting if it has one; check `internal/runner` cache save path first.

### 6. Reusable-workflow caller maps (largest item — last, and OK to descope)

`strategy`/`concurrency`/`secrets:` maps on `uses: ./.github/workflows/x.yml`
callers → explicit diagnostics today. `secrets: inherit` is the common real-world
case — implementing just `inherit` (pass caller's secrets through the existing
per-run secret plumbing) covers most usage. `strategy`/`concurrency` passthrough
is a bigger design question — if not done, keep diagnostics and say so.

### 7. Explicitly OUT of scope

- OIDC `id-token`: permission exists in `permissions:` plumbing but token
  exchange needs an OIDC provider decision — leave diagnostic, note in PR.
- Multi-org, notification filters/templates, OpenAPI client migration of other
  domains, any `apps/web` visual work.
- Rootless BuildKit/Kaniko/DinD — deferred by security handoff, needs its own
  decision doc first.

## Traps (inherited)

- `go test ./internal/store` TRUNCATES the dev DB — use
  `KERN_TEST_DATABASE_URL=postgres://kern:kern@localhost:5432/kern_test?sslmode=disable`
  (migrate first if needed: `KERN_DATABASE_URL=<same> go run ./cmd/migrate`).
- `GOTMPDIR=$HOME/.cache/go-tmp` for Go builds.
- `pkill -f` kills your own shell — `pkill -x` or PIDs.
- Rebuild `.data/kern-*` before trusting local runtime.
- Job containers uid 1000 + read-only — chmod 0777 where needed.
- Unqualified JOIN columns → SQLSTATE 42702 (bit agent 03).
- openapi-typescript needs the TS5 scratch env (see rules above).
- Vite HMR can serve stale transforms after multi-file edits — restart dev
  server on phantom `ReferenceError`.

## Done means

- Items 1–6 each: implemented with tests, or explicitly descoped with the
  diagnostic path verified and the reason recorded in `docs/compatibility.md`.
- `docs/compatibility.md` rows updated to match reality — this file is the
  contract; drift between it and code is the bug.
- `openapi.yaml` + `api.gen.ts` regenerated if any route changed.
- All gates green; memory updated.
- PR(s) merged or open-with-evidence; JARVIS-style report: shipped /
  descoped-and-why. Then STOP.
