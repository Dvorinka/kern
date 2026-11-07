# Security & isolation model

What KERN actually guarantees when it runs code, verified against
`internal/runner`, `internal/api` and `internal/store` — not aspirational.
Every row below names the code that enforces it. Where a guarantee does not
exist, this document says so.

Scope: single KERN control plane, jobs executed by `kern-runner` agents on
Docker hosts. Multi-org tenant isolation is a separate audit (see
`docs/production.md` roadmap).

## Trust tiers

Trust is assigned when a run is created and stored on `runs.trust_level`.

| Tier | Assigned when | Executor profile |
| --- | --- | --- |
| `trusted` | `push`, `schedule`, `release`-class triggers, `pull_request_target`, manual/API runs (default), same-repo `pull_request` when the project's `pr_trust` setting is on | **privileged** — see below |
| `protected` | manual/API opt-in only | **privileged** |
| `deployment` | manual/API opt-in only | sandboxed, but secrets-eligible |
| `pull_request` | `pull_request` event, same-repo head, `pr_trust` off | sandboxed, no secrets, no token, full egress |
| `fork` | `pull_request` event, fork head (`head.repo.fork` or different full name) | sandboxed, no secrets, no token, **allowlisted egress** |

`pull_request_target` cannot be subscribed as a GitHub App event; a
`pull_request` delivery synthesizes it and the run executes the base repo's
default-branch code, hence `trusted`.

Fork runs can additionally be held in `awaiting_approval` by the project
`fork_gate` setting (`off` | `all` | `first-time` on `author_association`),
approved via `POST /runs/:id/approve`.

## Executor isolation matrix

`executor.privileged()` (`internal/runner/executor.go`) returns true for
`trusted` and `protected` only. Everything else gets the sandbox column.

| Control | privileged (`trusted`, `protected`) | sandboxed (`pull_request`, `fork`, `deployment`) |
| --- | --- | --- |
| Step user | root (`execUID` → 0, `sudo` shim installed) | uid 1000 (`jobUID`) |
| Linux capabilities | unrestricted | `--cap-drop ALL` |
| Privilege escalation | unrestricted | `--security-opt no-new-privileges` |
| Root filesystem | writable | `--read-only` |
| `/tmp`, `/run` | image defaults | tmpfs `noexec,nosuid`, 512m/64m |
| CPU / memory | `KERN_JOB_CPUS`/`KERN_JOB_MEMORY` or host share | same limits, default 1 cpu / 1g |
| `/var/run/docker.sock` | **mounted** — `docker`, `buildx`, `compose` work | **never mounted** |
| Network | `--network host` (GitHub VM-runner parity: `docker run -p` publishes on localhost) | per-job bridge; `fork` additionally confined — see egress |
| Workspace/actions/cache mounts | rw | actions dir mounted `:ro`; cache shared per scope |
| Checkout | host-side `git clone` with a claim-time installation token (never enters the container) | same |

Job containers are created with `docker create --rm` and removed on cleanup;
the workspace lives under the runner work root and is deleted per job.
Docker-action and lifecycle-hook sibling containers
(`internal/runner/dockeraction.go`, `lifecycle.go`) receive the same
hardening flags as the job container.

**Residual, by design:** the privileged tier mounts the host Docker socket.
That is root-equivalent on the runner host — it is the documented price of
`docker`/`compose` step parity and is why `fork`/`pull_request`/`deployment`
can never reach it. Do not add trust tiers casually.

## Network egress policy

Default (non-privileged, non-fork): the job container sits on the default
Docker bridge or a per-job `kern-svc-<job>` bridge when portless services
exist — ordinary NAT egress, same as a GitHub-hosted runner.

`fork` trust is stricter (`internal/runner/egress.go`):

1. `docker network create --internal kern-ego-<job>` — an internal network
   has **no external route**. DNS still resolves via the embedded resolver,
   but no packet can leave the bridge toward the internet. The docker
   bridge gateway IP (the host itself) remains reachable — standard
   user-defined-bridge behavior, identical to the existing `kern-svc-*`
   service networks; host services should not rely on that path being
   firewalled.
2. A per-job proxy container (`kern-egro-<job>`, alias `kern-egress`) joins
   that internal network **and** the default bridge. It runs
   `kern-runner egress-proxy` — this same binary packaged into a minimal
   image the runner builds once (`<workroot>/_egress`, tag
   `kern-egress:local`): `scratch` for the static shipped binaries, a
   locally present busybox/alpine for dynamic dev builds. No required
   external image pull.
3. The proxy is a minimal forward proxy: `CONNECT` allowed only to
   `:443` on allowlisted hosts; plain-HTTP forwarding allowed on ports
   80/443 to allowlisted hosts. Everything else gets 403. It cannot be
   turned into a generic TCP relay (no SSH, no arbitrary ports).
4. The job container, its service containers, and every docker-action /
   lifecycle sibling container receive `HTTP(S)_PROXY=http://kern-egress:3128`
   (both cases) plus `NO_PROXY` covering localhost and service names.
   Workflow `env:` cannot override the proxy variables — the executor
   forces them.

Allowlist defaults cover GitHub (git smart-HTTP, API, `githubusercontent`
CDNs, `ghcr.io`) and the major package registries (npm, PyPI, Go module
proxy, Docker Hub, Maven, NuGet, RubyGems, crates.io, Gradle). Suffix
matching: `github.com` covers `*.github.com`, never `notgithub.com`.

Operator controls (runner env):

- `KERN_EGRESS_ALLOWLIST=a,b,c` — replaces the default list entirely.
- `KERN_RUNNER_EGRESS=off` — disables the restriction (escape hatch;
  documented, audited by being explicit).

Coverage is HTTP/S only. Tools that ignore proxy env vars (raw sockets,
`ssh`, `nc`, custom TCP protocols, `docker` client inside the job — which
has no socket anyway) cannot egress at all: fail closed. Image pulls and
`git clone` are unaffected — the daemon and the host-side checkout resolve
them before the container exists.

`pull_request` (same-repo, unpromoted) keeps full egress deliberately:
the code comes from a repo member's branch, matching GitHub's own trust
posture. Widening `fork`-style egress to it is a config decision, not a
code change — deferred until the fleet breakage data exists.

If proxy setup fails, the job fails with an `egress policy:` error — it
never falls back to open egress.

## Credentials

### Clone token

At claim time the control plane mints a GitHub App installation token
(`App.CloneToken`) used **only** by the runner's host-side `git clone` —
passed as an `http.extraHeader`, never written to `.git/config`, never
mounted into the container, masked in logs. It is the cached
installation token (repo checkout needs it for private repos; per-claim
minting would burn the installation's token-creation rate limit).

### `GITHUB_TOKEN`

For trust levels where `secretsEligibleTrust` holds (everything except
`fork` and `pull_request`), the claim response carries `github_token`:

- Minted per claim via `App.RunToken` — a repository-scoped installation
  token (`repositories: [name]`), ~1h expiry, never persisted.
- The workflow/job `permissions:` map is the **ceiling**: job-level map
  replaces workflow-level outright; the minted set is intersected with
  the installation's granted permissions and each level is clamped
  downward (`admin`→`write`→`read`). A workflow can narrow, never widen.
  A ceiling granting nothing the installation holds fails the mint; the
  job then runs without `GITHUB_TOKEN` (logged, not faked).
- No `permissions:` block → the token carries the installation's default
  grants for that repository (GitHub's permissive-default parity).
- `fork`/`pull_request` runs receive no token and no secrets —
  `jobSecrets` returns `blocked: true`.

The runner injects `GITHUB_TOKEN` through the same env-file path as
secrets, so shell steps, JS actions and docker-action containers all see
it. Both run tokens (clone + GitHub) join the runner-side log masker —
the server masker only knows stored secrets.

## Secret masking audit

Secrets are AES-256-GCM at rest, write-only via API after creation, and
masked wherever they could surface:

| Channel | Control |
| --- | --- |
| Step logs | server-side `secrets.Masker` at ingest (longest-first substring) **plus** runner-side `logMask` covering the per-run clone/`GITHUB_TOKEN` values the server cannot know |
| Step diagnosis JSON | `maskDiagnosis` recursively masks every string before `FinishStep` persists it (covers `detail`, `agent_prompt`, nested remediation) |
| Job error | masked with the job's masker before `FinishJob` persists it; surfaces in dashboard, notifications, MCP |
| Public run page | diagnosis trimmed to `kind`/`summary`/`suggestion` — `detail` never leaves the authenticated view |
| Notifications | built from masked `diagnosis->>summary` and masked `job.Error` |
| Check-run payloads to GitHub | workflow/job/step **names and statuses only** — no logs, errors or outputs |
| `audit_events` | records secret **names** on write/delete, never values |

**Known residual (GitHub parity, documented not fixed):** artifacts and
step/job `outputs` are job-authored *data*, not logs — they are not masked
(any byte-level masking would corrupt binaries, and GitHub itself does not
mask these channels either). A job that prints a secret to stdout is
covered; a job that writes it into an artifact or `GITHUB_OUTPUT` is not.
Artifact downloads require authentication; outputs feed `needs.*` within
the run. Treat secrets-in-artifacts as an operator concern, same as
GitHub.

Secret rotation is an ops procedure (create new → update consumers →
delete old); there is no auto-rotation.

## Ephemeral / JIT runners

`runners.ephemeral` (migration 0014 backfilled `false` for pre-existing
rows — the column defaulted `true` but was never written):

- `POST /api/v1/runners` (admin) mints a runner credential marked
  ephemeral — the JIT endpoint for external scalers
  (actions-runner-controller, custom autoscalers). Gin cannot route
  `/runners/jit` beside the `/runners/:id` wildcard; the collection POST
  is the endpoint. Name collisions return 409 rather than rotating an
  existing runner's credential (`Store.InsertRunner`, no upsert).
- `KERN_RUNNER_EPHEMERAL=1` (or `true`) puts the agent in JIT mode:
  `KERN_RUNNER_EPHEMERAL_IDLE` (default 2m) with zero active jobs sends
  `deregister: true` on the heartbeat; the control plane deletes the row
  (audited `runner.deregister`) only when no jobs remain, and the runner
  exits. The token dies with the row — it cannot rejoin.
- Stale ephemeral runners offline >24h are deleted by the store sweeper.

## Docker-build path — decision

`runs.using: docker` actions with `runs.image: Dockerfile` execute
`docker build` on the **host daemon** (`resolveDockerImage`,
`internal/runner/dockeraction.go`). A Dockerfile's RUN steps are
attacker-controlled code running through the host's BuildKit — a
host-root surface (build-time mounts, privileged build containers,
unrestricted egress, daemon CVE history).

**Decision: gated, not containerized.** `Dockerfile` builds now require
`trusted`/`protected` trust; `pull_request`, `fork` and `deployment` runs
fail the step with a diagnostic naming the trust requirement. Named
`runs.image` registry refs and `uses: docker://image` remain available to
all tiers — they run as hardened sibling containers (no build-time host
execution; the daemon only pulls).

Deferred alternatives, in order of increasing cost: rootless BuildKit or
Kaniko inside the job container for untrusted tiers; a dind sidecar only
for trusted projects. Neither is implemented — do not claim them.

## Sandbox evaluation — gVisor vs Firecracker (doc only)

Not integrated; recorded for the decision they would inform.

| | gVisor (`runsc`) | Firecracker |
| --- | --- | --- |
| Isolation | user-space kernel (Sentry) intercepts syscalls; same container UX | hardware-virtualized microVM per job; strongest boundary |
| Integration cost | `--runtime=runsc` on `docker run` — low; needs `runsc` + shim on every runner host | VM lifecycle orchestration (snapshot/restore, vsock, image staging) — high; effectively a second scheduler |
| Overhead | ~5–60% syscall-heavy workloads; network and memory cheaper than VM | ~125ms boot (snapshot restore), near-native CPU; RAM cost per VM |
| Fit | drop-in upgrade for the sandboxed tier; would harden `pull_request`/`deployment` against kernel exploits | the right answer for `fork` if container escapes become real; pairs naturally with the ephemeral-runner model (one VM per job, destroyed after) |
| Blockers | runsc must be installed on each runner host; some syscalls/filesystems unsupported (e.g. certain `io_uring`, some mounts) — needs a compatibility pass against the 13-repo fleet | no in-repo orchestration exists; KERN's docker-exec step model assumes a long-lived container — VM-per-step or VM-per-job both need design |

Recommendation if revisited: gVisor first for the sandboxed tiers
(cost/benefit), Firecracker only if a demonstrated escape or compliance
requirement justifies the orchestration. Neither changes the trust-tier
matrix above; they would deepen the sandbox column.

## What is NOT claimed

- No microVM or user-space-kernel isolation today — sandboxed jobs share
  the host kernel; container escape is bounded by caps/seccomp defaults,
  not eliminated.
- Egress allowlist covers HTTP/S through the proxy only; non-HTTP
  protocols are denied by topology, not filtered.
- `pull_request` egress is unrestricted (see above).
- Multi-tenant (multi-org) isolation is not audited here.
