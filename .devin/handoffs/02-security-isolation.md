# HANDOFF 02 — Security, trust & isolation

**Status:** queued. Run after or parallel to 01 (disjoint file ownership — see below).
**Source:** distilled from the "KERN CI/CD Overview" report (2026-11-07) + `docs/production.md`.
**Scope ownership:** runner executor hardening (`internal/runner`), trust/secrets paths (`internal/secrets`, trust resolution in orchestrator/api), GitHub credential issuance, network policy for job containers, `docs/` security notes. Do NOT touch `apps/web` styling or scheduler placement logic (handoff 01's territory — coordinate on `internal/runner` files you both need).

## Mission

Close the gap between "sandbox-by-default" and "safe to run strangers' code":
fine-grained trust levels, ephemeral credentials, no ambient Docker authority,
and network egress control for untrusted jobs.

## Orientation (do first)

1. `cd ~/agent-memory && git pull --ff-only`; read `projects/kern.md` end to end.
2. Repo `~/Desktop/PROG+HTML/KERN`, remote `github.com/Dvorinka/kern`.
   Read `project.md`, `plan.md`, `docs/architecture.md`, `docs/compatibility.md`,
   `docs/production.md`, `SECURITY.md`.
3. Branch off `origin/master` in a worktree (`git worktree add
   ~/.t3/worktrees/KERN/<name> -b feat/<name> origin/master`). The
   `feat/console-interactivity` UI port is uncommitted — leave it alone.
4. Local loop: `just dev` (api :8080), `just runner`, `just test`.
   Login `admin@kern.local` / `kern-admin-password`.

## Rules (non-negotiable)

- J.A.R.V.I.S. voice: polite, dry, brief. No emojis.
- Ponytail: smallest correct diff, stdlib before deps, no speculative scaffolding.
  New non-trivial logic leaves a runnable check.
- Never fake/silence behaviour — unsupported features emit diagnostics.
- **Security work gets the full pre-push gate plus a self-review pass:**
  `gofmt -l cmd internal migrations` empty, `go build ./...`,
  `go test ./... -count=1`, `go vet ./...`, `cd apps/web && pnpm lint && pnpm build`.
- PR-first; never push to main. Commits carry trailer
  `Authored By: TDvorak <info@tdvorak.dev>`.
- No GLM subagents.
- Memory loop: append durable facts to `~/agent-memory/projects/kern.md`, commit, push.

## Verified state — already done, do not rebuild

- Per-job container hardening: uid 1000, `--cap-drop=ALL`, `--security-opt
  no-new-privileges`, read-only root fs, `/tmp` tmpfs 512m, resource limits.
- Trust levels exist: `trusted` (push/schedule), `pull_request` (same-repo head),
  `fork` (no secrets, own cache scope, head-repo checkout), `pull_request_target`
  synthesized from `pull_request` (base-branch code).
- Fork gate: project `fork_gate` off|all|first-time → `awaiting_approval`;
  approve via API/CLI/UI; audit `run.approve`.
- Secrets: AES-256-GCM at rest, write-only after create, server-side log masking
  (longer-first exact substring).
- `pull_request_target` never receives fork secrets by construction.
- Job containers do NOT get `/var/run/docker.sock` — docker-in-job is a known
  compat blocker (`docs/porting.md`), not a live hole.
- GitHub App auth: RS256 JWT → installation tokens, cached per installation;
  env-app → org-apps → PAT cascade.
- Webhook HMAC verification (multi-secret), delivery dedupe, audit log.
- Security headers + CSP on the API; `/metrics` auth-gated; scope/role
  enforcement (`requireScope`/`requireRole`) done (PRs #40/#41).
- Auto-update: ed25519-signed `kern-update.json`, verified live on v0.3.1.

## Scope — ordered queue

1. **Isolation audit first.** Write `docs/security-isolation.md`: a matrix of
   exactly what each trust tier's job container gets — caps, seccomp profile,
   userns, network mode, env vars, mounts, secrets visibility, docker access.
   Verify every claim against `internal/runner` executor code, not README.
   This doc is the baseline every later item diffs against.
2. **Docker-build path decision.** `docker://` and Dockerfile actions exist in
   the engine — find HOW they execute today (runner-side docker CLI? in-job
   buildkit? skipped?). Whatever it is, it is the highest-privilege path in the
   system. Options, smallest-first: (a) gate behind trust level + explicit
   project flag if it already runs unprivileged-ish; (b) rootless BuildKit or
   Kaniko inside the job container for untrusted tiers; (c) dind sidecar only
   for trusted projects. Pick per evidence from item 1; document the choice.
   The report's warning stands: never mount the host docker socket into an
   untrusted job — that is root on the host.
3. **Ephemeral runner registration.** `kerne_` enroll tokens exist
   (migration 0003). Add the ephemeral half: token TTL/single-use option,
   `KERN_RUNNER_EPHEMERAL=1` self-deregister (runner deletes its row + refuses
   rejoin after job completion or idle timeout), and a JIT-style endpoint
   (`POST /runners/jit` → short-lived config) so external orchestrators
   (actions-runner-controller, custom scalers) can mint runners on demand.
   This is the prerequisite for handoff 01's autoscaling story — build it
   runner-side first, scaler-agnostic.
4. **GITHUB_TOKEN / per-run credentials.** Today workflows have no
   `GITHUB_TOKEN`; the workaround is PAT-as-secret. Implement the real thing
   for trusted runs: mint a short-lived GitHub App installation token scoped
   to the repo + minimal permissions, inject as `GITHUB_TOKEN` env, never for
   `fork` trust. Honor `permissions:` as the ceiling (field is recorded today,
   not enforced — `docs/production.md` compat tail). This is simultaneously a
   security and compat win — it unblocks `git push`-from-CI workflows.
5. **Network egress policy.** Untrusted-tier job containers currently egress
   freely. Add a per-job docker network with an allowlist (github.com + api
   endpoints + configured registries) enforced via iptables or a user-defined
   bridge — start with `fork` trust only, measure breakage on the 13-repo
   porting fleet before widening. Document the operator alternative (egress
   proxy) in `docs/deploy.md`.
6. **Secret hygiene pass.** Verify masking covers every exfil channel:
   step summary blobs, check-run output, artifact names, diagnosis strings,
   webhook payloads. Audit `audit_events` captures secret read vs write.
   Add secret rotation doc note (no auto-rotation — ops concern).
7. **Sandbox evaluation — doc, not code.** Write the gVisor (`runsc`) vs
   Firecracker assessment into `docs/security-isolation.md`: measured overhead
   on one real job if cheap to test, recommendation per trust tier. Do NOT
   integrate a runtime — that's a separate decision for the user.

## Explicitly out of scope

- Scheduler placement/bin-packing, cache locality, disk GC — handoff 01.
- Metrics/dashboards/quotas/SSO — handoff 03 / `docs/production.md`.
- `id-token`/OIDC for cloud auth, `workflow_run`/`release`/
  `repository_dispatch` events, `vars` context — compat tail, separate effort.
- Multi-org isolation audit — real but belongs with SSO work (handoff 03).

## Traps

- `pkill -f` kills your own shell — `pkill -x` or PID kill.
- `go test ./internal/store` TRUNCATES the dev DB.
- Job containers: uid 1000, read-only root, work dirs need explicit 0777 chmod;
  git needs `safe.directory=*` env.
- Docker Desktop bind mounts only under `$HOME`; CT 908 paths must match
  inside/outside runner container.
- pem mounts into non-root containers need 0644; multi-line PEM doesn't survive
  shell exports — use `KERN_GITHUB_APP_PRIVATE_KEY_FILE`.
- `pull_request_target` cannot be subscribed via GitHub App permissions — a
  `pull_request` delivery synthesizes it. Do not tell the operator to toggle it.
- Rebuild `.data/kern-*` before trusting local runtime; fleet SSH details in
  `~/agent-memory/projects/kern.md` (rotate — verify before use).

## Done means

- `docs/security-isolation.md` exists and every matrix row is verified against code.
- Docker-build path decided, implemented or explicitly gated, documented.
- Ephemeral runner mode live on at least one fleet runner (evidence: runner
  row removed/recreated cleanly).
- `GITHUB_TOKEN` minted for trusted runs OR a written reason it was deferred;
  `permissions:` enforced as ceiling if implemented.
- Egress allowlist active for `fork` trust on dev stack; breakage assessed.
- Masking coverage verified across all exfil channels (test or walkthrough).
- `docs/compatibility.md` + `SECURITY.md` updated; all gates green; memory updated.
