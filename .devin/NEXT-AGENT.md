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

- M0–M4 done; W1–W5 done (kern CLI, enrollment, fleet metrics, split, self-heal+diagnosis).
- M4: GitHub App auth (JWT→installation tokens, `github.Resolver` +
  `Server.githubClient` cascade env-app → org-apps → PAT), pull_request events
  (head-SHA runs, fork trust), github_apps table + manifest flow, multi-secret
  webhook HMAC. Migration 0007.
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

M4 — DONE (2026-11-04, commits 42b801c + a526fb9):
- GitHub App auth live: RS256 JWT → installation access tokens (cached per
  installation), repo → installation resolved + cached. Verified: commit
  statuses posted by `kern-ci[bot]` on master pushes and PR heads.
- pull_request events live: head-SHA runs, `refs/pull/N/head` ref, fork heads
  → trust `fork` (no secrets, own cache scope, checkout from head repo),
  same-repo PRs → `pull_request`; default action set + types/branches filters.
  Verified: PR #9 run green, `kern/KERN CI: success` on its head sha.
- Manifest flow shipped: github_apps table (mig 0007, pem+secret encrypted
  bytea), GET /github-app/manifest + /github-app/callback (session-gated,
  302→install), multi-secret webhook verification, env-app → org-apps → PAT
  cascade. "Connect GitHub" button on the projects page. NOT yet verified
  end-to-end in a browser (needs a human click through github.com; also needs
  KERN_PUBLIC_URL set to a public URL or the hook URL will be localhost).
- api env gotcha fixed: multi-line PEM doesn't survive shell exports —
  KERN_GITHUB_APP_PRIVATE_KEY_FILE added (running api uses it); inline PEM
  accepts literal \n.
- Hook 707211794 now subscribes push + pull_request. The App's own webhook
  also delivers check_run/check_suite (ignored, recorded).
- Dogfood fix: kern.yml GOMODCACHE must be absolute — /github/workspace/.gomod.
- New trap seen: runner concurrency 2 × go builds in Docker Desktop VM pushed
  host load >40 → api heartbeats took 14-32s. Not a bug — KERN_RUNNER_CONCURRENCY
  caps it. Parallel job caps belongs in the perf work.

PRODUCTION DEPLOY (2026-11-04): CT 908 `kern` on nab9 — unprivileged LXC,
nesting+keyctl, 4c/8G/60G, DHCP (.65). postgres + api + runner via
deployments/docker-compose.yml + /opt/kern/.env on the CT; source at
/opt/kern/src (redeploy: git pull + compose up -d --build). cloudflared
tunnel `kern` (f66c9aed…) on the CT serves kern.tdvorak.dev; api serves the
SPA from KERN_WEB_DIR=/app/web. Admin info@tdvorak.dev. The App webhook is
now THE event source (url=kern.tdvorak.dev, secret shared with the old repo
hook — repo hook 707211794 deleted to prevent double delivery; local dev api
gets no deliveries now). Verified: run 7 all-green on kern-908 runner,
kern-ci[bot] posts kern/KERN CI:success.
Traps fixed en route: docker.io bookworm CLI API 1.41 < dockerd-25+ floor
1.44 (static docker 27.5 CLI in runner image); job uid 1000 needs 0777 work
dirs via explicit chmod (umask eats MkdirAll mode; Docker Desktop hides it);
git safe.directory=* env for uid-mismatch clones; /opt/kern/app.pem must be
0644 (api runs as non-root `kern`); pct exec PATH lacks /usr/local/bin.

PERF/RELIABILITY PASS (2026-11-04, commits 4a4e10a + 9c68fbf + aca8ffc):
- Persistent runner cache: <work-root>/_cache mounts into every job at
  /kern/cache (KERN_CACHE_DIR exported). kern.yml moved gomod/gocache/gopath
  onto it. VERIFIED on CT 908: baseline job 100s -> warm job 13s (build 71s->3s,
  vet 10s->0s, test 15s->2s). Cold-cache run: 114s.
- Image GC: pulled images tracked in image-usage.json, removed after
  KERN_RUNNER_IMAGE_MAX_AGE (default 168h) idle-only. Load gate:
  KERN_RUNNER_LOAD_GATE pauses claims above n*cpu loadavg1 (set to 6 on CT 908).
  Claim backoff exponential 1s-30s.
- Log pump: mutex fix (stdout/stderr raced on buffer), 256KiB + 1s flush.
- Content-addressed source bundles: kern exec hashes path+content, resolves
  via GET /projects/:p/source/resolve, skips upload on hit; PUT ?sha256= stores
  under digest key and dedupes server-side too.
- continue-on-error: failed CoE job no longer fails the run; needs.<id>.result
  reports success. Stored conclusion stays 'failure' for the UI.
- Watchdog: in-progress jobs past timeout_seconds now failed server-side.
  KERN_LOG_RETENTION wired: hourly sweep prunes step log+summary blobs.
- Docs: docs/deploy.md (production checklist), .env.example + compatibility.md
  updated (incl. documented PR merge-ref divergence: we run head SHA).
- Tests: FuzzCompile (YAML corpus), logPump batch tests (race-clean), bundle
  digest stability, store tests now refuse non-kern_test DSNs (was truncating
  dev DB).

Remaining work, ordered by value:

1. Check-runs as the App (currently commit statuses only); pull_request_target
   strict semantics; first-time-contributor approval gate for fork PRs.
2. PR merge-ref semantics (refs/pull/N/merge) if wanted — head SHA is the
   current documented choice.
3. Security — fork-PR isolation policy, secret masking breadth test, webhook
   replay window.
4. Ops — artifact/run-row retention knobs beyond logs, backup story, metrics
   alerting rules.
5. Frontend — artifact browsing, webhook delivery viewer, run cancel coverage.
   NEXT PHASE IS USER-DIRECTED UI REDESIGN — hand back, do not restyle.
6. Tests — webhook processing integration tests, testcontainers e2e.

Traps unchanged: pkill -f kills your shell; /tmp tmpfs 512m in job containers;
docker bind-mounts only under $HOME; rebuild .data/kern-* before trusting
runtime; store tests truncate dev DB.
