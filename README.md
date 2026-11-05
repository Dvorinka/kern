<p align="center">
  <img src="docs/brand/logo-dark-box.svg" width="128" alt="KERN logo">
</p>

# KERN

Distributed, self-hosted CI/CD platform that executes existing GitHub Actions
workflow YAML on your own infrastructure. GitHub stays the source-code provider;
KERN owns the control plane, scheduler, execution engine and storage.

Status: early milestones. The control plane, scheduler, runner agent and
workflow engine work end to end (a real workflow executes in an isolated
container on a registered runner). See `plan.md` for the milestone ledger and
`docs/compatibility.md` for the honest feature matrix.

## Components

| Path | What it is |
| --- | --- |
| `cmd/api` | Control plane: HTTP API, auth, scheduler, orchestrator, log ingestion |
| `cmd/runner` | Runner agent: registers, claims jobs, executes steps in ephemeral containers |
| `cmd/migrate` | Standalone migration runner (migrations also run at api boot) |
| `internal/` | Platform packages (see `docs/architecture.md`) |
| `apps/web` | Dashboard (React + Vite) — in progress |
| `migrations/` | Versioned PostgreSQL schema (goose) |
| `deployments/` | Docker Compose + api Dockerfile |

## Quickstart

Requires Go 1.26+, Docker, PostgreSQL 16, and `just`.

```sh
# 1. Postgres (any reachable instance; or use the compose file)
docker run -d --name kern-pg -p 5432:5432 \
  -e POSTGRES_USER=kern -e POSTGRES_PASSWORD=kern -e POSTGRES_DB=kern \
  postgres:16-alpine

# 2. Control plane (migrations + bootstrap admin run at boot)
just dev          # http://localhost:8080, admin@kern.local / kern-admin-password

# 3. Runner agent (any machine with Docker; outbound-only)
just runner       # registers, heartbeats, long-polls for jobs

# 4. Create a project pointing at a repo checkout, then sync + trigger
curl -b cookies -c cookies -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@kern.local","password":"kern-admin-password"}'
curl -b cookies -X POST localhost:8080/api/v1/projects \
  -H 'Content-Type: application/json' \
  -d '{"slug":"demo","local_path":"/path/to/repo"}'
curl -b cookies -X POST localhost:8080/api/v1/projects/demo/sync
curl -b cookies -X POST \
  localhost:8080/api/v1/projects/demo/workflows/<workflow-id>/runs
```

Environment variables are documented in `.env.example`. Production-style
deployment: `docker compose -f deployments/docker-compose.yml up -d` with the
required secrets set (see `.env.example`).

## Development

```sh
just build    # go build ./...
just test     # go test ./...
just vet      # go vet ./...
just migrate  # apply migrations only
```

Docs: `docs/architecture.md` (design + ADRs), `docs/compatibility.md`
(GitHub Actions feature matrix), `plan.md` (milestones), `project.md` (spec).
