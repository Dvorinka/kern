# KERN — Distributed CI/CD Platform
## Product specification and implementation instructions

You are the principal software architect and autonomous full-stack engineer responsible for designing and building **KERN**, a modern, distributed, self-hosted CI/CD platform designed to replace GitHub Actions as the workflow execution infrastructure.

Your task is to build a production-quality product, not a prototype, mockup, landing page or superficial dashboard.

The platform must use the user's own infrastructure to execute workflows, build applications, run tests, publish artifacts, manage releases and deploy applications. GitHub remains the primary source-code provider and developer interface.

The long-term goal is to provide an experience comparable to or better than GitHub Actions, with substantially more control over execution, performance, resource allocation, caching and infrastructure.

---

# 1. FIRST: DEVELOPMENT ENVIRONMENT AND SKILLS

Before making changes:

1. Inspect the entire existing repository.
2. Identify the current architecture, dependencies, conventions, tooling and available infrastructure.
3. Read and follow the `tdvorak-fullstack` skill if it is installed and available in your agent environment. Its instructions are mandatory for frontend architecture, backend conventions, UI quality, code quality and implementation practices.
4. Do not invent skill instructions if the skill is unavailable.
5. Inspect existing documentation and configuration before introducing alternatives.
6. Preserve existing working functionality unless there is a clear technical reason to change it.
7. Identify reusable existing code and infrastructure.

Do not blindly initialize a new project if one already exists.

If the repository is empty, establish a clean monorepo architecture.

Work autonomously. Make sensible engineering decisions without repeatedly asking for approval for routine implementation details.

Do not push to any remote Git repository. Local commits are permitted when a meaningful implementation milestone is completed.

Maintain documentation and implementation progress throughout the project.

# 2. PRODUCT VISION

KERN is a GitHub Actions-compatible, distributed CI/CD execution platform.

Its primary characteristics:

- GitHub remains the source of truth for repositories and pull requests.
- KERN receives GitHub events through a GitHub App.
- KERN interprets existing GitHub Actions workflow YAML.
- KERN compiles workflows into its own internal execution graph.
- KERN schedules jobs across distributed self-hosted runners.
- Runners execute isolated workloads on the user's infrastructure.
- KERN streams logs and execution information to its dashboard.
- KERN publishes check results back to GitHub.
- KERN manages artifacts, packages, images, releases and deployments.
- KERN exposes a comprehensive API for developers and AI agents.

Existing repositories should ideally work without modifying their `.github/workflows/*.yml` files.

KERN must be independent of GitHub internally. GitHub is the first-class provider, but the architecture must allow GitLab, Forgejo and other providers to be added later.

**Important:** KERN is not a GitHub Actions runner dashboard. It is an independent CI/CD platform with its own control plane, scheduler, execution engine and storage.

# 3. TECHNOLOGY STACK

Use the following stack unless the existing repository has a compelling, documented reason to differ.

## Backend

- Go as the primary backend language.
- Prefer a modular monolith for the initial control plane.
- Use independently deployable worker and runner processes where appropriate.
- REST APIs with OpenAPI documentation.
- Structured logging with zap or an equivalent mature Go logging library.
- Explicit dependency injection and clean module boundaries.
- Strongly typed internal execution contracts.
- Context-aware operations and graceful shutdown.

Avoid premature microservices.

The architecture must support future horizontal scaling without requiring an early migration to microservices.

## Frontend

- SolidJS.
- Vite.
- TypeScript.
- Tailwind CSS.
- Ark UI where appropriate.
- Lucide icons.
- Modern responsive application layout.

Avoid generic AI-generated dashboard design.

The interface should resemble a polished developer infrastructure product, with the visual quality and interaction standards of Linear, Vercel and modern GitHub.

No emoji-based interface.

## Database and infrastructure

- PostgreSQL: durable application state and workflow metadata.
- Docker and containerd: execution infrastructure.
- BuildKit: container builds and layer caching.
- S3-compatible object storage: artifacts, logs and build outputs.
- MinIO as a self-hosted storage option.
- OpenTelemetry: observability.
- Redis-compatible caching or PostgreSQL-backed coordination where appropriate.
- NATS JetStream may be introduced for durable distributed event delivery when justified.

Do not use an in-memory queue as the authoritative job queue.

PostgreSQL should initially be the source of truth for workflow and job state.

Use transactional job claiming, leases, heartbeats and idempotent operations.

## Language policy

Go is the default for all backend components.

Do not introduce Rust simply because it might be faster. Rust may be considered for future specialized components if benchmarks demonstrate meaningful advantages.

# 4. SYSTEM ARCHITECTURE

Build four principal architectural layers.

## A. Provider integration

Responsibilities:

- GitHub App authentication.
- Webhook processing.
- Repository installation management.
- Commit and pull request events.
- Workflow discovery.
- Check suite and check run management.
- Status reporting.
- Release management.
- GitHub Packages and GHCR integration.
- Deployment reporting.
- Future provider abstraction.

Webhook processing must be authenticated, idempotent and resilient to retries.

Persist incoming events before processing them.

Implement webhook delivery deduplication.

## B. Control plane

The control plane coordinates all platform operations.

Modules:

- API gateway.
- Authentication and authorization.
- GitHub integration.
- Workflow parser.
- Workflow compiler.
- Expression evaluator.
- Action resolver.
- Dependency graph.
- Scheduler.
- Execution orchestrator.
- Secrets management.
- Cache management.
- Artifact management.
- Deployment orchestration.
- Notifications.
- Metrics and analytics.

The control plane must not execute arbitrary repository code itself.

It must be possible to restart or upgrade control-plane components without losing active job state.

## C. Execution plane

Distributed runner infrastructure.

Implement a lightweight Go-based runner agent.

Every runner must:

- Register with KERN.
- Authenticate securely.
- Report machine capabilities.
- Report CPU, RAM, disk and architecture.
- Send regular heartbeats.
- Receive compatible jobs.
- Download required source and dependencies.
- Execute jobs in isolated environments.
- Stream logs.
- Upload artifacts.
- Report exit codes and job results.
- Support cancellation.
- Recover from temporary network interruptions.
- Clean up execution environments.

Support multiple concurrent workers per physical server, with configurable resource limits.

Use a pull-based runner communication model with outbound connections to the control plane. Do not require public inbound access to worker machines.

## D. Storage and observability

Implement:

- Durable workflow metadata.
- Job and step history.
- Execution logs.
- Artifact storage.
- Cache metadata.
- Build outputs.
- Deployment history.
- Runner history.
- Metrics and traces.

Large logs and artifacts must not be stored directly in PostgreSQL.

Use object storage and references in the database.

---

# 5. GITHUB ACTIONS COMPATIBILITY

This is one of the highest-priority requirements.

The platform should execute existing GitHub Actions workflows without requiring users to rewrite them.

Investigate the following projects and technologies before implementing the interpreter:

- nektos/act.
- GitHub Actions runner source code.
- GitHub Actions toolkit.
- actionlint.
- GitHub workflow syntax documentation.
- GitHub Actions expressions and context documentation.
- Forgejo Runner.

Use existing open-source components where licensing and architecture permit.

Do not blindly adopt an entire existing product as the execution engine.

KERN should own its internal workflow representation, execution contracts, scheduler and orchestration.

## Workflow features

Implement compatibility for:

- YAML workflow parsing.
- Workflow names.
- Event triggers.
- push.
- pull_request.
- pull_request_target, with strict security handling.
- release.
- workflow_dispatch.
- workflow_call.
- schedule.
- repository_dispatch.
- workflow_run.
- Job dependencies through needs.
- Job outputs.
- Step outputs.
- Environment variables.
- Secrets.
- Variables.
- GitHub contexts.
- Expressions.
- Conditional execution.
- Matrices.
- Fail-fast.
- continue-on-error.
- Timeouts.
- Concurrency groups.
- Job cancellation.
- Reusable workflows.
- Composite actions.
- JavaScript actions.
- Docker actions.
- Shell execution.
- Job containers.
- Service containers.
- Artifact upload and download.
- Cache operations.
- Environment protection and approvals.
- OIDC authentication.
- Permissions.
- Annotations.

Implement a compatibility matrix and track the status of every feature.

Never claim full compatibility merely because a basic workflow executes successfully.

For each unsupported feature, provide a clear explanation and an actionable diagnostic.

## Action execution

Support:

- Actions pinned to commit SHAs.
- Version and tag references.
- Marketplace actions.
- Local actions.
- Composite actions.
- JavaScript actions.
- Docker-based actions.

Resolve action dependencies securely and cache downloaded action repositories.

Do not silently execute mutable action references without making their security implications visible.

Implement the GitHub Actions-compatible environment variables and file-command mechanisms required by actions.

Support relevant GitHub Actions toolkit functionality, including outputs, summaries, annotations and environment updates.

## Internal workflow representation

Never directly execute YAML instructions as they are parsed.

Use a pipeline:

1. Parse YAML.
2. Validate syntax.
3. Resolve triggers and expressions.
4. Construct the workflow model.
5. Build the dependency DAG.
6. Resolve actions and dependencies.
7. Determine execution requirements.
8. Generate scheduler tasks.
9. Execute jobs.
10. Aggregate results.
11. Report results to GitHub.

This architecture must make it possible to optimize the execution plan before jobs start.

---

# 6. DISTRIBUTED SCHEDULER

The scheduler is a core differentiator.

It must distribute jobs across available runners based on:

- Available CPU.
- Available RAM.
- Disk space.
- Operating system.
- CPU architecture.
- Runner labels.
- Installed dependencies.
- Docker and BuildKit availability.
- Cached dependencies.
- Runner health.
- Current queue length.
- Job priority.
- Required execution capabilities.

Implement:

- Job queues.
- Priority scheduling.
- Resource-aware placement.
- Runner leases.
- Heartbeats.
- Job timeouts.
- Cancellation.
- Retry policies.
- Runner draining.
- Graceful shutdown.
- Failed runner detection.
- Stale job recovery.
- Job reassignment.
- Concurrency controls.

Never assume that a job is safe to execute twice.

Design retry semantics carefully, including idempotency and handling of partially completed publishing or deployment operations.

A runner failure must not corrupt the entire workflow.

## Runner labels

Support familiar labels such as:

- ubuntu-latest.
- ubuntu-24.04.
- linux-x64.
- linux-arm64.
- docker.
- android.
- go.
- node.
- python.
- high-memory.
- high-cpu.

Support custom labels.

Map GitHub runner labels to available KERN capabilities.

Initially prioritize Linux x86_64 and ARM64.

Keep the executor interface extensible for Windows and macOS in the future.

---

# 7. EXECUTION SECURITY

Security is non-negotiable.

KERN will execute potentially untrusted code from repositories and pull requests.

Implement strong workload isolation.

## Execution environments

Support ephemeral isolated environments using:

- Docker containers.
- containerd.
- Restricted container configurations.
- Optional VM-based isolation for higher-risk workloads.

Evaluate Kata Containers, Firecracker or equivalent technologies where useful.

Do not require Kubernetes.

Never expose the host Docker socket to untrusted jobs.

Do not execute arbitrary repository code directly on the runner host.

Use resource limits, filesystem isolation, namespaces, seccomp and appropriate Linux security mechanisms.

## Trust levels

Distinguish:

1. Trusted internal builds.
2. Builds from protected branches.
3. External pull requests.
4. Forked repositories.
5. Privileged deployment workflows.

Untrusted pull requests must never receive repository secrets, privileged credentials or deployment permissions.

Implement explicit trust policies.

## Secrets

Implement:

- Encrypted storage.
- Repository-level secrets.
- Organization-level secrets.
- Environment-level secrets.
- Masking in logs.
- Restricted secret access.
- Short-lived credentials where possible.
- Secure runner delivery.
- Rotation support.

Never store secrets in plaintext.

Never expose secrets in logs, API responses, job metadata or frontend state unnecessarily.

## Authentication

Use GitHub App installation tokens for GitHub operations.

Use minimal permissions.

Implement runner authentication using securely provisioned identities and short-lived credentials.

Use mTLS or equivalent strong authenticated transport for sensitive runner communications.

---

# 8. PERFORMANCE AND CACHING

Performance is a primary product feature.

KERN should outperform conventional hosted CI execution wherever its infrastructure and workload permit.

Implement performance optimization as a measurable engineering objective, not a marketing claim.

## Warm runners

Maintain available execution capacity.

Avoid repeatedly provisioning environments and downloading dependencies when unnecessary.

Allow configurable warm pools.

## Distributed cache

Implement a shared content-addressed cache with local SSD caches on workers.

Support:

- Docker layer caching.
- BuildKit cache exports and imports.
- Go module caching.
- Node package caching.
- Python package caching.
- Rust crate caching.
- General dependency caches.
- Workflow cache actions.

Implement cache keys, versioning, expiration and eviction.

Separate caches by appropriate trust boundaries.

Untrusted jobs must not be able to poison caches consumed by trusted builds.

## Intelligent optimization

Implement:

- Parallel job execution.
- Dependency-aware scheduling.
- Cache-aware runner placement.
- Incremental Docker builds.
- Avoid unnecessary environment setup.
- Efficient source checkout.
- Artifact deduplication.
- Reuse of immutable action dependencies.

Add metrics measuring:

- Queue wait time.
- Environment startup time.
- Checkout time.
- Cache hit rate.
- Dependency download time.
- Execution time.
- Total workflow duration.

Use these measurements to guide optimization.

---

# 9. ARTIFACTS, REGISTRIES AND RELEASES

KERN must be a complete build and release platform.

Support both GitHub-hosted publishing and independent storage.

## Artifact management

- Workflow artifacts.
- Build outputs.
- Binary releases.
- Downloadable archives.
- Retention policies.
- Artifact metadata.
- Checksums.
- Artifact provenance.
- Secure download links.
- Artifact inspection.

## Container images

Integrate BuildKit.

Support:

- Docker builds.
- Buildx-compatible workflows.
- Multi-stage builds.
- Multi-platform images.
- Layer caching.
- OCI image formats.
- Image tagging.
- Image manifests.
- Image publishing.
- Build attestations where feasible.

Support publishing to:

- GitHub Container Registry.
- Other OCI-compatible registries.
- An optional built-in registry.

## Packages

Design support for:

- NPM.
- Go modules.
- Maven.
- Generic package artifacts.
- OCI packages.
- Other registry-compatible formats.

## Releases

Support:

- GitHub Releases.
- Release creation.
- Version tagging.
- Release assets.
- Changelogs.
- Draft and prerelease handling.
- Release history.

Make publishing operations retry-safe.

---

# 10. DEPLOYMENT ENGINE

Deployment capabilities must be integrated into KERN.

Support:

- SSH deployments.
- Docker deployments.
- Docker Compose.
- Container image deployment.
- Kubernetes deployments.
- HTTP webhooks.
- Remote scripts.
- Environment promotion.
- Deployment approvals.
- Rollback operations.
- Deployment history.
- Health checks.
- Deployment notifications.

Implement environments such as:

- Development.
- Staging.
- Production.

Support protected environments and approval requirements.

Store deployment metadata and associate deployments with Git commits and releases.

Never expose deployment credentials to arbitrary pull-request builds.

Deployments should be observable and traceable.

---

# 11. GITHUB INTEGRATION

GitHub is the first supported provider.

Implement a GitHub App with configurable repository installation.

Use:

- GitHub webhooks.
- Checks API.
- Commit Status API where appropriate.
- Pull Requests API.
- Contents API.
- Releases API.
- GitHub Packages and GHCR.
- Deployments API where applicable.

## Pull request experience

When a workflow executes, report its status to GitHub.

Create meaningful check runs for individual jobs or workflow stages.

Display:

- Build success.
- Build failure.
- Test results.
- Docker build results.
- Security scan results.
- Deployment results.

Provide check summaries and annotations.

Include an external URL linking to the corresponding KERN execution.

## Important GitHub limitation

KERN is an independent external CI engine.

Its executions must not be misrepresented as native GitHub Actions workflow runs.

External checks can appear in GitHub's commit and pull-request checks, but KERN cannot independently insert genuine workflow execution history or native step logs into the GitHub Actions tab through the Checks API.

Provide excellent external navigation instead.

## Re-run and cancellation

Support rerunning failed jobs and workflows.

Support cancellation from KERN.

Provide a secure mechanism for check-related user actions where possible.

Do not rely on unsupported GitHub API behavior.

---

# 12. FRONTEND AND USER EXPERIENCE

Build a genuinely polished developer application.

The dashboard is a major part of the product, not an afterthought.

Follow the `tdvorak-fullstack` skill and inspect its design conventions.

## Design direction

- Modern SaaS developer infrastructure.
- Clean, sharp, restrained visual language.
- Strong typography.
- Excellent spacing.
- Dense but readable data presentation.
- Thoughtful light and dark themes.
- Responsive layouts.
- No generic AI-generated dashboard aesthetic.
- No excessive gradients.
- No decorative cards without purpose.
- No emoji icons.

Use Lucide icons.

The application should feel closer to Linear or Vercel than Jenkins.

## Main navigation

Implement:

1. Overview.
2. Projects / Repositories.
3. Workflows.
4. Runs.
5. Jobs.
6. Runners.
7. Artifacts.
8. Container images.
9. Releases.
10. Deployments.
11. Cache.
12. Secrets.
13. Organizations.
14. Settings.
15. Infrastructure and analytics.

## Dashboard overview

Display:

- Active executions.
- Queued jobs.
- Recent workflow runs.
- Failed builds.
- Successful builds.
- Runner availability.
- Total execution time.
- Resource utilization.
- Cache hit rate.
- Build throughput.

Make the information useful and actionable.

## Workflow details

Display:

- Workflow metadata.
- Commit information.
- Branch.
- Pull request.
- Trigger.
- Execution status.
- Job dependency graph.
- Execution timeline.
- Individual job durations.
- Step-level details.
- Artifacts.
- Annotations.
- Failure information.
- Re-run controls.

## Live logs

Implement real-time log streaming.

Logs must:

- Appear as execution progresses.
- Be associated with individual steps.
- Support search.
- Support copying and downloading.
- Preserve timestamps.
- Handle ANSI output appropriately.
- Support large logs without loading everything into browser memory.
- Remain accessible after executions finish.

Use WebSockets or an appropriate streaming mechanism.

## Runner management

Display:

- Runner identity.
- Hostname.
- Status.
- Operating system.
- Architecture.
- CPU.
- RAM.
- Disk.
- Labels.
- Current jobs.
- Last heartbeat.
- Recent failures.
- Agent version.

Allow administrative runner management and configuration.

## Artifact and deployment interfaces

Provide usable artifact browsing, download links, registry visibility, release history and deployment inspection.

Avoid mock data in production interfaces.

All visible metrics must come from real backend data.

---

# 13. API AND AI AGENT INTEGRATION

KERN must be API-first.

All major dashboard operations should be possible through documented APIs.

Implement OpenAPI specifications.

Provide APIs for:

- Projects.
- Repositories.
- Workflows.
- Runs.
- Jobs.
- Steps.
- Logs.
- Artifacts.
- Runners.
- Deployments.
- Secrets administration.
- Cache inspection.
- Re-run.
- Cancellation.
- Metrics.

Design a future MCP server.

AI agents should be able to:

- Inspect workflow runs.
- Check build status.
- Retrieve failed job logs.
- Identify failed steps.
- Read test results.
- Trigger reruns.
- Cancel executions.
- Download artifacts.
- Inspect deployments.
- Retrieve runner status.

Provide appropriate authentication, permissions and audit logs.

An AI agent must be able to discover why a build failed without manually opening the KERN dashboard.

---

# 14. DATABASE AND DATA MODEL

Design a normalized PostgreSQL schema.

At minimum, model:

- Users.
- Organizations.
- Organization memberships.
- Projects.
- Repository installations.
- GitHub webhook deliveries.
- Workflows.
- Workflow versions.
- Workflow runs.
- Jobs.
- Steps.
- Runner registrations.
- Runner capabilities.
- Runner heartbeats.
- Job leases.
- Artifacts.
- Artifact metadata.
- Cache entries.
- Secrets.
- Environments.
- Deployments.
- Releases.
- Notifications.
- Audit events.

Use migrations.

Implement foreign keys, indexes, appropriate constraints and transaction boundaries.

Execution history must be durable.

Avoid storing large binary content or unbounded logs in relational columns.

Document important state transitions.

---

# 15. RECOMMENDED REPOSITORY STRUCTURE

Use a Go monorepo with logical module boundaries.

Adapt to the existing repository if necessary.

```text
kern/
├── apps/
│   └── web/
│
├── cmd/
│   ├── api/
│   ├── scheduler/
│   ├── orchestrator/
│   └── runner/
│
├── internal/
│   ├── auth/
│   ├── github/
│   ├── providers/
│   ├── workflows/
│   ├── expressions/
│   ├── actions/
│   ├── scheduler/
│   ├── execution/
│   ├── runners/
│   ├── artifacts/
│   ├── registry/
│   ├── deployments/
│   ├── cache/
│   ├── secrets/
│   ├── notifications/
│   └── telemetry/
│
├── packages/
│   ├── protocol/
│   ├── workflow-model/
│   └── sdk/
│
├── migrations/
├── deployments/
│   ├── docker/
│   └── examples/
│
├── docs/
├── scripts/
├── tests/
│   ├── integration/
│   ├── compatibility/
│   ├── security/
│   └── performance/
│
├── docker-compose.yml
├── Makefile
└── README.md
```

Do not create unnecessary abstractions or empty packages simply to match this tree.

---

# 16. TESTING AND QUALITY

Quality is mandatory.

Implement:

- Go unit tests.
- Frontend component tests.
- API integration tests.
- PostgreSQL integration tests.
- Runner integration tests.
- Workflow compatibility tests.
- GitHub webhook tests.
- Scheduler tests.
- Cache tests.
- Artifact tests.
- Security tests.
- Failure recovery tests.
- Performance benchmarks.

Build a compatibility test corpus with realistic workflows.

Test:

- Simple Go projects.
- Node projects.
- Python projects.
- Docker image builds.
- Matrix builds.
- Reusable workflows.
- Composite actions.
- JavaScript actions.
- Docker actions.
- Services.
- Caching.
- Artifacts.
- Cancellation.
- Failed runner recovery.
- Parallel workflows.

Include tests for hostile or malformed workflows.

Test the behavior of untrusted pull requests explicitly.

Do not mark a feature complete merely because its API endpoint exists.

A feature is complete only when the full workflow works end to end.

---

# 17. IMPLEMENTATION ROADMAP

Do not attempt to implement the entire platform in one enormous unstructured change.

Work incrementally, preserving a functioning system at every milestone.

## Phase 0 — Repository assessment

- Inspect repository.
- Read the required skill.
- Document existing architecture.
- Identify dependencies.
- Identify implementation risks.
- Produce initial technical architecture.
- Create a development roadmap.

## Phase 1 — Platform foundation

- Initialize or refine monorepo.
- Establish Go modules.
- Establish SolidJS frontend.
- PostgreSQL.
- Migrations.
- Authentication.
- API structure.
- Basic dashboard.
- Docker development environment.
- OpenAPI generation.

## Phase 2 — Runner infrastructure

- Runner agent.
- Registration.
- Heartbeats.
- Capabilities.
- Job leases.
- Job claiming.
- Docker executor.
- Live logs.
- Artifact upload.
- Runner management UI.

At the end of this phase, demonstrate a job running on a separate machine.

## Phase 3 — Workflow engine

- YAML parsing.
- Validation.
- Internal workflow representation.
- Dependency DAG.
- Expression evaluation.
- Triggers.
- Basic jobs and steps.
- Shell execution.
- Environment variables.
- Job dependencies.
- Matrix support.

## Phase 4 — GitHub integration

- GitHub App.
- Installation flow.
- Webhooks.
- Repository discovery.
- Workflow discovery.
- Commit checks.
- PR checks.
- Check summaries.
- External log links.
- Cancellation and rerun.

Demonstrate a real GitHub repository executing a workflow on self-hosted KERN infrastructure.

## Phase 5 — Compatibility expansion

- Composite actions.
- JavaScript actions.
- Docker actions.
- Reusable workflows.
- Secrets.
- Permissions.
- Services.
- Artifacts.
- Caching.
- Environments.
- OIDC.
- Compatibility suite.

## Phase 6 — Performance

- Shared content-addressed cache.
- Local SSD cache.
- BuildKit integration.
- Warm runner pools.
- Resource-aware scheduling.
- Parallel execution.
- Execution metrics.
- Performance benchmarks.

## Phase 7 — Publishing and deployments

- Artifact management.
- OCI registry.
- GHCR.
- GitHub Releases.
- Package publishing.
- Docker deployments.
- SSH deployments.
- Kubernetes integration.
- Rollbacks.
- Environment approvals.

## Phase 8 — Production readiness

- Security audit.
- Failure recovery.
- Backup and restoration.
- Monitoring.
- Alerting.
- Upgrade process.
- Installation documentation.
- Operational documentation.
- Compatibility reporting.
- Performance testing.

Do not skip earlier phases to build impressive-looking later features.

---

# 18. DEPLOYMENT AND SELF-HOSTING

The product must be straightforward to deploy.

Provide:

- Docker Compose deployment.
- Separate control-plane and runner configurations.
- Environment variable documentation.
- Database migrations.
- Health endpoints.
- Readiness endpoints.
- Backup instructions.
- Upgrade instructions.
- Runner installation instructions.

The control plane must support multiple instances.

Runners must be independently deployable on different servers.

No Kubernetes requirement.

The architecture should be suitable for Proxmox, Linux servers and Docker-based infrastructure.

Do not assume all machines share a filesystem.

All distributed communication must use explicit APIs and secure transport.

---

# 19. DOCUMENTATION

Maintain documentation as the implementation progresses.

Required documents:

- README.
- Architecture overview.
- Technical decisions.
- Database architecture.
- API documentation.
- Workflow compatibility matrix.
- Runner installation.
- Runner security.
- GitHub App installation.
- Artifact storage.
- Cache architecture.
- Deployment guide.
- Backup and recovery.
- Development guide.
- Troubleshooting.

Maintain a clear list of completed, partially implemented and unsupported features.

Do not describe unfinished functionality as implemented.

---

# 20. ENGINEERING PRINCIPLES

These principles are mandatory:

1. Build real functionality, not placeholder screens.
2. Prioritize a working end-to-end execution pipeline.
3. Prefer mature open-source components over unnecessary reinvention.
4. Maintain ownership of the workflow engine and scheduler.
5. Keep execution isolated from the control plane.
6. Design for distributed failures.
7. Treat security as a foundational requirement.
8. Make performance measurable.
9. Avoid premature microservices.
10. Avoid unnecessary dependencies.
11. Maintain strong typing and clear interfaces.
12. Document significant architectural decisions.
13. Keep APIs consistent and documented.
14. Make every major operation observable.
15. Never silently fake successful builds, artifacts or deployments.
16. Do not use mock data to disguise missing backend functionality.
17. Keep the frontend polished and consistent.
18. Follow the `tdvorak-fullstack` skill wherever available.
19. Commit meaningful completed milestones locally, but never push without explicit permission.
20. Do not sacrifice correctness or security to claim full GitHub Actions compatibility.

---

# 21. DEFINITION OF SUCCESS

KERN's first meaningful production milestone is achieved when the following complete scenario works:

1. An administrator deploys KERN's control plane.
2. The administrator installs a KERN GitHub App on a repository.
3. The repository contains an existing GitHub Actions workflow.
4. A developer pushes a commit.
5. GitHub sends an event to KERN.
6. KERN discovers and parses the workflow.
7. KERN compiles the workflow into an execution graph.
8. The scheduler identifies a suitable distributed runner.
9. The runner claims the job.
10. The runner creates an isolated execution environment.
11. The repository is checked out.
12. Existing GitHub Actions steps execute.
13. Logs stream into the KERN dashboard.
14. Tests and builds execute successfully.
15. Docker images can be built and published.
16. Artifacts are uploaded to storage.
17. KERN records the execution history.
18. GitHub receives the final check status.
19. The pull request displays a successful external check.
20. The check links to the corresponding KERN execution.
21. An AI coding agent can retrieve the run status and logs through the API.

This must work using actual infrastructure, real GitHub events, real workflow files and real execution results.

---

# 22. YOUR IMMEDIATE TASK

Start by assessing the existing repository and the available development environment.

Read the `tdvorak-fullstack` skill if available.

Then:

1. Write an architecture assessment.
2. Identify reusable existing components.
3. Identify the highest-risk engineering challenges.
4. Evaluate the feasibility of reusing `nektos/act` components.
5. Propose the exact Go libraries and runtime components.
6. Define the internal workflow and job execution contracts.
7. Define the database and scheduler architecture.
8. Produce an implementation roadmap with measurable milestones.
9. Identify any GitHub API limitations.
10. Begin implementation with the first foundational milestone.

Do not spend excessive time producing documentation without implementing anything.

Do not implement a fake end-to-end demonstration.

Prioritize the smallest real, working vertical slice, then expand it systematically.

At every milestone, report:

- What was implemented.
- What works end to end.
- What remains incomplete.
- Tests executed and their results.
- Architectural decisions made.
- Known limitations.
- The next concrete milestone.

**Final objective: Build KERN into a production-grade, distributed, self-hosted alternative to GitHub Actions, giving developers and AI agents a better CI/CD experience while keeping GitHub as their primary development interface.**
