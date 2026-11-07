# Deploying KERN

This is the checklist that produced the reference install (Proxmox CT, Docker
Compose, cloudflared tunnel, GitHub App). Adjust names and URLs to taste; the
order matters.

## Prerequisites

- A Linux host or container with Docker. On an unprivileged LXC the CT needs
  `nesting` and `keyctl` features enabled.
- A public ingress path for webhooks: a cloudflared named tunnel, a reverse
  proxy, or any stable URL that reaches the api on port 8080.
- The repo checked out somewhere persistent, e.g. `/opt/kern/src`.

## 1. Secrets file

Copy `.env.example` to `/opt/kern/.env` and fill in:

- `KERN_SESSION_SECRET` — 32+ random characters.
- `KERN_SECRET_KEY` — exactly 32 bytes (AES-256 for stored secrets).
- `KERN_RUNNER_JOIN_TOKEN` — random shared token the runner registers with.
- `KERN_PUBLIC_URL` — the public URL, e.g. `https://kern.example.com`. Commit
  statuses and check runs link back to runs at this URL. Those links open
  without a login only when the org setting `public_ci` is `on` (Settings →
  Public runs, default off).
- `KERN_BOOTSTRAP_EMAIL` / `KERN_BOOTSTRAP_PASSWORD` — first admin login.
- `KERN_GITHUB_APP_ID`, `KERN_GITHUB_APP_PRIVATE_KEY_FILE`,
  `KERN_GITHUB_WEBHOOK_SECRET` — see section 3.
- Optional: `KERN_RUNNER_CONCURRENCY` (jobs in parallel on this runner; unset
  means one worker per CPU, and `1` gives that single job every CPU unless
  `KERN_JOB_CPUS` is set), `KERN_RUNNER_LOAD_GATE` (pause claiming above n×CPU
  load), `KERN_JOB_CPUS` (unset shares the host equally across the workers),
  `KERN_JOB_MEMORY`, `KERN_LOG_RETENTION`, `KERN_CACHE_MAX_AGE`. A job opts
  into several runners with `strategy.split` (a count, or `auto` for the sum
  of online CPUs that match `runs-on`). Each shard is a full checkout.
- `KERN_GEOIP_URL` — egress-IP lookup for the dashboard fleet map (default
  `https://ipwho.is/{ip}`; ipapi.co-shaped endpoints also parse). The API
  resolves each runner's connecting
  IP plus its own egress once; both are coarse city-level fixes the operator
  can override on the runners page. Set to `off` to disable — lookups are the
  only traffic that sends runner IPs to a third party.

## 2. Bring the stack up

```sh
cd /opt/kern/src
docker compose -f deployments/docker-compose.yml --env-file /opt/kern/.env up -d --build
```

The compose file runs three services: `postgres`, `api` (serves the SPA and
the API on :8080), `runner` (executes jobs; mounts the host docker socket).

Important detail: job bind mounts are resolved by the *host* docker daemon,
so the runner's work root must be the same path inside and outside its
container. The compose file already mounts `${KERN_RUNNER_WORK:-/opt/kern/work}`
at `/opt/kern/work` for exactly this reason — keep the two paths identical.

Verify: `curl -s localhost:8080/healthz` and log in at the web UI with the
bootstrap admin.

Set `KERN_VERSION` to the release you are deploying (`v0.3.0`, not `dev`)
before the build. An unstamped binary reports `dev`, and automatic apply
refuses to compare that.

## 2a. Automatic updates

The API container cannot replace itself. Apply is a second process.

1. Settings → Automatic updates, or `kern settings set auto_update on`.
   The default is off. Turning it on does nothing until a release carries
   a signed `kern-update.json`.
2. Start the sidecar once: `docker compose -f deployments/docker-compose.yml --env-file /opt/kern/.env --profile update up -d`.
   It polls `GET /api/v1/update/plan` with the runner join token. When the
   plan says apply, it `git fetch`es the manifest's commit, fast-forwards
   `master` only, and runs `docker compose up -d --build --no-deps api runner`
   from `deployments/` with `KERN_VERSION` set to the release tag. A dirty
   checkout or a non-fast-forward is reported and not forced.
3. External runners (not the compose service) opt in with
   `KERN_RUNNER_UPDATE=1`. The heartbeat then offers
   `ghcr.io/dvorinka/kern-runner@sha256:…` to one idle runner at a time.
   That runner pulls the digest and replaces its own container. The compose
   runner leaves the variable unset; the sidecar rebuilds it.

The signature is ed25519. The public key is compiled into the API. The
private key is the GitHub secret `KERN_UPDATE_SIGNING_KEY`, used by
`release.yml` after the images are pushed. A release without that asset
stays informational: the settings page shows it and nothing is applied.
The first deploy of this mechanism is still the command above. Later
releases apply themselves only after that sidecar is running and the
running version is a real dotted triple.

## 3. GitHub App

The App is the event source and the status poster.

1. Create the App (GitHub → Developer settings → GitHub Apps → New): any name,
   homepage `https://kern.example.com`, webhook URL
   `https://kern.example.com/api/v1/webhooks/github`, webhook secret = the
   `KERN_GITHUB_WEBHOOK_SECRET` value, permissions: Contents read, Commit
   statuses read/write, Checks read/write, Pull requests read. Events: push,
   pull_request, check_run, check_suite. Checks write is what lets GitHub
   render the job table on the commit. Statuses still post if that permission
   is missing.
2. Download the private key; place it at the path in
   `KERN_GITHUB_APP_PRIVATE_KEY_FILE` and `chmod 0644` — the api container runs
   as a non-root user and must be able to read it. The compose file mounts
   `KERN_GITHUB_APP_KEY_FILE` to `/run/kern-app.pem`.
3. Install the App on the org/repos.
4. Restart the api (`docker compose ... up -d --build`) so the env takes effect.

Statuses then post as `<app-slug>[bot]` with context `kern/<workflow>`. The
browser-based manifest flow (`Connect GitHub` on the projects page) is an
alternative self-serve path for other users; it needs `KERN_PUBLIC_URL`
pointing at the public URL.

## 4. Ingress for webhooks

A cloudflared named tunnel works without opening ports:

```sh
cloudflared tunnel create kern
# config.yml: tunnel: <id>; credentials-file; ingress: hostname kern.example.com → http://localhost:8080; catch-all 404
cloudflared tunnel route dns kern kern.example.com
systemd unit: cloudflared tunnel run kern
```

The tunnel's URL must match both the App webhook URL and `KERN_PUBLIC_URL`.

## 5. Sync a project and dogfood

In the UI (or `kern` CLI): create a project bound to a GitHub repo (`owner/name`),
then Sync — the api reads the repo's `.github/workflows/*` via the App/PAT and
stores compiled versions. The next push fires a webhook delivery → a run → a
commit status on the pushed SHA.

## 6. Operations notes

- Redeploy: `cd /opt/kern/src && git pull && docker compose -f deployments/docker-compose.yml --env-file /opt/kern/.env up -d --build`.
- Runner envs live in `.env`; the runner re-registers on restart (upserts by
  org+name), so a new image does not need a fresh enroll token.
- `/api/v1/metrics` exposes Prometheus text format behind auth — scrape with
  `Authorization: Bearer <api token>` (a `read`-scoped token is enough).
  `/healthz`/`/readyz` stay open for probes.
- Retention: `KERN_LOG_RETENTION` sweeps step logs/summaries,
  `KERN_CACHE_MAX_AGE` evicts actions/cache entries,
  `KERN_WEBHOOK_RETENTION` prunes `webhook_deliveries` rows, artifacts expire after
  90 days, `KERN_RUNNER_IMAGE_MAX_AGE` GCs pulled job images on the runner.
- Postgres lives in the `kern-db` volume — back it up with `docker exec
  <pg> pg_dump -U kern kern`.
- The runner-side persistent cache (mounted into jobs at `/kern/cache`)
  lives under `KERN_RUNNER_WORK/_cache`; delete it freely to force cold runs.
- Fork-PR egress: `fork`-trust jobs run on a per-job `--internal` network
  whose only way out is a sibling allowlist proxy the runner builds itself
  (`kern-runner egress-proxy`, scratch image `kern-egress:local` under the
  work root). Defaults cover GitHub + major registries; tune with
  `KERN_EGRESS_ALLOWLIST` on the runner or disable with
  `KERN_RUNNER_EGRESS=off`. If the built-in proxy proves too narrow for
  your fleet, the operator alternative is an egress proxy of your choice:
  run it on the docker host, point `KERN_EGRESS_ALLOWLIST` at nothing and
  instead give fork jobs reachability by other means — today that means
  `KERN_RUNNER_EGRESS=off` plus your own network-level controls (a
  filtered egress gateway, host firewall, or a corporate proxy enforced
  by `HTTP_PROXY` at the docker-daemon/network layer). KERN does not yet
  support plugging an external proxy into the internal network; that is
  the documented gap, not a hidden one. See `docs/security-isolation.md`.
- JIT runners: `POST /api/v1/runners` (admin token) returns a ready
  `kern-runner --token … --control …` credential flagged ephemeral; the
  agent self-deregisters after `KERN_RUNNER_EPHEMERAL_IDLE` (default 2m)
  with no active jobs. Set `KERN_RUNNER_EPHEMERAL=1` on the spawned
  agent.
