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

- M0–M3 done; W1–W5 done (kern CLI, enrollment, fleet metrics, split, self-heal+diagnosis).
- M5 partial: artifacts native, job container.image, concurrency cancel-in-progress,
  actions/cache (+restore/save, restore-keys, deferred save), needs.*.outputs,
  hashFiles (step level), on:schedule cron, artifact expiry sweep (90d),
  GITHUB_STEP_SUMMARY → per-step blob → sanitized markdown in run detail UI,
  cache eviction sweep (KERN_CACHE_MAX_AGE, 7d default).
- Web dashboard pages: overview, projects(+detail), runs(+detail DAG/SSE logs/summaries),
  runners(metrics+self-heal toggle), secrets, artifacts(+download/delete), cache(list+evict).
- Migration 0006 = steps.summary_ref. Latest: branding (logo SVG in layout+login), version
  ldflags, `kern exec` offload feature (see below).
- Stack runs: postgres docker kern-pg-dev :5432, api :8080, runner tdub, vite :5173.
  Login admin@kern.local / kern-admin-password. Cookie jar /tmp/opencode/kern-cookies.txt.
  triggerRun needs `-d '{}'` JSON body.

## GitHub + Proxmox fleet (this session's state)

- `ci.yml` runs on `[self-hosted, linux, x64]` — the Proxmox fleet.
- Three kern runners: `ci-runner-901-kern`, `ci-runner-902-kern` (in /opt/gha/kern on
  CTs 901/902), `ci-runner-903-kern` (CT 903 proxmox-nab9, tarball install same dir).
  systemd units `actions.runner.Dvorinka-kern.ci-runner-<ct>-kern.service`.
- SSH: `ssh -i ~/.ssh/proxmox_devin root@10.0.0.10` (LAN; netbird path dead). `pct exec <ct> -- bash -lc ...`.
- Runner gotchas: `.env` in runner dir needs `HOME=/root` (else go env fails) + PATH;
  `.runner_migrated`/`.service` files = configured marker; relay dir copy leaves absolute
  symlinks + migrated state — rebuild, don't copy blindly. svc.sh: install→start.
- `pnpm/action-setup` needs explicit `with: version:` — root package.json absent.
- Public repo: fork PRs need approval before self-hosted execution (GitHub default).

## This session's branch state

- `feat/session-2` merged via PR #7 (squash → master `1a06683`).
- `v0.1.0` tag pushed → release.yml run FAILED at `binaries (darwin, arm64)`:
  `cmd/kern/cmd_auth.go` used `unix.TCGETS/TCSETS` (Linux-only ioctls).
  Fixed with `golang.org/x/term` (`term.ReadPassword` + `IsTerminal` piped
  fallback), commit `8c36fac` on master, `v0.1.1` tagged + pushed.
- `kern exec` SHIPPED `d88ec56` + docs `f3a29b2`: source bundle upload
  (`PUT /projects/:p/source`), `bundle_ref` trigger field, runner-side
  `GET /runners/jobs/:jobID/source`, checkout-by-untar replaces git clone,
  CLI streams logs and `--pull`s artifacts. Verified live on demo run #3.
- M4-lite SHIPPED `3f0cf29`+`06fa4a2`: push webhook → run compiled AT the
  pushed SHA → commit status back. Live: hook 707211794, cloudflared tunnel,
  `kern` project synced from GitHub, dogfood `kern.yml` green (run #5),
  `kern/KERN CI: success` status posted on commit f06b590.
- `v0.1.1` release: binaries ×4 + SHA256SUMS shipped and checksum-verified;
  `images` job FAILED — both Dockerfiles pinned golang:1.24 vs go.mod ≥1.26
  (toolchain switcher can't help inside buildkit). Also `kern --version`
  was unwired. Fixed `e526eb4` (Dockerfiles + release.yml → 1.26, version
  command), tagged `v0.1.2` → release refiring, queued behind fleet CI.
- Gotcha: `kern.yml` fires on BOTH GitHub Actions (fleet CTs) and KERN —
  the GHA copies fail fast (no docker on those runners). Harmless but noisy
  commit checks; consider runs-on gating or deleting if it annoys.
- User directive: work autonomously in a loop — build, monitor, improve, do not stop.

## Priority queue — pick the deepest self-contained vertical first

1. **Release — DONE: `v0.1.2` fully verified.** All 5 legs green; GH release
   carries 4 tarballs + SHA256SUMS (checksum verified), `kern --version` on the
   downloaded linux-amd64 binary prints `kern v0.1.2`, ghcr.io/dvorinka/kern-
   {api,runner}:v0.1.2 + :latest pushed (runner digest e30ebd7b… confirmed in
   job log). Stale `v0.1.0` tag left in place — user's call.
2. **`kern exec` offload — DONE** (`d88ec56`, verified live). Remaining polish:
   content-addressed bundle dedupe, Ctrl-C → run-cancel passthrough, compose
   wrapper. For docker builds the workflow saves an image tar via
   upload-artifact; `docker load` locally — pattern in docs/remote-exec.md.
3. **M4-lite — DONE (live)**: push webhooks → runs + commit statuses via token.
   Ops state: repo hook id 707211794 on Dvorinka/kern → cloudflared quick tunnel
   (`~/.t3/tools/cloudflared/2026.10.4/linux-x64/cloudflared tunnel --url
   http://localhost:8080`, ephemeral trycloudflare URL — re-register hook when
   it dies). api env: KERN_GITHUB_WEBHOOK_SECRET (in .data, ephemeral) +
   KERN_GITHUB_TOKEN=`gh auth token`. kern.yml dogfood workflow runs on:push in
   golang:1.26. Gap: pull_request events + check-runs need a GitHub App — do NOT
   invent secrets.
4. Cache paths outside workspace; GITHUB_STEP_SUMMARY at job level aggregation;
   testcontainers store tests; `::error::` annotation parsing; composite/JS/
   Docker actions, reusable workflows, services, environments, OIDC.

## Known traps (already bitten — do not repeat)

- `pkill -f X` matches your own `sh -c` cmdline → kills your shell. Use `pkill -x` or kill by PID from `ss -tlnp`.
- Docker Desktop runs in a VM: bind mounts only work under `$HOME`. Runner `--work-root` must be under `~`.
- `blobs.ReadFrom` caps at 256KB — use `blobs.Open` for full objects.
- `setsid` required for background processes; plain `&` dies with the shell.
- Rebuild binaries before trusting runtime behaviour — stale `.data/kern-*` bites.
- gofmt gate: `gofmt -l cmd internal migrations` must be empty. Pre-push: go build+test+vet, `pnpm lint`+`pnpm build`.
- `go test ./internal/store` TRUNCATES the dev DB (it-* leftovers) — rejoin runner + resync demo after.
- expressions evaluator only indexes map[string]any — never map[string]string.
- Empty concurrency_group ''='' matched cancel-in-progress (guard `<>` '').
- gin: static segment can't collide with `:id` routes (use `/enroll-tokens`).

## Non-negotiables

- No fake/silent behaviour — unsupported features emit diagnostics, never fake success.
- `gofmt` clean, `go build ./...`, `go test ./... -count=1`, `go vet ./...`,
  `pnpm lint` + `pnpm build` green before every push.
- Update `docs/compatibility.md` when a feature lands; keep it honest.
- Commit style: `Authored By: TDvorak <info@tdvorak.dev>` trailer.
- Report in J.A.R.V.I.S. voice: what shipped, what verified (with evidence), what deferred and why.

## HANDOFF — path to "fully functional / real-environment ready"

Verified green today: `kern exec` offload, M4-lite (push webhook→run→commit
status via token), v0.1.2 release (4 tarballs + SHA256SUMS + GHCR images),
KERN CI dogfood loop (kern.yml green on every push, status posted).
GitHub App exists: kern-ci (app_id 5607782, installation 178470896, pem in
~/Downloads, api env already carries APP_ID+PRIVATE_KEY).

Remaining work, ordered by value:

1. M4 finish — JWT sign with pem → POST /app/installations/:id/access_tokens →
   statuses/check-runs posted as the App (drop PAT). pull_request event handling
   in processGithubDelivery (checkout head_sha, secrets withheld on fork runs).
   github_apps table + manifest-flow "Connect GitHub" button so every user
   self-serves an App (form POST manifest→settings/apps/new→callback converts
   code→id/pem/secret→install redirect).
2. Correctness gaps — pull_request/checkout ref semantics, timeout-minutes,
   step-level `if:`, concurrency-group cancel across triggers, continue-on-error.
3. Performance (the user's next ask) — persistent runner-side caches so warm
   runs skip module/image downloads entirely; container image GC; bundle
   content-addressed dedupe; log flush batching; parallel job caps.
4. Reliability — runner claim retry/backoff, stale-lease job reaper check,
   run timeout watchdog, api graceful shutdown.
5. Security — fork-PR isolation policy, secret masking breadth test, webhook
   replay window.
6. Ops — production deploy doc (compose + TLS), retention for logs/artifacts,
   /metrics endpoint, backup story.
7. Frontend — artifact browsing, webhook delivery viewer, run cancel coverage.
8. Tests — webhook processing integration tests, testcontainers e2e, compiler
   fuzz.

Traps unchanged: pkill -f kills your shell; /tmp tmpfs 512m in job containers;
docker bind-mounts only under $HOME; rebuild .data/kern-* before trusting
runtime; store tests truncate dev DB.
