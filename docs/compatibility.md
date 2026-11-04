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
| `on: schedule` | done | 5-field cron in UTC; the scheduler mints a run per due instant, deduped; missed windows are not caught up (same as GitHub) |
| `on: workflow_call` / reusable workflows | no | compile error `reusable-workflow` |
| Trigger filters (`branches`, `paths`, `tags`, `types`…) | partial | parsed; unknown filters emit a warning and are ignored |

## Jobs

| Feature | Status | Notes |
| --- | --- | --- |
| `runs-on` labels | done | label subset match against runner labels |
| `needs` DAG | done | cycle detection at compile; gating in orchestrator |
| `strategy.matrix` incl. `include`/`exclude` | done | 256-combination cap |
| `strategy.fail-fast` | done | cancels pending matrix siblings on failure |
| `if:` on jobs | done | evaluated server-side with `needs.*` results; implicit `success()` when no status function is used |
| `if:` on steps | done | evaluated on the runner with `steps.*`, status functions; implicit `success()` when none is used |
| `env` (workflow/job/step) | done | merged; expressions interpolated |
| `outputs` (job and step) | done | `GITHUB_OUTPUT` file command; `needs.*.outputs` resolved in downstream step bodies and conditions |
| `timeout-minutes` | done | job + step |
| `continue-on-error` | done | conclusion vs outcome preserved |
| `concurrency.group` | done | serialises runs per project+group |
| `concurrency.cancel-in-progress` | done | newer queued run cancels the older in-group run |
| `container` (job image) | partial | `image` only, string or `container.image`; other options warn and are ignored |
| `services` | no | compile error `services` |
| `environment` / approvals | no | compile error `environment` |
| `permissions` | partial | recorded, not enforced |

## Steps

| Feature | Status | Notes |
| --- | --- | --- |
| `run:` scripts | done | bash wrapper, `set -eo pipefail`, working-directory, `${{ }}` interpolated |
| `uses: actions/checkout` | done | native checkout before step execution; an uploaded source bundle (`kern exec`) replaces the clone entirely |
| `uses: actions/upload-artifact`, `actions/download-artifact` | done | native; `name`, `path` (glob incl. `**`), `if-no-files-found` (`warn`/`error`/`ignore`) |
| `uses: actions/cache` (+ `/restore`, `/save`) | partial | native; `key`, `path`, `restore-keys` (prefix, newest wins), `cache-hit` output, `fail-on-cache-miss`, `lookup-only`. Combined action saves at job end on miss/partial restore. Limits: workspace-relative paths only; branch-level scoping approximated by run trust (`pull_request` runs read trusted entries, save to their own scope); entries unused for 7 days are evicted |
| Other `uses:` actions | no | compile error `action-unsupported`; composite/JS/Docker actions are M5 |
| `shell:` | partial | `bash` honoured; `sh` honoured; bash-less images fall back to `sh` |
| `working-directory:` | done | |
| File commands `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | done | summaries stored per step, rendered as sanitized markdown in run detail |
| `::error::`/`::warning::` annotations | no | passthrough in logs only |

## Expressions

| Feature | Status | Notes |
| --- | --- | --- |
| Literals, `&&`, `||`, `!`, comparisons, parentheses | done | |
| Context access `a.b`, `a['b']`, `a[0]` | done | `github`, `env`, `vars`, `matrix`, `needs`, `steps`, `job`, `runner`, `secrets` |
| Status functions `success()`, `failure()`, `cancelled()`, `always()` | done | |
| String/object functions (`contains`, `startsWith`, `format`, `toJSON`, `hashFiles`…) | partial | core set implemented; `hashFiles` works in step-level expressions on the runner and errors with a diagnostic at job level |
| Arithmetic, object filters `a.*` | no | parse error at compile time |

## Provider integration (M4-lite)

| Feature | Status | Notes |
| --- | --- | --- |
| Inbound `push` webhooks | done | HMAC-verified deliveries persisted + processed; matching projects trigger runs compiled at the pushed SHA; `branches`/`branches-ignore` honoured, other filters are declared skips |
| Workflow discovery from GitHub | done | contents/tree API via `repo_full_name`; `KERN_GITHUB_TOKEN` for private repos |
| Commit statuses | done | `pending` at run creation, terminal state on completion; context `kern/<workflow>` |
| `pull_request` events, check-runs | no | needs GitHub App installation tokens (M4) |

## Secrets and trust

| Feature | Status | Notes |
| --- | --- | --- |
| AES-256-GCM at rest | done | org + project scoped rows |
| Masking in logs | done | server-side at ingestion |
| Fork / pull_request runs receive no secrets | done | `{blocked: true}` contract |

## Not implemented (tracked)

- Composite, JavaScript, and Docker action runtimes (M5)
- Reusable workflows `jobs.<id>.uses` (M5)
- Service containers; job `container` options beyond `image` (M5)
- Cache paths outside the workspace; cache size quotas; `upload-chunk-size` and other action tuning inputs
- OIDC, deployment environments, approvals (M5+)
- Some string functions, arithmetic operators, object filters `a.*`
