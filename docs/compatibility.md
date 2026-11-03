# GitHub Actions compatibility matrix

Status legend: **done** = implemented and exercised, **partial** = works with
documented limits, **no** = rejected at compile time with a diagnostic.

Nothing in this file is claimed on faith: "done" means there is a test or a
verified run. Unsupported constructs are compile diagnostics (error or warning),
never silently ignored.

## Triggers

| Feature | Status | Notes |
| --- | --- | --- |
| `on: push` | done | parsed, modelled |
| `on: pull_request` | done | parsed, modelled; trust level `pull_request` withholds secrets |
| `on: workflow_dispatch` | done | `POST /projects/:p/workflows/:id/runs` |
| `on: pull_request_target` | partial | parsed; strict handling lands with the GitHub App (M4) |
| `on: release`, `repository_dispatch`, `workflow_run` | partial | parsed and modelled; no provider events yet |
| `on: schedule` | partial | cron recorded; no scheduler yet |
| `on: workflow_call` / reusable workflows | no | compile error `reusable-workflow` |
| Trigger filters (`branches`, `paths`, `tags`, `types`…) | partial | parsed; unknown filters emit a warning and are ignored |

## Jobs

| Feature | Status | Notes |
| --- | --- | --- |
| `runs-on` labels | done | label subset match against runner labels |
| `needs` DAG | done | cycle detection at compile; gating in orchestrator |
| `strategy.matrix` incl. `include`/`exclude` | done | 256-combination cap |
| `strategy.fail-fast` | done | cancels pending matrix siblings on failure |
| `if:` on jobs | done | evaluated server-side with `needs.*` results |
| `if:` on steps | done | evaluated on the runner with `steps.*`, status functions |
| `env` (workflow/job/step) | done | merged; expressions interpolated |
| `outputs` (job and step) | done | `GITHUB_OUTPUT` file command; `needs.*.outputs` |
| `timeout-minutes` | done | job + step |
| `continue-on-error` | done | conclusion vs outcome preserved |
| `concurrency.group` | done | serialises runs per project+group |
| `concurrency.cancel-in-progress` | no | recorded, not enacted |
| `container` (job image) | no | compile error `job-container` |
| `services` | no | compile error `services` |
| `environment` / approvals | no | compile error `environment` |
| `permissions` | partial | recorded, not enforced |

## Steps

| Feature | Status | Notes |
| --- | --- | --- |
| `run:` scripts | done | bash wrapper, `set -eo pipefail`, working-directory |
| `uses: actions/checkout` | done | native checkout before step execution |
| Other `uses:` actions | no | compile error `action-unsupported`; composite/JS/Docker actions are M5 |
| `shell:` | partial | `bash` honoured; other shells run under `/bin/bash` |
| `working-directory:` | done | |
| File commands `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | done | summary is captured, not yet rendered in UI |
| `::error::`/`::warning::` annotations | no | passthrough in logs only |

## Expressions

| Feature | Status | Notes |
| --- | --- | --- |
| Literals, `&&`, `||`, `!`, comparisons, parentheses | done | |
| Context access `a.b`, `a['b']`, `a[0]` | done | `github`, `env`, `vars`, `matrix`, `needs`, `steps`, `job`, `runner`, `secrets` |
| Status functions `success()`, `failure()`, `cancelled()`, `always()` | done | |
| String/object functions (`contains`, `startsWith`, `format`, `toJSON`, `hashFiles`…) | partial | core set implemented; `hashFiles` needs workspace access |
| Arithmetic, object filters `a.*` | no | parse error at compile time |

## Secrets and trust

| Feature | Status | Notes |
| --- | --- | --- |
| AES-256-GCM at rest | done | org + project scoped rows |
| Masking in logs | done | server-side at ingestion |
| Fork / pull_request runs receive no secrets | done | `{blocked: true}` contract |

## Not implemented (tracked)

- Composite, JavaScript, and Docker action runtimes (M5)
- Reusable workflows `jobs.<id>.uses` (M5)
- Service containers and job containers (M5)
- Artifacts (`actions/upload-artifact`), cache action (M5/M6)
- OIDC, deployment environments, approvals (M5+)
- `hashFiles`, some string functions, arithmetic operators
- Scheduled (`cron`) execution
- `cancel-in-progress` concurrency semantics
