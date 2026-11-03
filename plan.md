# KERN — Implementation Plan

Status: living document. Milestones are marked when they are verifiably done, not when code exists.

## 1. Scope of this plan

KERN is a distributed, self-hosted CI/CD control plane that executes existing GitHub Actions
workflows on the operator's own infrastructure. This plan covers the path from an empty
repository to the first end-to-end milestone (M0–M3) and sketches the later phases.

## 2. Stack decisions

| Layer | Choice | Notes |
| --- | --- | --- |
| Backend | Go 1.27, modular monolith, Gin, zap | `cmd/api`, `cmd/runner`; scheduler runs in-process until multi-instance control planes are tested |
| Database | PostgreSQL 16+, versioned SQL migrations (goose) | Source of truth for workflow/job state; transactional claiming |
| Query layer | pgx v5, hand-written typed store | Deviation from the skill default (sqlc): the query surface is still moving. ADR in `docs/architecture.md`; sqlc adoption deferred, not rejected |
| Queue | PostgreSQL only | `FOR UPDATE SKIP LOCKED`, leases, heartbeats. No in-memory authoritative queue |
| Execution | Docker, one container per job | no host socket, read-only root, dropped capabilities, resource limits |
| Blobs | `blobs.Store` interface; filesystem driver now, S3/MinIO driver next | Logs and artifacts are never stored in relational columns |
| Frontend | **React 18 + Vite + TypeScript strict + Tailwind + TanStack Query + Lucide** | React per user instruction, overriding the SolidJS line in `project.md` |
| API contract | OpenAPI 3.1 spec → generated TypeScript client (`openapi-typescript` + `openapi-fetch`) | No hand-duplicated API types |
| Auth | argon2id passwords, HMAC-signed HttpOnly session cookie, hashed API tokens | Better Auth (Node) cannot run inside a Go control plane; ADR required |
| Observability | zap structured logs, Prometheus text `/metrics`, OTel hooks | OTel collector is optional, not required to run |
| Task runner | `just` + Docker Compose | `just dev`, `just test`, `just migrate` |

## 3. Architecture

```
GitHub ──webhook──▶ api (Gin)
                      │ HMAC verify → persist delivery → dedupe
                      ▼
              workflow engine: parse → validate → model → DAG → plan
                      ▼
              PostgreSQL (runs, jobs, steps, leases, runners, blobs metadata)
                      ▼
              scheduler: single-statement claim + lease
                      ▲
      long-poll claim │ heartbeat │ log chunks │ result
                      │
              runner agent (separate host, outbound only)
                      ▼
              Docker executor: one ephemeral container per job
```

Invariants:

- YAML is never executed as parsed. The compiler produces a static plan; runners only ever see a `ClaimedJob`.
- The control plane never executes repository code.
- A runner failure never corrupts a run: expired leases are reclaimed and retried with a bounded attempt count.
- Untrusted (fork / `pull_request_target`) work never receives secrets or deployment credentials.

## 4. Data model

Tables: `orgs`, `users`, `org_members`, `sessions`, `api_tokens`, `projects`, `installations`,
`webhook_deliveries`, `workflows`, `workflow_versions`, `workflow_runs`, `jobs`, `steps`,
`runners`, `job_leases`, `artifacts`, `cache_entries`, `secrets`, `environments`, `deployments`,
`releases`, `audit_events`.

Key choices: UUID keys; `run_number` unique per workflow; immutable `workflow_versions` keyed by
commit SHA; `jobs.status` constrained; logs/artifacts addressed by `blob_ref`; `lease_expires_at`
is the only authority on runner liveness.

## 5. Execution contracts

```
ParsedWorkflow  -> CompiledWorkflow{jobs, edges} -> RunPlan
JobSpec{key,name,needs,runsOn,image,env,steps,matrix,timeout,continueOnError,if}
ClaimedJob{jobID,runID,attempt,leaseID,spec,source{cloneURL,ref,sha},secrets,maskedValues}
LogChunk{jobID,stepKey,seq,ts,data}
JobResult{conclusion,exitCode,outputs,stepResults,error}
```

## 6. GitHub Actions compatibility scope

See `docs/compatibility.md` for the maintained matrix. v1 implements: triggers `push`,
`pull_request`, `workflow_dispatch`, `schedule` (model only), `repository_dispatch`; `needs`,
outputs, `env`, `secrets`, `vars`, matrices with include/exclude/fail-fast, `if`/expressions,
`continue-on-error`, `timeout-minutes`, `concurrency`, `run` steps, checkout, job containers
later, step and job outputs, file commands (`GITHUB_ENV`, `GITHUB_OUTPUT`, `GITHUB_PATH`,
`GITHUB_STEP_SUMMARY`), annotations, masking.

Not yet: composite/JS/Docker actions, reusable workflows, services, OIDC, environment approvals,
artifact/cache actions. Each unsupported construct produces an explicit diagnostic; it is never
silently ignored.

## 7. Milestones

| # | Scope | Acceptance |
| --- | --- | --- |
| M0 | Monorepo, compose, migrations, config, logging, health, auth, API skeleton, React shell | stack boots, login works, migrations up/down, `just test` green |
| M1 | Workflow model, parser, validator, expression evaluator, compiler, DAG | table-driven tests, hostile/malformed corpus, expression conformance |
| M2 | Scheduler + leases + reaper, runner agent, Docker executor, log ingestion, cancel, retry | a real workflow runs end to end on a real runner |
| M3 | React dashboard: overview, runs, run detail with DAG and live logs, runners | no mock data; SSE logs stream live |
| M4 | GitHub App: install, webhooks, discovery, checks, summaries, rerun/cancel | requires operator credentials; documented Checks API limits |
| M5 | Actions (composite/JS/Docker), reusable workflows, services, artifacts, caches, environments | compatibility suite green |
| M6 | Distributed cache, BuildKit, warm pools, cache-aware placement | measured queue-wait and cache-hit improvements |
| M7 | Registry, releases, packages, deployments, approvals, rollback | idempotent publish retries proven |
| M8 | Hardening, backup/restore, docs, upgrade path, security audit | operational runbooks + scorecard |

## 8. Risks

1. Action runtime depth (JS action toolkit shims, Docker actions) — highest cost, deferred to M5 with diagnostics.
2. Container isolation correctness — hostile-workflow tests gate the executor.
3. Secret leakage through logs — masking applied at ingestion, server side.
4. Scheduler starvation under concurrency groups — measured queue wait from M2.
5. GitHub API limits: external checks cannot create native Actions runs or step logs, installation tokens expire hourly, webhook retries are unordered.

## 9. Decisions taken

- Module path `github.com/dvorinka/kern`; repo stays local, no remote configured.
- React dashboard (user instruction) instead of SolidJS.
- Filesystem blob driver first, S3/MinIO driver behind the same interface next.
- Single-tenant default org bootstrapped from `KERN_BOOTSTRAP_EMAIL`/`KERN_BOOTSTRAP_PASSWORD`.
- The scheduler loop lives in `cmd/api` for M0–M3 and moves to `cmd/scheduler` when multi-instance behaviour is tested.
