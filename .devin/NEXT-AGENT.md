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

- On `feat/session-2`: CI pnpm fix. Work in flight (uncommitted or branch-pushed):
  branding, `kern exec`, release pipeline.
- User directive: work autonomously → PR → wait green on fleet → merge → tag v0.1.0 →
  release workflow builds binaries + Docker images (GHCR) + kern CLI tarballs.

## Priority queue — pick the deepest self-contained vertical first

1. **`kern exec` offload** (user's top ask — "laptop too weak, offload job to fleet,
   see it live locally, get results back"): implemented as
   `PUT /projects/:p/source` (session, tar.gz → blob `sources/<project>/<uuid>.tar.gz`)
   → trigger run with `source.bundle_ref` → runner `GET /runners/jobs/:jobID/source`
   (downloads+untars instead of git clone) → `kern exec` CLI (upload, trigger,
   stream logs live via SSE, `--pull <dir>` fetches artifacts after). For docker
   builds: workflow saves image tar via upload-artifact; `docker load` locally —
   document in docs/remote-exec.md.
2. **Release pipeline**: `.github/workflows/release.yml` on `v*` tags — matrix go build
   (kern/kern-api/kern-runner linux+arm64), tarballs+checksums, gh release, docker
   build+push ghcr.io/dvorinka/kern-{api,runner} (needs deployments/runner.Dockerfile —
   docker-cli only inside, socket mounted; document). `version` ldflags on cmds.
3. **Branding**: done — `docs/brand/*.svg` → apps/web/public/favicon.svg, inline SVG
   Logo component in layout + login (replace ListChecks stand-in).
4. M4 GitHub App — still blocked on operator credentials.
5. Cache paths outside workspace; GITHUB_STEP_SUMMARY at job level aggregation;
   testcontainers store tests; `::error::` annotation parsing.

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
