# Contributing to KERN

Thanks for your interest. KERN is a distributed, self-hosted CI/CD platform
written in Go — the bar for contributions is production quality, not prototypes.

## Setup

Requires Go 1.24+, Docker, PostgreSQL 16, `just`, and Node/pnpm for `apps/web`.

```sh
just dev      # control plane on :8080 (migrations + bootstrap run at boot)
just runner   # runner agent, registers against :8080
just build && just test && just vet
cd apps/web && pnpm install && pnpm build && pnpm lint
```

## Rules

- All schema changes via versioned goose migrations only.
- Secrets are AES-256-GCM at rest, masked server-side in logs, never delivered
  to `fork`/`pull_request` trust levels. Do not weaken this.
- The control plane never executes repository code; the runner only ever sees
  a `ClaimedJob`.
- Unsupported GitHub Actions features must produce compile diagnostics —
  never silently ignore them. Keep `docs/compatibility.md` honest.
- `gofmt`, `go vet`, `go test` and `pnpm build` must pass before opening a PR.

## Scope

See `plan.md` for milestones and `docs/compatibility.md` for the feature
matrix. Good first contributions: workflow-compat edge cases, expression
functions, store tests, dashboard polish. Discuss large changes in an issue
first.
