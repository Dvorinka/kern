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
  statuses link back to runs at this URL.
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

## 3. GitHub App

The App is the event source and the status poster.

1. Create the App (GitHub → Developer settings → GitHub Apps → New): any name,
   homepage `https://kern.example.com`, webhook URL
   `https://kern.example.com/api/v1/webhooks/github`, webhook secret = the
   `KERN_GITHUB_WEBHOOK_SECRET` value, permissions: Contents read, Commit
   statuses read/write, Pull requests read. Events: push, pull_request,
   check_run, check_suite.
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
- `/api/v1/metrics` exposes Prometheus text format; `/healthz`/`/readyz` for
  probes.
- Retention: `KERN_LOG_RETENTION` sweeps step logs/summaries,
  `KERN_CACHE_MAX_AGE` evicts actions/cache entries, artifacts expire after
  90 days, `KERN_RUNNER_IMAGE_MAX_AGE` GCs pulled job images on the runner.
- Postgres lives in the `kern-db` volume — back it up with `docker exec
  <pg> pg_dump -U kern kern`.
- The runner-side persistent cache (mounted into jobs at `/kern/cache`)
  lives under `KERN_RUNNER_WORK/_cache`; delete it freely to force cold runs.
