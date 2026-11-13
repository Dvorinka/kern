---
name: kern-ops
description: Operate a KERN self-hosted CI fleet — runner lifecycle (enroll, drain, rebuild, version-stamp, self-update), run diagnosis, env-file corruption prevention, DB access, and release mechanics. Use when working on or against a KERN deployment, when CI jobs fail on self-hosted runners in ways hosted runners don't reproduce, when runners report the wrong version, or when asked to triage a stuck/queued/failed run.
---

# kern-ops

Operate a KERN fleet (GitHub Actions-compatible self-hosted CI). The repo's
`docs/runbook.md` is the deep symptom -> check -> fix reference; this skill is
the fast path plus the failure modes that keep recurring.

Start every investigation with `kern doctor` — it probes `/healthz`, `/readyz`,
`/meta`, auth, queue pressure, webhook failures and per-runner liveness.
`kern status --watch` is the live dashboard.

## Command surface

| Task | Command |
|---|---|
| Fleet health | `kern doctor`, `kern status [--watch]` |
| Run triage | `kern diagnose <run-id>` (statuses + failing step log tail), `kern runs list`, `kern runs show <id>`, `kern runs wait <id>` |
| Re-run | `kern runs rerun <run-id>`; `--failed` requeues only failed jobs, `--job <name>` targets one job. **Prefer the full run UUID** — short-id resolution is a recent addition; if a short id is rejected, use the UUID from `kern runs list` |
| Runner lifecycle | `kern runners list` (status, labels, load, disk, **version**), `kern runners drain|undrain <id>`, `kern runners self-update <id>`, `kern runners wake`, `kern runners install`, `kern runners enroll` |
| Logs | `kern logs -f`, `kern runs logs <id>` |
| Manual run | `kern runs trigger` |
| DB (compose deploy) | `docker exec <deployments>-postgres-1 psql -U kern -d kern` — table is `workflow_runs`, not `runs` |

Agent surface: `kern` MCP endpoint exposes run lifecycle, secrets and runner
drain for agents. HTTP API: `GET /api/v1/overview` mirrors `kern status`;
`GET /api/v1/webhooks/deliveries` for webhook forensics (payload excluded —
fetch `GET /webhooks/deliveries/:id`).

## The playbook that matters

### 1. Version-stamped runners, or nothing updates

A runner image built without `--build-arg VERSION=vX.Y.Z` defaults to
`VERSION=dev`. A `dev`-stamped runner:

- is refused by `RunnerShouldOffer` (fleet thinks it can't take jobs cleanly),
- **cannot self-update** — it silently stays on whatever code it was built
  with, including already-fixed bugs.

Always either pull `ghcr.io/dvorinka/kern-runner:vX.Y.Z` (anonymous pull works)
or build with an explicit `VERSION` arg. Verify with `kern runners list` — the
version column is ground truth, not the image tag on the host.

**Corollary:** a runner on stale code reproduces *already-fixed* bugs. Before
filing or debugging a fleet-side defect, check the runner's reported version
against the release that contains the fix.

### 2. Env-file corruption = stale runner, not the renderer

Symptom: jobs fail with `invalid env file step-N.env: variable '"type":
"service_account",' contains whitespaces` — multi-line secrets (service-account
JSON, PEM, multiline values) leak into the step env file.

Cause observed: runners on pre-fix builds wrote multi-line values into
`step-N.env`. Current code routes multi-line values through `stepEnvExtra`
(`-e` flags), which is whitespace-safe — the renderer provably cannot emit
`\n` into the env file. Fix the runner, not the workflow: drain it, rebuild/
roll to a stamped ghcr image, un-drain.

Related gap (fixed upstream of old runners): `docker:` actions dropped
`stepEnvExtra`, so multi-line secrets were silently missing in `docker://`
and `runs.using: docker` actions.

### 3. Drain discipline

- `kern runners drain <id>` stops new claims; in-flight jobs finish normally.
  Draining is the correct quarantine for a suspect runner — jobs then land on
  healthy fleet members and the fleet stays green while you rebuild.
- Rebuild the container on the stamped ghcr image preserving env/binds, then
  confirm it heartbeats online with the right version before `undrain`.
- Keep `KERN_RUNNER_UPDATE=1` in the runner env so the self-update channel
  stays armed.
- A slow `docker pull` of the runner image is normal (~minutes); run it
  detached rather than blocking the session.

### 4. Release -> self-update chain

`git tag vX.Y.Z` -> `release.yml` builds the ghcr runner image **and** signs
`kern-update.json` -> each runner's updater (or the `kern-updater` sidecar on
compose deployments) applies it. A release that published no
`kern-update.json` + `.sig` leaves the fleet behind forever — check release
assets when a runner won't update.

Per-host memory: `KERN_JOB_MEMORY` in each runner's `.env`.

### 5. Job environment hard limits seen in practice

- Job containers have a read-only rootfs by default and **no sudo, no docker
  socket** unless the runner grants privilege — `needs_docker` jobs need a
  runner advertising `kern-docker` (host executor) or privileged mode.
- Trust gating: trusted/protected refs get root+rw+sudo shim+docker.sock;
  fork PRs stay sandboxed.
- Multi-line secrets must travel via `stepEnvExtra` — see playbook item 2.
- `GOTMPDIR`/`TMPDIR` inside jobs should point at runner workspace
  (`/kern/tmp`-style), not the container default — Go `fork/exec` and linking
  fail on cramped or noexec temp mounts.

### 6. API access quirks

- Plain HTTP clients (urllib, curl without browser headers) can be refused by
  the edge (Cloudflare 403/1010). Use the `kern` CLI binary or the MCP
  endpoint — it carries auth correctly.
- Fork PR runs park at run level under `fork_gate`; environment approvals park
  jobs in `waiting`/`waiting_approval`. Both are intentional states, not
  stuck — approve via `kern deployments list` / `kern runs approve`.
- Webhook deliveries are retained `KERN_WEBHOOK_RETENTION` (default 30d) then
  pruned — absence of an old delivery row is not evidence it never arrived.

### 7. Disk and load gates

- Below `KERN_RUNNER_DISK_MIN_FREE_PCT` (default 10%) the runner prunes
  reclaimable Docker space — **only while idle**; below the critical tier it
  drops ledger images and evicts `_cache` to half cap.
- `KERN_RUNNER_MEM_MIN_FREE_BYTES` (default 1 GiB) pauses claims under host
  memory pressure.
- A runner that heartbeats but claims nothing: check `draining`, the load
  gate (`load gate` lines in the runner log), and label mismatch
  (`runs-on:` labels vs `kern runners list`).

## Filing bugs

Report KERN/CI-CD friction as issues on the repo with: the run id, the
failing step's log excerpt, runner reported version (`kern runners list`),
and `kern doctor` output. Never paste secrets — scrub tokens, env values and
Authorization headers first.
