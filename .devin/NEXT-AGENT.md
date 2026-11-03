# KERN — autonomous continuation brief

You are continuing work on KERN, a production-grade distributed CI/CD platform that
executes GitHub Actions workflows on self-hosted infrastructure. Read
`~/agent-memory/projects/kern.md` first (it holds durable state), then `project.md`
(spec), `plan.md` (milestone ledger), `docs/architecture.md`, `docs/compatibility.md`
in the repo. Work autonomously; the user trusts verification over claims.

## Environment

- Repo: `~/Desktop/PROG+HTML/KERN` — public remote `github.com/Dvorinka/kern` (origin,
  MIT). Module `github.com/dvorinka/kern`, Go 1.24.
- Voice: J.A.R.V.I.S. — polite, dry, brief. Ponytail restraint: smallest correct diff,
  stdlib before deps, no speculative abstractions, no scaffolding "for later".
- Subagents: none by default; if unavoidable use only `subagent_explore`/`subagent_general`.
- Memory loop: `cd ~/agent-memory && git pull --ff-only`, read `projects/kern.md`,
  write durable facts, commit + push after edits.

## Verified state (do not re-audit unless suspicious)

- M0–M3 done: Postgres queue (SKIP LOCKED + leases + reaper), Gin API (session cookie
  auth, runner bearer tokens, CSRF origin checks), workflow compiler + GitHub
  expression evaluator, Docker executor (ephemeral container/job, cap-drop ALL,
  no-new-privileges, read-only rootfs), AES-256-GCM secrets masked server-side,
  SSE log streaming, React 19 + Vite + Tailwind v4 dashboard at `apps/web`.
- M5 partial (verified live): `actions/upload-artifact`/`download-artifact` native
  (tar.gz to blob store, literal-root stripping), `jobs.<id>.container.image`,
  `concurrency.cancel-in-progress`, runner upsert by (org,name), bash→sh fallback
  in containers (alpine works). Migration 0002 applied.
- All pushed: `d2a99fc`, `634d504`, `0ea5dbd`, `1575e3f`, `93f2091`.

## Running the stack

```sh
docker start kern-pg-dev || docker run -d --name kern-pg-dev -p 5432:5432 -e POSTGRES_USER=kern -e POSTGRES_PASSWORD=kern -e POSTGRES_DB=kern postgres:16
cd ~/Desktop/PROG+HTML/KERN
go build -o .data/kern-api ./cmd/api && go build -o .data/kern-runner ./cmd/runner
setsid env KERN_ENV=development KERN_DATABASE_URL="postgres://kern:kern@localhost:5432/kern?sslmode=disable" KERN_BLOB_DIR=.data/blobs KERN_RUNNER_JOIN_TOKEN=kern-runner-join .data/kern-api > /tmp/kern-api.log 2>&1 &
setsid env KERN_ENV=development .data/kern-runner --control http://localhost:8080 --join-token kern-runner-join --work-root "$PWD/.data/runner" --concurrency 2 > /tmp/kern-runner.log 2>&1 &
cd apps/web && pnpm install && setsid pnpm dev > /tmp/kern-web.log 2>&1 &   # :5173 → :8080
```

Demo repo `/tmp/opencode/kern-demo` has a real workflow exercising artifacts +
container images. Login `admin@kern.local` / `kern-admin-password`, project slug `demo`,
cookie jar pattern `/tmp/opencode/kern-cookies.txt` (login via POST /api/v1/auth/login).

## Known traps (already bitten — do not repeat)

- `pkill -f X` / `pgrep -f X` matches your own `sh -c` cmdline → kills your shell.
  Use `pkill -x kern-runner` or `kill <pid>` from `ss -tlnp | grep :8080`.
- Docker Desktop runs in a VM: bind mounts only work under `$HOME`. Runner
  `--work-root` must be under `~` (use `.data/runner`), never `/tmp`.
- `blobs.ReadFrom` caps at 256KB — use `blobs.Open` for full objects.
- `setsid` required for background processes; plain `&` dies with the shell.
- A running binary is stale if built before latest source — rebuild before trusting
  runtime behaviour. `go run`/`go build` outputs in `.data/` are the live ones.
- gofmt gate: run `gofmt -l cmd internal migrations` — must be empty.

## Priority queue — pick the deepest self-contained vertical first

1. **Artifacts UI** (small): `/artifacts` page — list via `GET /api/v1/artifacts`,
   download link `GET /api/v1/artifacts/:id/download`. Nav entry in
   `apps/web/src/components/layout.tsx` (Package icon), route in `main.tsx`, types
   in `lib/types.ts` (Artifact already shaped in `internal/store/artifacts.go`).
   Also fix cosmetic: `uses:` steps display "Checkout <ref>" — derive label from
   the action ref in `internal/api/handlers_runner_agent.go` step name / or compiler.
2. **Step `if` conditions + `conclusion` plumbing gaps**: `verify artifact`-style
   skipped steps show empty conclusion — check step skip propagation.
3. **`if-no-files-found` warn/error/skip** full semantics in
   `internal/runner/artifact.go` (only `error` handled today).
4. **`actions/cache`** — biggest remaining compat unlock after artifacts. Needs a
   cache_entries store write path + blob key scheme `cache/<org>/<key>.tar.gz` +
   restore-keys prefix lookup. Check schema (`cache_entries` table exists).
5. **Job `outputs` → `needs.<id>.outputs`** cross-job resolution (jobs table has
   outputs + outputs_spec already).
6. **`hashFiles`** — needs workspace hashing; evaluate on runner for step-level,
   reject job-level with diagnostic.
7. **Cron `schedule:`** — plan stores triggers raw; scheduler Tick can mint runs.
8. **M4 GitHub App** — blocked on operator credentials (App ID, private key,
   webhook secret). Ask user only if everything above is done; webhook endpoint
   already verifies HMAC and persists deliveries.
9. **Testcontainers-go integration tests** for store layer (claim race, lease
   reaping, cancel-in-progress).

## Non-negotiables

- No fake/silent behaviour — unsupported features emit diagnostics, never fake success.
- `gofmt` clean, `go build ./...`, `go test ./... -count=1`, `go vet ./...`,
  `pnpm lint` + `pnpm build` green before every push.
- Update `docs/compatibility.md` when a feature lands; keep it honest.
- Commit style: `Authored By: TDvorak <info@tdvorak.dev>` trailer; push to origin.
- Report in J.A.R.V.I.S. voice: what shipped, what verified (with evidence), what
  deferred and why. No enthusiasm, no hedging.
