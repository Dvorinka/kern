# Runner contract — job/service networking and port conventions

What a workflow can rely on. Everything here is verified behaviour, not
intention.

## Job networking

| Trust tier | Job container network | Meaning |
| --- | --- | --- |
| trusted / protected (same-repo pushes, PRs, dispatches) | `--network host` | Shares the runner host's netns. `localhost` in a step is the host's loopback; `docker run -p` inside steps publishes where the job can reach it (GitHub VM-runner parity). |
| pull_request (untrusted, same-repo PRs) | default docker bridge | No daemon socket, no host loopback. Read-only `GITHUB_TOKEN`; secrets withheld. |
| fork (untrusted fork PRs) | `--network kern-ego-<job>` | Internal-only bridge plus an allowlist egress proxy. No daemon socket, no inbound reach, read-only token, no secrets. |

`pull_request` runs against a fork gate / `pull_request_target` semantics
as documented in `docs/compatibility.md`.

## Services (`jobs.<id>.services`)

Two access models, same as GitHub's:

- **No `ports:`** — the service runs on a per-job bridge
  (`kern-svc-<job>`) where its service name is a DNS alias. Reach it as
  `servicename:containerport` (the container-job model).
- **With `ports:`** — the service shares the job container's netns, and
  its name also maps to `127.0.0.1` in the job's `/etc/hosts` (the
  VM-runner model). `localhost:<port>` works.
  - `host:container` remaps are honoured. On bridge jobs a loopback
    forwarder container (`kern-svc-<job>-<svc>-fwd-<hostport>`, built
    from the kern-egress image) binds the host port inside the shared
    netns and splices to the container port. On host-net jobs the
    mapping is a real `docker run -p 127.0.0.1:host:container` publish.
  - TCP only: `/udp` remaps warn and are skipped.
  - On host-net jobs the published ports are serialized per runner via
    an in-process lock — a second job wanting the same port waits, it
    does not fail.

### Health

`options:` accepts only docker `--health-*` flags
(`--health-cmd/--health-interval/--health-timeout/--health-retries/--health-start-period`);
anything else warns and is dropped. Steps wait for every service to
report `healthy` (or simply `running` when no healthcheck is defined),
bounded at 2 minutes — so `pg_isready`-style options are honoured and
manual probe loops are unnecessary.

`credentials:` authenticates the service image pull via a throwaway
`docker --config` login, deleted after pull. `volumes:` warns and is
ignored (host bind mounts would expose the runner filesystem to
untrusted runs).

## Kern-reserved host ports

Ports the control plane claims on its host netns — workflows should not
publish these on hosts that also run jobs:

| Port | Consumer |
| --- | --- |
| `8080` | control-plane api (compose `ports:`) |
| `55432` | control-plane postgres, operator access only (`psql -h 127.0.0.1 -p 55432`). The api reaches postgres internally; `:5432` is deliberately left free so job services can have it |

Everything else on the host's loopback/interface space is fair game for
`ports:` publishes on host-net jobs; orchestrated conflicts between
kern-managed jobs are serialized by the runner's port lock, and
non-kern containers squatting a port are an operator concern.

## Claim ordering

`jobs.<id>.priority` (integer, default `0`, KERN extension) — the
runner claim query orders by `priority DESC, queued_at`, so a queued job
with a higher number is claimed before lower-priority jobs. Use it to
keep short urgent work (e.g. this repo's dogfood CI) ahead of long
builds on a shared fleet. GitHub ignores the key.

## Per-job resources

`jobs.<id>.resources` (KERN extension) sets the job container's cgroup
limits without spelling env vars:

```yaml
jobs:
  build:
    resources:
      memory: 8g   # KERN_JOB_MEMORY — docker --memory
      cpus: 4      # KERN_JOB_CPUS — docker --cpus
      disk: 30g    # KERN_JOB_DISK — work-root budget; breach = disk_quota abort
```

An explicit `KERN_JOB_*` value in `env:` wins over the block. Limits only
apply on trusted tiers (trusted/protected runs); a fork pull_request job
declaring `resources:` keeps the runner defaults. GitHub ignores the key.

## Job container hardening (untrusted tiers)

uid 1000, `--cap-drop ALL`, `no-new-privileges`, `--read-only`, `/tmp` and
`/run` as small noexec tmpfs. No docker socket — `runs.image: Dockerfile`
builds and sock mounts are trusted-tier features. Steps run under
`bash -eo pipefail`.
