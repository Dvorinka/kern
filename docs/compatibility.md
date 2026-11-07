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
| `on: pull_request` | done | live via webhook: runs the head SHA (`refs/pull/N/head`) — GitHub tests `refs/pull/N/merge` instead, a documented divergence; fork heads get trust `fork` (no secrets, own cache scope), same-repo PRs `pull_request` unless the project sets `pr_trust=trusted`, which promotes them to `trusted` (privileged executor + secrets, GitHub parity); `types`/`branches` filters honoured |
| `on: workflow_dispatch` | done | `POST /projects/:p/workflows/:id/runs` |
| `on: pull_request_target` | done | runs the base repo's default-branch head (workflow file and checkout both trusted); `github.head_ref`/`head_sha` expose the PR head branch for job-level `if:` filters; secrets allowed, so keep job code on the default branch. GitHub Apps never receive this event name; a `pull_request` delivery starts these workflows too |
| Fork-PR approval gate | done | project setting `fork_gate` (`off` default, `all`, `first-time`): matching fork runs park in `awaiting_approval` until an operator approves (`POST /runs/:id/approve`, `kern runs approve`, or the run page) or cancels; audited as `run.approve` |
| `on: release`, `repository_dispatch`, `workflow_run` | partial | parsed and modelled; no provider events yet |
| `on: schedule` | done | 5-field cron in UTC; the scheduler mints a run per due instant, deduped; missed windows are not caught up (same as GitHub) |
| `on: workflow_call` / reusable workflows | partial | `jobs.<id>.uses` expands at compile: `./.github/workflows/x.yml` (local checkout or contents API at the run SHA) and `owner/repo/...@ref`. Called jobs get prefixed ids (`caller_called`); a synthetic boundary job carries the caller's `needs`/`if`/outputs so `needs.<caller>` and `needs.<caller>.outputs` resolve. Declared `on.workflow_call` inputs/outputs honoured; resolved contents are pinned on the workflow version for deterministic recompile. `secrets: inherit` honoured — called jobs run in the caller's project so the per-job secrets endpoint already delivers the inherited set; explicit `secrets:` maps stay diagnostic. Not implemented (explicit diagnostics): `strategy`/`concurrency` on callers; a failed called job shows the caller as `skipped`, not `failed` |
| Trigger filters (`branches`, `paths`, `tags`, `types`…) | partial | parsed; unknown filters emit a warning and are ignored |

## Jobs

| Feature | Status | Notes |
| --- | --- | --- |
| `runs-on` labels | done | label subset match against runner labels; `matrix.*` expressions in labels are interpolated per combination — other contexts (`inputs.*`, `vars.*`) stay literal and warn. macOS runners advertise `macos`, `macos-<arch>`, `self-hosted`, `kern`, `<goos>-<arch>` — use `runs-on: macos` (or `[self-hosted, macos]`) for Xcode/iOS jobs |
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
| `container` (job image) | partial | `image` (string or `container.image`) and `env` (merged under job env, job wins) honoured; `credentials`/`ports`/`volumes`/`options` warn and are ignored — raw docker `options:` never crosses the trust boundary |
| `services` | partial | two access models matching GitHub's: a service without `ports` runs on a per-job docker network where its name resolves as a DNS alias (`pg:5432`, the container-job model); a service declaring `ports` shares the job container's network namespace so `localhost:<port>` works and the name maps to `127.0.0.1` (the VM-runner model — host-side remaps like `5433:5432` cannot be expressed and warn); `image`/`env` honoured, `--health-*` `options` passed to docker with a 2-minute healthy wait; `credentials` honoured via a throwaway docker login for the image pull only; `volumes` warn and are ignored (host bind mounts would expose the runner filesystem to untrusted runs); non-health options warn; service stdout/stderr stream into the active step's log under a `service:<name>` marker (lines between steps land in the next step's log) |
| `environment` / approvals | partial | `environment: name` or `{name, url}` compiles and is created on first use. A protected environment can require N distinct approvals, a named reviewer list (empty = any org member; owners and admins bypass the list), a wait of up to 43200 minutes, and a branch policy of `all` or selected name patterns (`*` does not match `/`). The job parks in `waiting_approval` until the approvals are in and the wait has elapsed. A ref outside the pattern fails the job. No GitHub-style "protected branches only" mode, because KERN has no branch-protection source |
| `permissions` | done | enforced as the ceiling on the per-run `GITHUB_TOKEN` (trusted-tier runs only): job-level map replaces workflow-level; the minted token is intersected with the GitHub App installation's grants and clamped downward — workflows can narrow, never widen. No `permissions:` block → installation defaults for the repo. `fork`/`pull_request` runs get no token regardless |

## Steps

| Feature | Status | Notes |
| --- | --- | --- |
| `run:` scripts | done | bash wrapper, `set -eo pipefail`, working-directory, `${{ }}` interpolated |
| `uses: actions/checkout` | done | native checkout before step execution; an uploaded source bundle (`kern exec`) replaces the clone entirely |
| `uses: actions/upload-artifact`, `actions/download-artifact` | done | native; `name`, `path` (glob incl. `**`), `if-no-files-found` (`warn`/`error`/`ignore`) |
| `uses: actions/cache` (+ `/restore`, `/save`) | partial | native; `key`, `path`, `restore-keys` (prefix, newest wins), `cache-hit` output, `fail-on-cache-miss`, `lookup-only`. Combined action saves at job end on miss/partial restore. Limits: workspace-relative paths only; branch-level scoping approximated by run trust (`pull_request` runs read trusted entries, save to their own scope); entries unused for 7 days are evicted |
| `uses: ./path` local composite actions | done | action.yml resolved from the checked-out workspace; inputs (defaults, `required`, case-insensitive `with:`), per-step `if:`/`env`/`working-directory`, `shell` (required per sub-step, as on GitHub), nesting to depth 10, declared `outputs:` via `steps.<id>.outputs`, summaries folded into the parent step's log and `GITHUB_ENV`/`GITHUB_PATH` propagated |
| `uses: owner/repo[/path]@ref` remote actions | partial | the runner fetches with `git fetch --depth 1` (branches, tags and reachable SHAs) into a read-only mount; public https only — host derived from the run's repo URL or `KERN_ACTIONS_GIT_BASE`; composite, JavaScript and Docker actions execute |
| `runs.using: node12/16/20/24` JavaScript actions | partial | executed inside the job container on a pinned Node distribution the runner downloads once (`<work-root>/_node`, verified against `SHASUMS256.txt`, mounted read-only); all `node*` runtimes map to Node 20; that bin directory is prepended to `PATH` so action scripts can call `node`; `INPUT_*` env and `GITHUB_*` file commands honoured. `pre:`/`post:` lifecycle hooks run at job setup / job end (post pops LIFO after all mains, gated by `pre-if`/`post-if`, default `always()`); `save-state` reaches post hooks as `STATE_*`. Linux amd64/arm64 and darwin amd64/arm64 runner hosts |
| `uses: docker://image` | partial | runs as a hardened sibling container (dropped caps, no-new-privileges, read-only root, resource limits) with workspace, file-command and `/kern/actions` mounts; on `fork` runs the sibling joins the job's egress-restricted network; `with.args` appends to the image entrypoint; `INPUT_*` env from `with:` keys |
| `runs.using: docker` Dockerfile actions | partial | `runs.image: Dockerfile` builds the action dir context — **`trusted`/`protected` runs only**: the build executes through the host docker daemon, a host-root surface sandboxed tiers are denied with a diagnostic. A registry ref pulls instead and runs on every tier. `runs.args` interpolates against `inputs`, `with.args` appends; `pre-entrypoint`/`post-entrypoint` run as fresh containers of the same image at job setup / job end; action containers run as the image's user under the same hardening as `docker://` |
| `shell:` | partial | `bash` honoured; `sh` honoured; bash-less images fall back to `sh` |
| `working-directory:` | done | |
| File commands `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | done | summaries stored per step, rendered as sanitized markdown in run detail |
| `::error::`/`::warning::`/`::notice::`/`::debug::` annotations | partial | parsed per step (props `file`/`line`/`col`/`endLine`/`endColumn`/`title`, % escapes), stored on the step, surfaced in run detail, and posted to the GitHub check run on completion (`error`→`failure`, batched 50/request up to 1000; `debug` and file-less annotations have no Checks API form and stay KERN-only). `set-env`/`add-path`/`stop-commands`/`echo` remain unimplemented |

## Expressions

| Feature | Status | Notes |
| --- | --- | --- |
| Literals (signed, `.5`, `1.`, exponent, `0x`, `0o`, `NaN`, `±Infinity`), `&&`, `||`, `!`, comparisons, parentheses | done | GitHub's `AbstractEqual` coercion ported: `'01'=='1'` is false, `'10'<'9'` is true (string-kind compare), collections compare by identity |
| Context access `a.b`, `a['b']`, `a[0]` | done | `github`, `env`, `vars`, `matrix`, `needs`, `steps`, `job`, `runner`, `secrets` |
| Status functions `success()`, `failure()`, `cancelled()`, `always()` | done | |
| String/object functions (`contains`, `startsWith`, `endsWith`, `format`, `join`, `toJSON`, `fromJSON`, `case`, `hashFiles`…) | done | `hashFiles` works in step-level expressions on the runner and errors with a diagnostic at job level; `toJSON` pretty-prints like GitHub |
| Object filters `a.*` | done | filtered arrays skip missing keys on chained derefs and flatten one level on `a.*.*`; arithmetic stays `no` — GitHub Actions has no arithmetic operators either, so `1 + 2` errors there too |

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
| Fork / pull_request runs receive no secrets | done | `{blocked: true}` contract; same gate withholds the per-run `GITHUB_TOKEN` |
| Per-run `GITHUB_TOKEN` | done | trusted-tier runs receive a ~1h repository-scoped GitHub App installation token minted at claim time, narrowed by `permissions:` (see Jobs table). Requires `KERN_GITHUB_APP_*` configured; mint failures degrade to no token with a runner-log warning, never a fabricated one |
| Fork-tier egress allowlist | done | `fork` runs execute on a per-job `--internal` docker network with HTTP/S egress only through a sibling allowlist proxy (`kern-runner egress-proxy` in a scratch image the runner builds itself). Allowlist = GitHub + major registries; `KERN_EGRESS_ALLOWLIST` replaces it, `KERN_RUNNER_EGRESS=off` disables. Non-HTTP protocols have no route at all. `pull_request` keeps full egress |

## Runner behaviour (not GitHub-surface, documented for operators)

- Every job container mounts the runner-persistent host dir `<work-root>/_cache` at `/kern/cache` (`KERN_CACHE_DIR` is exported). Point tool caches there (`GOMODCACHE`, `GOCACHE`, npm/pip caches) to get warm runs without `actions/cache` round trips. The dir survives job cleanup and is shared by all projects on the runner — do not place secrets in it.
- Job containers also mount the host file `/etc/ssl/certs/ca-certificates.crt` when the runner can see it. A bare `ubuntu:24.04` ships no CA bundle, and Go will not use its embedded roots without that file, so `go` module fetches fail closed.
- `runs-on: ubuntu-latest` / `ubuntu-24.04` and jobs without a `container:` map to `KERN_DEFAULT_IMAGE` (default `ubuntu:24.04`). Set it to the published baseline image (`ghcr.io/dvorinka/kern-ubuntu:24.04`) to get the GitHub-hosted toolset — curl, make, gcc, cmake, jq, sudo, git-lfs, python3, gh. Language toolchains stay with the workflow's `setup-*` actions. A failed pull falls back to a locally cached image instead of failing the job.
- Images the runner pulls are tracked and garbage-collected after `KERN_RUNNER_IMAGE_MAX_AGE` of disuse (default `168h`; `0` disables). GC only removes images KERN recorded, and only while no job is running.
- `KERN_RUNNER_LOAD_GATE` pauses job claiming while `loadavg1` exceeds `n` × CPU count (`0` = off). Use it on hosts where parallel builds starve the control plane.
- `KERN_ACTIONS_GIT_BASE` overrides the git host remote `uses: owner/repo@ref` actions are fetched from (default: the run's repo host, else `https://github.com`). Fetches are anonymous https/git only — private action repositories are not supported yet. Resolved refs are cached under `<work-root>/_cache/_actions` keyed by commit SHA, so repeat jobs skip the clone; entries unused for 7 days are pruned.
- The runner sweeps stale per-job dirs under the work root and, when free space drops below `KERN_RUNNER_DISK_MIN_FREE_PCT` (default `10`, `0` disables), prunes reclaimable Docker space (build cache, stopped containers, dangling images, unused networks) — only while no job is running. `KERN_RUNNER_PREPULL` keeps a comma-separated image list pulled so job pulls hit the local daemon.
- JavaScript actions need a Node runtime the runner downloads once from `nodejs.org` into `<work-root>/_node/<release>` (checksum-verified, `.complete`-marked) and mounts read-only at `/kern/node`. Air-gapped runners can pre-seed that directory with any `node-v<release>-<os>-<arch>` dist.
- `KERN_RUNNER_EXECUTOR` selects the execution substrate: `docker` (default on linux) runs steps in ephemeral containers; `host` (default on darwin) runs them as local processes under `<work-root>/<job>` with the runner's own environment plus the `GITHUB_*` contract. Host runners register `executor=host`; the claim query refuses `fork`/`pull_request` trust tiers for them — host execution only ever sees trusted work. On host executor: `container:`/`services:`/`runs.image:`/`uses: docker://` emit KERN diagnostics or fail the step (no silent ignore), composite and JavaScript actions work, `RUNNER_OS`/`RUNNER_ARCH` report the real platform (`macOS`/`ARM64` on Apple silicon).
- Job privilege follows run trust. `trusted`/`protected` runs (your own pushes, manual triggers, and same-repo PRs on projects with `pr_trust=trusted`) get GitHub parity: root, a writable rootfs, a `sudo` pass-through shim, `/var/run/docker.sock` for `docker`/`buildx`/`docker compose` steps, and `--network host`. `pull_request`, `fork`, and `deployment` runs keep the hard sandbox (uid 1000, dropped caps, no-new-privileges, read-only rootfs, noexec tmpfs, no socket); `fork` runs additionally sit on an egress-restricted internal network (see Secrets and trust). Self-heal's apt/apk fixes therefore only apply on privileged runs.
- `POST /api/v1/runners` (admin) mints an ephemeral runner credential for JIT/scale-from-zero fleets; `KERN_RUNNER_EPHEMERAL=1` makes the agent self-deregister after `KERN_RUNNER_EPHEMERAL_IDLE` (default 2m) with no active jobs. See `docs/security-isolation.md`.
- Step logs flush to the control plane every second or at 256 KiB buffered, whichever comes first.
- Org setting `auto_update` (default `off`) applies a signed GitHub release. The API writes nothing into its own container: a compose profile `update` sidecar fast-forwards `master` and rebuilds `api` plus the compose runner. Runners with `KERN_RUNNER_UPDATE=1` pull `ghcr.io/dvorinka/kern-runner` at the manifest digest and replace their own container, one at a time. Unsigned releases and a `dev` version stamp are shown and not applied. See `docs/deploy.md`.

## Not implemented (tracked)

- Cache paths outside the workspace; `upload-chunk-size` and other action tuning inputs (per-entry size cap exists: `KERN_CACHE_MAX_BYTES`, default 1 GiB, over-cap saves fail with a step diagnostic)
- OIDC; deployment branch policy "protected branches only" (no branch-protection source)
- Arithmetic operators (GitHub has none — parity, not a gap); job `container:` `credentials`/`ports`/`volumes`/`options`; service `volumes`
