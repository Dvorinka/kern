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
| `on: pull_request` | done | live via webhook: runs the head SHA (`refs/pull/N/head`) — GitHub tests `refs/pull/N/merge` instead, a documented divergence; fork heads get trust `fork` (no secrets, own cache scope), same-repo PRs `pull_request`; `types`/`branches` filters honoured |
| `on: workflow_dispatch` | done | `POST /projects/:p/workflows/:id/runs` |
| `on: pull_request_target` | done | runs the base repo's default-branch head (workflow file and checkout both trusted); `github.head_ref`/`head_sha` expose the PR head branch for job-level `if:` filters; secrets allowed, so keep job code on the default branch. GitHub Apps never receive this event name; a `pull_request` delivery starts these workflows too |
| Fork-PR approval gate | done | project setting `fork_gate` (`off` default, `all`, `first-time`): matching fork runs park in `awaiting_approval` until an operator approves (`POST /runs/:id/approve`, `kern runs approve`, or the run page) or cancels; audited as `run.approve` |
| `on: release`, `repository_dispatch`, `workflow_run` | partial | parsed and modelled; no provider events yet |
| `on: schedule` | done | 5-field cron in UTC; the scheduler mints a run per due instant, deduped; missed windows are not caught up (same as GitHub) |
| `on: workflow_call` / reusable workflows | partial | `jobs.<id>.uses` expands at compile: `./.github/workflows/x.yml` (local checkout or contents API at the run SHA) and `owner/repo/...@ref`. Called jobs get prefixed ids (`caller_called`); a synthetic boundary job carries the caller's `needs`/`if`/outputs so `needs.<caller>` and `needs.<caller>.outputs` resolve. Declared `on.workflow_call` inputs/outputs honoured; resolved contents are pinned on the workflow version for deterministic recompile. Not implemented (explicit diagnostics): `strategy`/`concurrency` on callers, explicit `secrets:` maps (use `inherit`); a failed called job shows the caller as `skipped`, not `failed` |
| Trigger filters (`branches`, `paths`, `tags`, `types`…) | partial | parsed; unknown filters emit a warning and are ignored |

## Jobs

| Feature | Status | Notes |
| --- | --- | --- |
| `runs-on` labels | done | label subset match against runner labels |
| `needs` DAG | done | cycle detection at compile; gating in orchestrator |
| `strategy.matrix` incl. `include`/`exclude` | done | 256-combination cap |
| `strategy.fail-fast` | done | cancels pending matrix siblings on failure |
| `strategy.split` | done | opt-in integer 1–32 or `auto`. Expanded at run start into N jobs that share a job key, so fail-fast and `needs` wait for every shard. Each shard checks out on its own runner and receives `KERN_SHARD` (1-based), `KERN_SHARDS`, and `matrix.shard`. `auto` sums CPUs of online runners whose labels cover `runs-on` (a runner reporting under 1 CPU counts as 1), capped at 16; zero runners yields one shard. The product with the matrix width stays ≤ 256. Refuses `environment` and a matrix axis named `shard`. Shard outputs merge last-wins. A script that ignores `KERN_SHARD` runs the whole job N times. `kern split` prints the key and the file buckets |
| `if:` on jobs | done | evaluated server-side with `needs.*` results; implicit `success()` when no status function is used |
| `if:` on steps | done | evaluated on the runner with `steps.*`, status functions; implicit `success()` when none is used |
| `env` (workflow/job/step) | done | merged; expressions interpolated |
| `outputs` (job and step) | done | `GITHUB_OUTPUT` file command; `needs.*.outputs` resolved in downstream step bodies and conditions |
| `timeout-minutes` | done | job + step |
| `continue-on-error` | done | conclusion vs outcome preserved; a failed continue-on-error job does not fail the run and reports `needs.<id>.result = success` |
| `concurrency.group` | done | serialises runs per project+group |
| `concurrency.cancel-in-progress` | done | newer queued run cancels the older in-group run |
| `container` (job image) | partial | `image` only, string or `container.image`; other options warn and are ignored |
| `services` | partial | service containers run on a per-job docker network where the service name resolves as a DNS alias (`pg:5432`), matching GitHub's container-job access model; `image`/`env` honoured, `--health-*` `options` passed to docker with a 2-minute healthy wait; `credentials`/`ports`/`volumes` and non-health options warn and are ignored; service logs are not streamed into the job |
| `environment` / approvals | partial | `environment: name` or `{name, url}` compiles and is created on first use. A protected environment can require N distinct approvals, a named reviewer list (empty = any org member; owners and admins bypass the list), a wait of up to 43200 minutes, and a branch policy of `all` or selected name patterns (`*` does not match `/`). The job parks in `waiting_approval` until the approvals are in and the wait has elapsed. A ref outside the pattern fails the job. No GitHub-style "protected branches only" mode, because KERN has no branch-protection source |
| `permissions` | partial | recorded, not enforced |

## Steps

| Feature | Status | Notes |
| --- | --- | --- |
| `run:` scripts | done | bash wrapper, `set -eo pipefail`, working-directory, `${{ }}` interpolated |
| `uses: actions/checkout` | done | native checkout before step execution; an uploaded source bundle (`kern exec`) replaces the clone entirely |
| `uses: actions/upload-artifact`, `actions/download-artifact` | done | native; `name`, `path` (glob incl. `**`), `if-no-files-found` (`warn`/`error`/`ignore`) |
| `uses: actions/cache` (+ `/restore`, `/save`) | partial | native; `key`, `path`, `restore-keys` (prefix, newest wins), `cache-hit` output, `fail-on-cache-miss`, `lookup-only`. Combined action saves at job end on miss/partial restore. Limits: workspace-relative paths only; branch-level scoping approximated by run trust (`pull_request` runs read trusted entries, save to their own scope); entries unused for 7 days are evicted |
| `uses: ./path` local composite actions | done | action.yml resolved from the checked-out workspace; inputs (defaults, `required`, case-insensitive `with:`), per-step `if:`/`env`/`working-directory`, `shell` (required per sub-step, as on GitHub), nesting to depth 10, declared `outputs:` via `steps.<id>.outputs`, summaries folded into the parent step's log and `GITHUB_ENV`/`GITHUB_PATH` propagated |
| `uses: owner/repo[/path]@ref` remote actions | partial | the runner fetches with `git fetch --depth 1` (branches, tags and reachable SHAs) into a read-only mount; public https only — host derived from the run's repo URL or `KERN_ACTIONS_GIT_BASE`; composite, JavaScript and Docker actions execute |
| `runs.using: node12/16/20/24` JavaScript actions | partial | executed inside the job container on a pinned Node distribution the runner downloads once (`<work-root>/_node`, verified against `SHASUMS256.txt`, mounted read-only); all `node*` runtimes map to Node 20; that bin directory is prepended to `PATH` so action scripts can call `node`; `INPUT_*` env and `GITHUB_*` file commands honoured. Limits: `pre:`/`post:` entrypoints are skipped with a warning; linux amd64/arm64 runner hosts only |
| `uses: docker://image` | partial | runs as a hardened sibling container (dropped caps, no-new-privileges, read-only root, resource limits) with workspace, file-command and `/kern/actions` mounts; `with.args` appends to the image entrypoint; `INPUT_*` env from `with:` keys |
| `runs.using: docker` Dockerfile actions | partial | `runs.image: Dockerfile` builds the action dir context; a registry ref pulls instead. `runs.args` interpolates against `inputs`, `with.args` appends; `pre-entrypoint`/`post-entrypoint` are skipped with a warning; action containers run as the image's user under the same hardening as `docker://` |
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
| Inbound `pull_request` webhooks | done | head-SHA runs compiled at the head SHA on the base repo; checkout comes from the head repo so forks work; default action set + `types`/`branches` filters honoured |
| Inbound `pull_request_target` webhooks | done | not a separate App subscription. The permissions form has no such checkbox, and `X-GitHub-Event` stays `pull_request`. That delivery also starts `pull_request_target` workflows: workflow and checkout both pinned to the base repo's default-branch head (contents API, falling back to the PR base SHA); `github.head_ref`/`head_sha` carry the PR head branch; same `types`/`branches` filters as `pull_request`. A delivery that already names `pull_request_target` is handled once |
| Webhook delivery retention | done | `webhook_deliveries` rows older than `KERN_WEBHOOK_RETENTION` (default 30d) are pruned hourly |
| GitHub App auth | done | RS256 JWT → installation access tokens (cached); repo → installation resolved via API; preferred over `KERN_GITHUB_TOKEN` when `KERN_GITHUB_APP_ID` + key are set; `KERN_GITHUB_APP_PRIVATE_KEY_FILE` for pem paths |
| Workflow discovery from GitHub | done | contents/tree API via `repo_full_name`; App token or `KERN_GITHUB_TOKEN` for private repos |
| Commit statuses | done | `pending` at run creation, terminal state on completion; context `kern/<workflow>`; description includes the job tally; posted as the App installation when configured |
| Check-runs | done | one check per run, name `kern/<workflow>`, `external_id` is the run id, output is a job and step table; `details_url` matches the commit status target |
| Public run page | optional | org setting `public_ci`, default `off`. When `on`, GitHub links open `/ci/:id` (no login) backed by `GET /api/v1/public/runs/:id`. Payload is status, jobs, and step names. Logs, env, scripts, and email actors stay private. When `off`, links stay on `/runs/:id` and the public route 404s |

## Secrets and trust

| Feature | Status | Notes |
| --- | --- | --- |
| AES-256-GCM at rest | done | org + project scoped rows |
| Masking in logs | done | server-side at ingestion |
| Fork / pull_request runs receive no secrets | done | `{blocked: true}` contract |

## Runner behaviour (not GitHub-surface, documented for operators)

- Every job container mounts the runner-persistent host dir `<work-root>/_cache` at `/kern/cache` (`KERN_CACHE_DIR` is exported). Point tool caches there (`GOMODCACHE`, `GOCACHE`, npm/pip caches) to get warm runs without `actions/cache` round trips. The dir survives job cleanup and is shared by all projects on the runner — do not place secrets in it.
- Job containers also mount the host file `/etc/ssl/certs/ca-certificates.crt` when the runner can see it. `ubuntu:24.04` ships no CA bundle, and Go will not use its embedded roots without that file, so `go` module fetches fail closed.
- Images the runner pulls are tracked and garbage-collected after `KERN_RUNNER_IMAGE_MAX_AGE` of disuse (default `168h`; `0` disables). GC only removes images KERN recorded, and only while no job is running.
- `KERN_RUNNER_LOAD_GATE` pauses job claiming while `loadavg1` exceeds `n` × CPU count (`0` = off). Use it on hosts where parallel builds starve the control plane.
- `KERN_ACTIONS_GIT_BASE` overrides the git host remote `uses: owner/repo@ref` actions are fetched from (default: the run's repo host, else `https://github.com`). Fetches are anonymous https/git only — private action repositories are not supported yet.
- JavaScript actions need a Node runtime the runner downloads once from `nodejs.org` into `<work-root>/_node/<release>` (checksum-verified, `.complete`-marked) and mounts read-only at `/kern/node`. Air-gapped runners can pre-seed that directory with any `node-v<release>-linux-<arch>` dist.
- Step logs flush to the control plane every second or at 256 KiB buffered, whichever comes first.
- Org setting `auto_update` (default `off`) applies a signed GitHub release. The API writes nothing into its own container: a compose profile `update` sidecar fast-forwards `master` and rebuilds `api` plus the compose runner. Runners with `KERN_RUNNER_UPDATE=1` pull `ghcr.io/dvorinka/kern-runner` at the manifest digest and replace their own container, one at a time. Unsigned releases and a `dev` version stamp are shown and not applied. See `docs/deploy.md`.

## Not implemented (tracked)

- JavaScript action `pre:`/`post:` and docker action `pre-entrypoint:`/`post-entrypoint:` lifecycles (M5)
- Cache paths outside the workspace; cache size quotas; `upload-chunk-size` and other action tuning inputs
- OIDC; deployment branch policy "protected branches only" (no branch-protection source)
- Some string functions, arithmetic operators, object filters `a.*`
