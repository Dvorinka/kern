# KERN platform audit — 2026-11-21

Consolidated evidence for the audit wave filed as issues
[#368](https://github.com/Dvorinka/kern/issues/368)–[#378](https://github.com/Dvorinka/kern/issues/378).
Every finding below was verified against source at `d3bc174` (v0.17.0) with
`file:line` references. Issue bodies on GitHub carry the same content; this
document is the in-repo copy so fixes can cite it without leaving the checkout.

Audits covered: multi-tenant security, cache/blob storage, runner reliability,
workflow-engine GHA compatibility, parallelism/scheduling, auto-healing,
API/DB/observability, CLI contract + power-user surface, agentic MCP/CI
capabilities, and web UI/UX.

## Issue index

| Issue | Title | Labels |
| --- | --- | --- |
| [#368](https://github.com/Dvorinka/kern/issues/368) | Security: Fix multi-tenant org isolation bypasses, fork token exposure, grant/RBAC holes, and secret masking gaps | bug, go |
| [#369](https://github.com/Dvorinka/kern/issues/369) | Cache & Storage: Fix cross-scope cache poisoning, fork host-cache RCE, blob OOMs/mutex stalls, and add zero-copy zstd + direct S3 streaming | bug, enhancement, go |
| [#370](https://github.com/Dvorinka/kern/issues/370) | Runner Reliability: Fix live-exec deadlock & truncation, broken service registry creds, zombie containers, cancellation races, and log loss | bug, go, docker |
| [#371](https://github.com/Dvorinka/kern/issues/371) | Workflow Engine & GHA Compat: Fix orchestrator races, concurrency groups, matrix/reusable/expression/webhook bugs, and add dynamic matrix + merge_group | bug, enhancement, go, github_actions |
| [#372](https://github.com/Dvorinka/kern/issues/372) | Parallelism & Speed: Slot-aware fleet scheduling, persistent host tool caches, bare-mirror checkouts, BuildKit cache, and parallel job boot | enhancement, go, docker |
| [#373](https://github.com/Dvorinka/kern/issues/373) | Auto-Healing & Resilience: Fix disk/mem GC bugs, add reactive mid-run remediation, rolling self-update, flaky quarantine, and service health recovery | bug, enhancement, go |
| [#374](https://github.com/Dvorinka/kern/issues/374) | API, DB & Observability: Add missing indexes, replace hot polling with LISTEN/NOTIFY, fix N+1 & sync webhooks/checks, and add OTel + valid Prometheus | enhancement, bug, go |
| [#375](https://github.com/Dvorinka/kern/issues/375) | CLI Bugs & Contract: Fix global flag stripping, 60s timeout kills, log truncation, testbox host-container reaping, short-ID traps, --json & OpenAPI/SKILL drift | bug, go, documentation |
| [#376](https://github.com/Dvorinka/kern/issues/376) | CLI v2 Power-User Suite: Multiplexed SSE live watch & TUI, warm local testbox v2, rich diagnose, batch fleet rollout, profiler/LPT sharding, init & upgrade | enhancement, go |
| [#377](https://github.com/Dvorinka/kern/issues/377) | Agentic CI & MCP Superpowers: Streamable HTTP MCP (2025-03-26/06-18), annotations/resources/prompts, failure bundles, sandboxed repro/exec, scoped agent tokens & autonomous CI-fixer loop | enhancement, bug, go |
| [#378](https://github.com/Dvorinka/kern/issues/378) | Web UI/UX: Fix broken rerun/undrain/log-truncation/SSE bugs, eliminate 150-request fan-out storm, improve accessibility & polish run/fleet UX | bug, enhancement, accessibility, javascript |

## Issue #368 — Security: Fix multi-tenant org isolation bypasses, fork token exposure, grant/RBAC holes, and secret masking gaps

### Summary

Deep source audit of `v0.17.0` (`d3bc174`) identified critical multi-tenant isolation bypasses, untrusted fork-PR token/workflow exposures, RBAC/grant bypasses, path traversal gaps, and secret masking weaknesses across the API, store, and expression packages.

---

### Verified Findings (`file:line`)

#### 1. Cross-Org Tenant Isolation Bypasses (Critical)
- **Cross-org job claiming & secret exfiltration** (`internal/store/jobs.go:79-99`, `internal/api/handlers_runner_agent.go:897-975`): `ClaimJob`'s `candidate` CTE never joins `projects` or filters `p.org_id = me.org_id` (only `fleet` is org-scoped at line 51). A runner in Org B can claim Org A's queued jobs; `jobSecrets` verifies only `*job.RunnerID == runner.ID` and returns Org A's decrypted secrets, `clone_token`, and `github_token`.
- **Cross-org container RCE on live exec** (`internal/api/handlers_exec.go:22-80, 103-151`, `internal/api/api.go:275-277`): `createExec`, `getExec`, and `postExecStdin` never check that the job/exec belongs to `user.OrgID`. Additionally, `GET /api/v1/exec/:id` is mounted on the `read` group (`api.go:276`), allowing any read-only member to read an admin's live exec stdout.
- **Cross-org IDOR across runs, steps, logs, artifacts, deployments, webhooks, overview & MCP**:
  - `getArtifact` (`internal/api/handlers_artifacts.go:176-199`) omits `currentUser` / org checks entirely.
  - `ApproveDeployment` / `RejectDeployment` (`internal/api/handlers_environments.go:188-237`, `internal/store/environments.go:302-348, 383-426`) never verify org ownership; `reviewerAllowed` grants bypass to any org's `admin`/`owner` (or any `member` when `reviewers` is empty); `RejectDeployment` (`393`) also lacks `FOR UPDATE OF d`, racing `ApproveDeployment`.
  - `Run`, `RunPrefix`, `Runs` (when `f.ProjectID == nil`), `CancelRun`, `RerunJobs`, `ApproveRunHold`, `StepByID`, `StepLogRef`, `dayCounts`, `queuedJobs`, `stepLogs`, `stepSummary`, `stepLogStream` (`internal/store/runs.go:260-420, 584-748, 821-832, 915-990`, `internal/api/handlers_runs.go:18-94, 189-506`, `internal/store/helpers.go:39-58`) do not scope by `org_id`.
  - `webhook_deliveries` (`internal/api/handlers_webhook.go:1058-1090`, `internal/store/webhooks.go:52-121`) has no `org_id` column and exposes raw payloads globally to any authenticated `read` user.
  - `overview`, `prometheus`, `approveRemediation`, `dismissRemediation` (`internal/api/handlers_misc.go:26-140, 717-776`) and MCP `kern_list_runs`/`kern_get_run`/`kern_step_log`/`kern_rerun_run`/`kern_approve_run`/`kern_cancel_run` (`internal/api/handlers_mcp.go:266-328, 441-476, 544-577`) omit org scoping.

#### 2. Untrusted Fork PR & GitHub App Exposures (Critical)
- **Write-capable unscoped `clone_token` handed to fork/PR jobs** (`internal/api/handlers_runner_agent.go:401-429`, `internal/github/app.go:226-232`): `App.CloneToken` calls `a.token(ctx, instID)`, minting an unscoped installation token covering **all repos** with **all App permissions** (`contents:write`, `actions:write`, etc.) and sends it to every job regardless of `TrustLevel`. Both lines 402 & 419 also check only `s.GH.App != nil`, ignoring DB-registered manifest GitHub Apps (`s.dbAppClient`).
- **GitHub permissions hyphen/underscore mismatch & `permissions: {}` escalation** (`internal/github/app.go:251-277`, `internal/api/handlers_runner_agent.go:422-429, 882-886`): `forkTokenPermissions` passes `"pull-requests": "read"` (hyphen) while GitHub API expects `"pull_requests"` (underscore), silently dropping scopes. Explicit `permissions: {}` hits `len(requested) == 0` (`app.go:251`) and grants full unrestricted installation permissions.
- **Fork PR poisons `LatestWorkflowVersion` used by trusted runs** (`internal/api/handlers_webhook.go:800-831`, `internal/store/projects.go:196-213`, `internal/store/schedules.go:23-33`, `internal/api/handlers_runner_agent.go:133-157`, `internal/api/handlers_projects.go:556`): `workflowAtSHA` persists untrusted PR/fork workflow versions with `created_at = now()`, and `triggerRunByPath` persists inline `sha="adhoc"` YAML. `LatestWorkflowVersion`, `ScheduledWorkflows`, and `primeNewRunner` pick `ORDER BY created_at DESC LIMIT 1` with no branch/trust filter, letting untrusted PR YAML execute as `trusted` (with secrets) on cron/prime/fallback triggers. (Also `LatestWorkflowVersion` omits `v.deps` at `store/projects.go:201`.)
- **Cross-org webhook forgery, GET CSRF & stale App cache** (`internal/api/handlers_misc.go:144-165`, `internal/api/handlers_github_app.go:92-145, 166-234`, `internal/api/handlers_checks.go:44-61`): `githubWebhook` accepts any org's App secret without binding the signing org to `repository.full_name` (`actOnCheckRun` also skips repo/run binding); `dbAppClient` caches `ghRepoApp` globally without org scoping; `deleteGitHubApp` never evicts in-memory caches; `githubAppCallback` is a `GET` route without a CSRF `state` nonce.

#### 3. Plaintext Secrets, SSRF, Grant/RBAC & Path Traversal Bugs (High)
- **Plaintext `notify_secret` leak + `notify_url` SSRF** (`internal/api/handlers_misc.go:271-278, 365-374, 461`, `internal/api/notify.go:184-200`): `orgs.settings` stores `notify_secret` unencrypted and `GET /api/v1/settings` returns `"settings": raw` verbatim to read-only members. `notify_url` checks only `http(s)://` prefix with no private/loopback/link-local IP dial guard or redirect blocking.
- **Token grant & RBAC bypasses** (`internal/api/grant.go:63-65, 177-198`, `internal/api/handlers_mcp.go:359-476`, `internal/api/handlers_runs.go:328-342`, `internal/api/handlers_auth.go:68-108, 225-277`):
  - `constrainGrant()` returns early when `len(projects) == 0`, allowing label-only or cap-only tokens onto `/mcp`, `/secrets`, `/runners`, `/settings`.
  - MCP `kern_trigger_run`/`kern_rerun_run` skip `enforceDispatchLabels`.
  - `grantRestricted(c)` ignores `tokenCap(c)`, allowing cap-only tokens to list/revoke other tokens.
  - In-place rerun (`{"failed": true}`) reuses original `dispatch_token_id`, bypassing caller's `max_concurrent_jobs`.
  - `cliToken` (`99-100`) and `createToken` (`236-243`) allow `member`-role users to mint `admin`-scoped tokens.
- **Public UUID prefix enumeration** (`internal/api/public_run.go:23-36`, `internal/api/handlers_runs.go:121-164`): `getPublicRun` resolves `runIDParam(c)` *before* checking `publicCIEnabled`, leaking private 8-char UUID prefixes via `ambiguous_id`.
- **Path traversal & source digest spoofing**:
  - `workflow_resolver.go:19-27`: local `./` reusable lookup lacks `..` / symlink containment check.
  - `expressions/expr.go:1132`: `hashFiles('../../../../etc/passwd')` escapes workspace root.
  - `handlers_source.go:19-59`: `uploadSource` trusts client `?sha256=` without comparing computed digest and keys `sources/<slug>/<digest>.tar.gz` without `org_id`.
  - `handlers_projects.go:447-457`: `normWorkflowPath` accepts `".."` and trailing `"/.."`.

#### 4. Secret Masking & Crypto Weaknesses (High)
- (`internal/secrets/secrets.go:33-90`, `internal/api/handlers_runner_agent.go:516-517, 564-589, 681-699`, `internal/runner/executor.go:250-261`):
  - `NewMasker` does not split multiline secrets (PEM/SSH keys, JSON credentials) per line, has no minimum length guard (1-char secrets destroy logs), and `MaskBytes` has no sliding overlap buffer across flush boundaries.
  - `::add-mask::` workflow commands are unimplemented.
  - `cache_token` (`ACTIONS_RUNTIME_TOKEN`), `CloneToken`, `GitHubToken` are missing from API/runner maskers, and `finishJob` stores `req.Outputs` unmasked.
  - AES-256-GCM `Encrypt`/`Decrypt` pass `nil` AAD (no binding to org/project/env/name).
  - `Store.Secrets()` (`internal/store/secrets.go:39-51`, `handlers_misc.go:820-844`) matches *all* project secrets when `projectID == nil`, letting `DELETE /api/v1/secrets/FOO` delete a project-scoped secret.

---

### Acceptance Criteria
- [ ] Every store query and API/MCP/exec/artifact/deployment/webhook handler enforces `org_id` ownership; `ClaimJob` joins `projects` on `p.org_id = me.org_id`; add cross-org isolation tests covering two orgs in one DB.
- [ ] `CloneToken` mints repo-scoped `contents:read` tokens (via env OR DB manifest App); normalize hyphenated permission keys to underscores; honor empty `permissions: {}` as zero-scope; never mint write tokens for `fork` runs.
- [ ] Tag `workflow_versions` with `default_branch`/`trusted` (or separate `ad_hoc` flag) so untrusted PRs and `kern exec` never pollute `LatestWorkflowVersion` / cron / prime runs.
- [ ] Encrypt `notify_secret`, strip it from `GET /settings`, and block private/loopback/metadata IPs on `notify_url` egress.
- [ ] Enforce `constrainGrant` on all grant dimensions (`projects`, `labels`, `max_concurrent`), enforce role ceilings on `cliToken`/`createToken`, bind webhook secrets to owning org + repo, add OAuth `state` CSRF nonce on `githubAppCallback`, and evict deleted Apps from memory.
- [ ] Fix path containment in `workflow_resolver.go`, `hashFiles`, `normWorkflowPath`, verify `uploadSource` sha256 + org-scope key, and guard `getPublicRun` before `runIDParam`.
- [ ] Upgrade `secrets.Masker` (multiline line-splitting, min length >= 3, sliding chunk overlap, `::add-mask::`, mask `cache_token`/`CloneToken`/`GitHubToken`/`req.Outputs`), bind AES-GCM AAD, and fix `Store.Secrets()` when `projectID == nil` (`project_id IS NULL`).


## Issue #369 — Cache & Storage: Fix cross-scope cache poisoning, fork host-cache RCE, blob OOMs/mutex stalls, and add zero-copy zstd + direct S3 streaming

### Summary

Cache and blob storage currently contain critical poisoning/data-loss races, a direct fork-to-trusted RCE vector on shared runner `_cache`, heap OOMs on large uploads, and a global filesystem mutex that stalls fleet log ingestion during uploads. Fixing these alongside streaming `zstd` and presigned S3 transfers will both close the security holes and make cache/artifact I/O an order of magnitude faster.

---

### Verified Findings (`file:line`)

#### 1. Security & Integrity Defects (Critical)
- **Cross-scope cache blob poisoning & concurrent-save blob deletion** (`internal/api/handlers_cache.go:167-189`, `internal/api/handlers_ghcache.go:433-450`, `internal/api/handlers_artifacts.go:39-51`, `internal/blobs/blobs.go:79-100`):
  - `blobRef` is `cache/<projectID>/<escapedKey>.tar.gz` — omitting `scope`, `version`, and a unique ID. An untrusted `fork`/`pull_request` run saving key `K` calls `s.Blobs.Put(blobRef, ...)` and silently overwrites the `trusted` branch's live blob in place before writing its `pull_request` DB row.
  - On a concurrent save race (`store.ErrExists`), Job B has already overwritten Job A's blob at line 434 and then calls `s.Blobs.Delete(blobRef)` at line 447 — deleting Job A's live blob while leaving a permanently broken DB row (404 on restore, 409 on future saves). Identical overwrite-then-delete hazard on quota overflow in `handlers_cache.go:168-176` and `handlers_artifacts.go:39-51`.
  - All fork and same-repo PRs share one `"pull_request"` scope (`handlers_cache.go:24-36`), allowing cross-PR cache poisoning.
- **Untrusted fork jobs mount host `_cache` read-write (direct cross-job RCE)** (`internal/runner/executor.go:671`, `internal/runner/composite.go:410-412`, `internal/runner/gradle.go:77-86`):
  - Every job — including untrusted `fork` PRs — mounts `cacheDir(WorkRoot):/kern/cache` read-write (`0o777`), which contains `_actions`, `_toolcache`, `gradle/init.d`, `pnpm-store`, `cargo`, and `ms-playwright`. A malicious fork PR can poison actions or tool binaries executed by the next trusted/release job, defeating the `:ro` mount on `/kern/actions` (`executor.go:660-662`).
- **`ghcache` protocol bugs, FD leaks & HA breakage** (`internal/api/handlers_ghcache.go:58-66, 221-236, 247-258, 334-455, 513-520`, `internal/store/cache.go:44-71`, `internal/api/api.go:75`):
  - `CacheLookup` never filters by `version` (which encodes paths + compression format in `@actions/cache`), serving gzip archives to zstd callers or mismatched path sets.
  - `sweepGHCacheUploads` runs only once at startup (`api.go:75`), leaking open `*os.File` FDs, map entries, and `/tmp/kern-ghcache-*` files for every aborted upload.
  - `ghCacheUpload` buffers up to **256 MiB per PATCH in RAM** (`379`) and never bounds `start`/`end` against `CacheMaxBytes` before `WriteAt` (sparse-file / disk DoS); `ghCacheCommit` ignores `req.Size` (`407-418`) and lacks per-upload mutex synchronization against concurrent `PATCH`.
  - `ghCacheContext` (`221-236`) does not check `job.Status == "in_progress"`; `requestBaseURL` (`158-172`) trusts unvalidated `X-Forwarded-Proto`/`Host`; `ghCacheState.uploads` is in-process memory only (returns 404 under multi-API HA).

#### 2. OOMs, Global Contention & Archive Corruption
- **`S3.Put` buffers up to 4 GiB in heap RAM** (`internal/blobs/s3.go:133-149`, `handlers_artifacts.go:40`, `handlers_cache.go:168`): `io.Copy(io.MultiWriter(&buf, h), r)` buffers the full upload in memory before `PutObject`.
- **`Filesystem.Put` stalls all fleet log writes globally** (`internal/blobs/blobs.go:35, 54-100`): holds single global `f.mu.Lock()` across full network `io.Copy` (blocking every `Filesystem.Append` fleet-wide) and writes non-atomically via `os.Create` (concurrent unlocked `Open()` reads partial files).
- **`S3.Append` is O(N^2) read-modify-write + lock map leak** (`internal/blobs/s3.go:36-41, 85-88, 100-131`): re-downloads and re-uploads entire log on every chunk, leaks one `*sync.Mutex` per key forever, drops chunks across multiple API replicas, and passes `context.Background()`.
- **Runner cache/artifact corruption & RAM buffering** (`internal/runner/cache.go:58, 176`, `internal/runner/artifact.go:109`, `internal/runner/client.go:23-26, 139`):
  - `tarPaths` (`cache.go:176`) checks `d.Type().IsRegular()`, silently stripping all symlinks (`node_modules/.bin/*`, `.pnpm`).
  - `cacheRestoreStep` (`cache.go:58`) and `artifactDownload` (`artifact.go:109`) extract as runner UID without `chownTree(ws, e.execUID())`, causing `EACCES` in unprivileged containers.
  - `fh2bytes` (`client.go:23-26`) reads the entire tarball into RAM, discards `io.ReadAll` errors, and transfers under the shared 60s `http.Client.Timeout` (`139`).

---

### Implementation Plan & Optimizations
1. **Immutable content/UUID blob keys + trust isolation**:
   - Key cache/artifact blobs by `<kind>/<projectID>/<scope>/<version>/<uuid>.<ext>` (never overwrite existing blobs in place); store SHA-256 checksum on DB rows and verify on restore; scope PR caches per branch/PR (`pr:<num>`) with read-only fallback to default branch.
   - Isolate runner host caches by trust tier: mount trusted `_cache` `:ro` (or use separate `_cache_untrusted`) for `fork` jobs; keep `_actions` and `_toolcache` strictly read-only to all jobs.
2. **Zero-copy streaming + parallel `zstd` + presigned S3**:
   - Stream `tar -> klauspost/compress/zstd -> io.Pipe -> HTTP PUT` (dedicated long-timeout transfer client, preserving symlinks + `chownTree`) and `HTTP GET -> zstd/gzip auto-detect -> untar`.
   - Switch `Filesystem` to per-key locking + temp-file atomic rename (`os.Rename`).
   - Switch `S3.Put` to streaming multipart (`size = -1` with `io.TeeReader`) and add optional presigned S3 PUT/GET URLs so runner cache/artifact payloads bypass `kern-api` completely.
3. **Hardened `ghcache` & automatic cache-poison recovery**:
   - Filter `CacheLookup` by `version`, run periodic upload sweep + bound `end <= CacheMaxBytes` + verify `req.Size` + stream PATCH directly to temp file or S3 multipart upload (enabling multi-API HA).
   - On `untar`/checksum failure in `cacheRestoreStep` (`cache.go:58-61`), clean partial files, quarantine/delete the corrupt entry on the control plane, set `cache-hit=false`, and continue cleanly; on `missingActionEntrypoints` (`composite.go:291-308`), wipe `_cache/_actions/<cacheKey>` before re-cloning.

### Acceptance Criteria
- [ ] Fork PR cannot overwrite or poison trusted cache blobs (API or runner `_cache` mount).
- [ ] Concurrent cache/artifact saves never delete the winning blob; `CacheLookup` honors `version`.
- [ ] Large cache/artifact uploads use O(1) RAM on both runner and API, never stall log appends, preserve symlinks, and set proper container ownership.
- [ ] Corrupt cache blobs auto-quarantine and gracefully degrade to `cache-hit=false`.


## Issue #370 — Runner Reliability: Fix live-exec deadlock & truncation, broken service registry creds, zombie containers, cancellation races, and log loss

### Summary

Source audit of `internal/runner/*` uncovered a guaranteed deadlock in every `kern ssh`/`exec` session, silent byte truncation on exec I/O, doubly-broken private service container registry credentials, permanent zombie action containers/process leaks, multiple cancellation/watchdog bugs, and log loss/reordering in `logPump`.

---

### Verified Findings (`file:line`)

#### 1. Live Exec Deadlock, I/O Truncation & Port-Forward Drop (Critical)
- **Guaranteed deadlock on every `kern ssh` / `exec` session** (`internal/runner/exec.go:17-35, 83-116`): `execSession` binds `defer cancel()` on `runExec`'s caller context, while `runExec` starts a 300ms flusher goroutine (`posterDone`) that exits only on `<-ctx.Done()` (`89-91`), yet line `100` blocks on `<-posterDone` right after `cmd.Wait()`. Every completed exec hangs forever, never calls `finishExec`, leaks `a.execSessions`, and polls `/stdin` every 500ms indefinitely.
- **Silent data corruption/truncation on exec stdout (>256 KiB) & stdin (>64 KiB)** (`internal/blobs/blobs.go:102-135`, `internal/blobs/s3.go:151-170`, `internal/api/handlers_exec.go:111-118, 232-241`, `cmd/kern/cmd_ssh.go:140`, `internal/runner/exec.go:149`): `ReadFrom` returns total blob `size`, not `offset + len(chunk)`, yet `streamExec` sets `off = out.Size` and `pumpExecStdin` sets `off = size`. Any burst exceeding 256 KiB stdout or 64 KiB stdin permanently drops the gap bytes.
- **Exec client robustness** (`internal/runner/exec.go:141-143, 185-193`): `pumpExecStdin` aborts on the first transient HTTP error; `execPoster.flushLocked` holds `p.mu` across 60s HTTP POSTs and drops chunks on failure.
- **Half-closed TCP drop in `portforward.go`** (`internal/runner/portforward.go:21-57`): `spliceTCP` waits on `<-done` only once before closing both sockets via `defer`, killing responses whenever a client calls `CloseWrite`. Dial errors are dropped silently and `RunPortForward` lacks a shutdown context.

#### 2. Private Service Container Registry Credentials Doubly Broken (High)
- (`internal/workflows/services.go:65`, `internal/orchestrator/orchestrator.go:617-628`, `internal/store/runs.go:52-58`, `internal/runner/services.go:123-157`):
  1. `serviceSeeds()` and `store.ServiceSeed` drop `sp.Credentials` before DB persistence.
  2. Even when populated, `registryLogin` (`services.go:140-157`) runs `docker --config <tmpdir> login` and immediately deletes `<tmpdir>` via `defer os.RemoveAll(dir)` on return; `pullService` (`129`) then runs `docker pull -q` without `--config` (always unauthenticated).

#### 3. Zombie Action Containers, Leaked Images & Unkilled Step Processes (High)
- (`internal/runner/dockeraction.go:77-81, 175-268`, `internal/runner/steps.go:251, 339`, `internal/runner/executor.go:1160-1176, 1233`, `internal/runner/gc.go:247`):
  - Killing `stepCtx` kills only the `docker` CLI client, leaving `docker run --rm --name kern-action-...` running detached on the daemon. Neither `cleanup()`, `orphanedContainers` (skips `kern-*` at `executor.go:1233`), nor `sweepOrphanServices` (matches only `kern-svc-`/`kern-job-`/`kern-egro-` at `gc.go:247`) reaps it.
  - Dockerfile-built action images (`kern-action-<job>-<step>`, `dockeraction.go:77-81`) are never tracked in `image-usage.json` or removed in `cleanup()`.
  - `docker exec` (`steps.go:251`) leaves timed-out processes alive inside `e.container` during subsequent `if: always()`/`post` steps.
  - Host-mode `exec.CommandContext` (`steps.go:339`) omits `Setpgid: true` / process-group SIGKILL, leaking child build processes (`go test`, `node`, `java`).

#### 4. Cancellation, Watchdog & Concurrency Races (High)
- **Cancelled `uses:` step still executes** (`internal/runner/steps.go:174-182`): `runStepBody` returns `("cancelled", 1, nil)` (`stepErr == nil`), so `if step.Uses != ""` still invokes `runUsesStep`.
- **`dockerActionRun` misclassifies `disk_quota` as `"cancelled"` & misses pre-launch cancel** (`internal/runner/dockeraction.go:239-241, 266-268`), suppressing `disk_quota` diagnosis and transient retry.
- **`prepare()` & `runPreHooks()` ignore job cancellation and `step.If`** (`internal/runner/executor.go:348-404`, `internal/runner/lifecycle.go:65-82, 111`): `e.killFn` is `nil` during prepare; `runPreHooks` ignores `ctx.Err()` and arms `post` hooks for skipped steps.
- **`watchSilence` can hang forever in `procDump` and races `pump.close()`** (`internal/runner/watchdog.go:149-151, 171-189`, `internal/runner/steps.go:227-228`): `procDump` uses unbounded `stepCtx` so a wedged container blocks reaching the kill branch.
- **Sticky job-wide watchdog latches** (`internal/runner/steps.go:76-87`, `watchdog.go:54, 157`): `e.hangDetected` and `e.diskQuotaExceeded` are never reset between steps, misdiagnosing later failures after a `continue-on-error` step.
- **Uncancellable cache/artifact contexts** (`cache.go:37, 131`, `artifact.go:72, 92, 101`) pass `context.Background()`.
- **Egress proxy readiness check ignores output** (`internal/runner/egress.go:211-220`): checks only `.Run() == nil`, never stdout == `"true"`.
- **Leaked sampler goroutines** (`metrics.go:154-196`, `watchdog.go:22-66`, `local.go:25-67`): not stopped by `cleanup()` / `e.stop`.
- **Multi-slot runner races** (`executor.go:757-759, 1296-1336`, `composite.go:907-915`, `lifecycle.go:223-232`): global `/github/workspace` symlink rewrite races at `Concurrency > 1`; `seedDockerCLI` writes `_cli/docker` unlocked (`ETXTBSY`); `drainFile`/`drainState` delete `GITHUB_OUTPUT`/`GITHUB_STATE`, breaking subsequent `dockerActionRun` steps.

#### 5. Log Loss, Out-of-Order Flushes & Silent Truncation (High)
- (`internal/runner/steps.go:767-840`, `internal/api/handlers_runner_agent.go:505-529`, `internal/runner/services.go:517-544`):
  - `logPump.flush()` unlocks `p.mu` before HTTP POST, allowing 1s ticker and 256 KiB threshold flushes to append out of order, and drops up to 256 KiB on transient HTTP failure.
  - `br.ReadString('
')` (`steps.go:829`) has unbounded line RAM, while `appendLog` (`handlers_runner_agent.go:511`) silently truncates chunks >1 MiB and returns `200 OK`.
  - `svcLogLine` (`services.go:517-523`) drops 3/4 of buffer at an arbitrary mid-UTF-8 byte offset; `flushServiceLogs` ignores `svcLogPendingCap` when `currentStep == 0`.

---

### Acceptance Criteria
- [ ] `runExec` uses a dedicated flush-stop channel/sub-context so `cmd.Wait()` cleanly flushes final bytes and calls `finishExec`; `getExec` / `execStdinFeed` advance offset by `len(chunk)` (or return `next_offset`).
- [ ] `spliceTCP` supports half-close (`CloseWrite` + wait on both directions) and graceful context shutdown.
- [ ] Service container `Credentials` persist through `ServiceSeed` and pass `--config <tmpdir>` to `pullService` before removing `<tmpdir>`.
- [ ] Timeout/cancel explicitly `docker rm -f`s `kern-action-*` containers, sweeps `kern-action-*` in `gc.go`, tracks/removes built action images, and kills host step process groups (`Setpgid: true`).
- [ ] Fix all cancellation, watchdog latch, `procDump` timeout, `egress` check, `seedDockerCLI` lock, `drainFile` truncation (not unlink), and sequentialized `logPump` retry/chunking (<=512 KiB chunks) with unit tests.


## Issue #371 — Workflow Engine & GHA Compat: Fix orchestrator races, concurrency groups, matrix/reusable/expression/webhook bugs, and add dynamic matrix + merge_group

### Summary

Deep audit of `internal/orchestrator/*`, `internal/workflows/*`, `internal/expressions/*`, `internal/store/{runs,jobs,helpers,schedules}.go`, and `internal/api/handlers_webhook.go` found high-impact state-machine races, concurrency-group inversions, GitHub Actions compatibility bugs, and two major missing platform capabilities (dynamic `fromJSON` matrix and `merge_group`/preview deployments).

---

### Verified Bugs (`file:line`)

#### 1. Concurrency Groups, Scheduler & State-Machine Defects (High)
- **Five defects in `PromoteQueuedRuns` & concurrency groups** (`internal/store/runs.go:686-738, 753-817`, `internal/orchestrator/orchestrator.go:149-158, 768-777`):
  1. `triggerContext` omits `github.workflow`, `github.head_ref`, `github.ref_name`, `github.event`, `inputs`, and `vars` — collapsing standard `${{ github.workflow }}-${{ github.head_ref || github.ref }}` groups across unrelated workflows.
  2. `runs.go:764` checks `AND o.cancel_in_progress` (old run) instead of `n.cancel_in_progress` (new run).
  3. No pending-queue depth cap of 1 (GitHub cancels superseded queued runs when `cancel-in-progress: false`).
  4. Non-atomic cancellation forgets to cancel `steps` and never fires `PostStatus` / `notifyRunCompleted`, leaving superseded PR checks stuck `in_progress` on GitHub.
  5. `RerunJobs` (`686-738`) bypasses concurrency groups, retains stale `jobs.classification`/`metrics`, orphans old log/summary blobs, and never resets GitHub Check Runs.
- **`AdvanceRun` concurrency, matrix/cancelled needs & 200-run ceiling** (`internal/orchestrator/orchestrator.go:348, 394-580, 658-680`, `internal/store/runs.go:841-890`, `internal/store/helpers.go:16-22, 61-78`):
  - Unlocked concurrent `AdvanceRun` (`Tick` + `finishJob`) can insert duplicate `deployments` rows (`466-488` non-transactional) and double-fire `PostStatus` + `notifyRunCompleted` because `FinishRunIfDone` doesn't report `RowsAffected() == 1`.
  - `ActiveRunIDs(ctx, 200)` (`orchestrator.go:348`) orders oldest-first — runs #201+ starve indefinitely if 200 runs sit waiting on approval.
  - `NeedsMap` (`520-548`) overwrites `result = j.Conclusion` per matrix member so `[failure, success]` reports `"success"`.
  - `dependencyStatus` (`570-571`) maps `cancelled` to `"failure"`, breaking `if: cancelled()`.
  - `needsWithAliases` (`658-680`) skips overwriting colliding caller/callee job keys.
  - `CancelSiblings` (`helpers.go:61-78`) ignores `in_progress` siblings and updates `deployments` without filtering `j.status`, corrupting running/succeeded deployments.
- **Requeue & Timeout state corruption** (`internal/store/jobs.go:311-323, 415-420, 461-466, 521-524, 545-573`, `internal/store/selfheal.go:163-168`):
  - `RequeueJob`/`ReapExpiredLeases`/`ApproveRemediation` skip resetting `completed` steps and `log_ref`/`diagnosis`, concatenating attempt 2 logs onto attempt 1 and triggering false retries from stale attempt-1 diagnoses.
  - `TimeoutOverdueJobs` (`545-573`) marks jobs `completed` without releasing `job_leases`, cancelling `steps`, or clearing `runner_id`.
- **Multi-cron dedupe collision & offline-runner alert drop**:
  - `HasScheduledRun` (`orchestrator/cron.go:40`, `store/schedules.go:66-74`) ignores the cron expression; two schedules in one workflow firing at the same minute silently drop the second.
  - `NotifyRunnersOffline` (`internal/api/notify.go:78-111`) keys `seen` by `r.OrgID` instead of `r.ID`, dropping alerts for all but the first offline runner per org.
  - `CreateRun` (`store/runs.go:107-117`) retries all errors 5x instead of only SQLSTATE `23505`.

#### 2. Workflow Compiler, Matrix, Reusable, Cron & Webhook Bugs (High)
- **Include-only matrix collapses N jobs into 1** (`internal/workflows/matrix.go:44-58, 85-102`): `matchesDimensions` returns `true` for every `include` item when `dimKeys` is empty, overwriting `combos[0]` repeatedly.
- **Matrix Cartesian OOM DoS** (`internal/workflows/matrix.go:45-58`, `compile.go:281-290`): expands full Cartesian product in RAM before enforcing `maxMatrixJobs = 256`.
- **Nested reusable workflow secret leak & `__boundary` waste** (`internal/workflows/reusable.go:196-250`, `compile.go:269`): line 214 unconditionally overwrites `nj.SecretsMap = secretsMap` (so `A -> secrets:inherit -> B -> no secrets -> C` leaks all org/project secrets to `C`, and clobbers `C`'s `Inputs`); synthetic `__boundary` hardcodes `RunsOn: ["self-hosted"]` and leases a full container (`236`); `Resolve()` runs before `maxCallDepth`/`maxCalls`.
- **Webhook & trigger filter bugs** (`internal/api/handlers_webhook.go:588-615, 686-717, 757-766, 808-825, 914-933, 1011, 1039-1044`, `internal/workflows/event.go:79-86`):
  - `repository_dispatch` expects nested `{"repository_dispatch": {...}}` instead of GitHub's top-level `action` + `client_payload`.
  - `pushPathsMatch` skips if **any** file matches `paths-ignore` instead of **all** files (editing `README.md` + code skips CI).
  - `pullRequestMatchesPlan` rejects `paths`/`paths-ignore` on PR events.
  - `pathGlobMatch` maps `?` to regex optional-quantifier instead of `[^/]`; `globMatch` lets `*` cross `/`.
  - `pushChangedFiles` fails on first push to new branch (`before == 0000...`); `resolvePRTargetSHA` uses `default_branch` (unescaped) instead of `pr.Base.Ref`; `workflowAtSHA` panics on `sha[:8]` and runs deleted workflow files on 404.
- **Cron parser POSIX `dow=7` bug** (`internal/workflows/cron.go:94-111`): zeros `s.dow[7]` before reading it (`5-7`, `1-7`, `*/7` never match Sunday); lacks `MON`/`JAN` names; `NextCron` brute-forces up to 1M minutes.
- **Standard GHA YAML decode/validation gaps** (`internal/workflows/model.go:20-40, 101-136, 175-176`, `compile.go:125-140, 217-220, 297-302, 708`, `validate.go:12-31`): decode errors on scalar `concurrency`, expression `cancel-in-progress`/`continue-on-error`/`fail-fast`/`timeout-minutes`, scalar `permissions: read-all|write-all`, mapping `runs-on: {group, labels}`; silently ignores `jobs.<id>.concurrency` and `strategy.max-parallel`; typos in `pwsh-windows` (`217`) and `ghcrAuthedByGitHubToken` (`708`); incomplete expression validation in `validate.go`.

#### 3. Expression Evaluator Bugs (`internal/expressions/expr.go`) (High)
- `Truthy()` (`128-152`): treats `"0"`/`"0.0"` as falsy (non-empty strings are truthy in GHA) and `NaN` as truthy (falsy in GHA).
- `hashFiles()` (`1124-1154`): returns empty SHA-256 instead of `""` on 0 matches and treats `!` exclusions as literal paths.
- Case-sensitive context lookup (`599-601, 759`); equal precedence (`3`) for `==`/`!=` and `<`/`<=`/`>`/`>=` (`402-406`); `statusCheckRe` matching inside string literals + outer `${{ }}` stripping breaking `${{ a }} && ${{ b }}` (`105-117`); backslash escaping in single-quoted strings (`231-235`); eager evaluation of `case()` args (`740-748`).

---

### High-Impact Platform Enhancements
1. **Dynamic Matrix (`matrix: ${{ fromJSON(needs.changes.outputs.matrix) }}`) & `strategy.max-parallel`**: Defer job-template expansion into `AdvanceRun` once upstream `needs` finish, and enforce `max-parallel` in `ClaimJob`.
2. **Monorepo PR & Push Affected-Path Skipping**: Implement paginated `GET /repos/{repo}/pulls/{n}/files` + push commit-list fallback (`before == 0000...`), enabling full `paths`/`paths-ignore` on `push` and `pull_request` plus optional job-level `paths:` skipping.
3. **GitHub Merge Queue (`merge_group`) & Ephemeral Preview Deployments**: Handle `merge_group` (`checks_requested`/`destroyed`) and `pull_request` `closed` teardown; sync KERN `deployments` bidirectionally with GitHub's Deployments API (posting live preview URLs on PRs) and add environment TTL auto-teardown.

### Acceptance Criteria
- [ ] All concurrency-group, `AdvanceRun`, `NeedsMap`, `CancelSiblings`, `RequeueJob`, `TimeoutOverdueJobs`, and `HasScheduledRun` bugs fixed with DB integration tests.
- [ ] All matrix, reusable-workflow, webhook filter, cron, YAML-model, and expression bugs fixed with unit test matrices matching GHA semantics.
- [ ] Dynamic `fromJSON` matrix, PR `paths`/`paths-ignore`, and `merge_group` webhooks work end-to-end.


## Issue #372 — Parallelism & Speed: Slot-aware fleet scheduling, persistent host tool caches, bare-mirror checkouts, BuildKit cache, and parallel job boot

### Summary

Real-world CI runs (e.g. Invico's multi-lane CI) spend significant wall-clock time on avoidable overhead: repeated full-history `git clone`, cold Go/Python/linter caches (re-downloading `golangci-lint` every run), unconditional `docker pull` + throwaway container probes, single-slot runner starvation, serial service boot, and host-port locking that serializes parallel Postgres/Redis jobs. Addressing these bottlenecks cuts typical job times dramatically and unlocks true horizontal & vertical parallelism.

---

### Verified Bottlenecks & Implementation Blueprint (`file:line`)

#### 1. Slot-Aware Fleet Scheduling & True Multi-Slot Concurrency
- **Bottlenecks**:
  - `runners` stores `cpus`, not worker `concurrency` (`migrations/0001_init.sql:196-215`). `ClaimJob`'s least-loaded yield (`internal/store/jobs.go:47-53, 151-173`) compares raw `o.load < me_f.load` without knowing if peer `o` has any free slot — a full 1-slot runner (`1/1`) starves a 4-slot runner (`2/4`, 2 idle slots).
  - `Agent.claimsPaused()` (`internal/runner/agent.go:671-683`) omits `a.updating` and `a.retire`, and newly minted JIT runners (`handlers_runners.go:267-270`) start as `online` with `load=0` — causing busy runners to yield all claims to them.
  - `needs_docker` fallback (`internal/store/jobs.go:105-114`) omits `AND h.claims_paused = ''`, so one paused `kern-docker` runner blocks fallback runners.
  - `proj_caps`/`token_caps` (`jobs.go:54-93, 175`) are unlocked MVCC snapshots, allowing concurrent claims to overshoot `max_concurrent_jobs`.
  - `SizeWorkers` (`internal/runner/size.go:10-34`) defaults to 1 worker per CPU (`1 CPU / 1 GiB` per job), starving parallel compilers and causing OOMs.
  - `FleetSlots` (`internal/store/runners.go:112-127`, `orchestrator/split.go:100-136`) sums raw `cpus` instead of free worker slots, so `split: auto` on a 16-CPU `Concurrency=1` runner spawns 16 serial shards.
- **Upgrade**: Persist `runners.concurrency` (reported on register + heartbeat); yield only to peers with `o.load < o.concurrency` and lower utilization ratio (`load/concurrency`); mark updating/retiring/unbooted-JIT runners non-claimable; enforce `max_concurrent_jobs` with transactional advisory/row lock; size `split: auto` from actual free slots bounded by historical duration; run `StartRun` -> `AdvanceRun` to fixpoint immediately (removing 2s scheduler tick latency per DAG hop); schedule longest-pole jobs first within equal priority using historical medians (`StepMedians`).

#### 2. Wire Missing Persistent Host Tool Caches & Runner Tool Cache
- **Bottleneck**: `gc.go:543-581` already defines eviction units for `gomod`, `gocache`, `gopath`, and `executor.go:233-234` comments that `GOMODCACHE, npm, pip` live in `_cache`, **yet `jobEnv()` (`executor.go:776-896`) never exports `GOMODCACHE`, `GOCACHE`, `GOLANGCI_LINT_CACHE`, `PIP_CACHE_DIR`, `UV_CACHE_DIR`, `YARN_CACHE_FOLDER`, `RUSTUP_HOME`, or `SCCACHE_DIR`**. Because `HOME` is per-job ephemeral (`executor.go:832, 1172`), Go/Python/linter jobs rebuild cold on every run, and tools like `golangci-lint` re-download into `/tmp` on every job.
- **Upgrade**: Export all language/linter cache env vars under trust-isolated `e.cachePath()` (paired with trusted/untrusted isolation from the cache security issue); raise default `KERN_RUNNER_CACHE_MAX_BYTES` (`cmd/runner/main.go:400`) from `8 GiB` to a disk-proportional budget (e.g., 25-30% of volume, min 20 GiB); extend `toolset` / `_toolcache` with persistent cached CLI binaries (`golangci-lint`, `trivy`, `gitleaks`, `osv-scanner`, `pnpm`, `uv`, `gh`).

#### 3. Shallow / Bare-Mirror Git Checkouts
- **Bottleneck**: `cloneArgs` (`internal/runner/executor.go:982-1067`, specifically `1062-1064`) disables `--depth 1` whenever `run.SHA != ""` — which is true for virtually every webhook/PR/push run — forcing a full-history network clone + full `chownTree` walk (`368`) on every job and shard.
- **Upgrade**: Maintain per-repo bare mirrors under `_cache/_repos/<slug>.git` (per-repo mutex, updated via `git fetch --prune --filter=blob:none origin <sha>`) and materialize workspaces via `git worktree add --detach` or `git clone --shared --no-checkout` + checkout; fall back to `git init && git fetch --depth=1 origin <sha>` (proven in `composite.go:232`) when mirrors are disabled.

#### 4. Eliminate Unconditional `docker pull`, Throwaway Probes & Serial Boot
- **Bottlenecks**:
  - Unconditional `docker pull -q` on every job (`executor.go:531`), service (`services.go:129`), and Docker action (`dockeraction.go:85`).
  - `imageUserHome` (`executor.go:572-587`) spawns a throwaway `docker run --rm` container on every unprivileged job just to read `/etc/passwd`.
  - `runStepUnit` (`steps.go:174-182`) runs an empty `docker exec` on every `uses:` step just to reset `/github/cmd/*`.
  - `startServices` (`services.go:93-120`) pulls, starts, and health-waits services sequentially; `runPreHooks` (`lifecycle.go:69-81`) fetches remote actions sequentially and calls `git ls-remote` (`composite.go:488`) on every job with zero TTL.
- **Upgrade**: Add image pull policy (`if-not-present` for immutable digests/semver tags, 15m TTL for mutable tags refreshed by `prepullLoop`); memoize `(imageDigest -> PATH, uid1000Home, hasBash)`; reset `/github/cmd/*` in Go for `uses:` steps; parallelize service pulls/starts/waits and `runPreHooks` fetches; add 5-15m TTL on `refPointerPath` mtime before `git ls-remote`; optionally maintain a warm container pool for default runner images.

#### 5. Persistent Runner-Local BuildKit & Registry Mirror
- **Bottleneck**: `docker/build-push-action` with `cache-from/to: type=gha` streams GBs of layers through `kern-api`'s `ghcache`, and every job spawns isolated `buildx_*` builders (`executor.go:639, 1233`).
- **Upgrade**: Support a persistent shared BuildKit builder (`BUILDX_BUILDER` + `DOCKER_BUILDKIT=1`) backed by host cache (`/kern/cache/buildx`) or LAN pull-through OCI registry, plus `KERN_DOCKER_REGISTRY_MIRROR`.

#### 6. Parallel Service-Backed Jobs (Remove `hostPortLocks` Serialization)
- **Bottleneck**: Privileged jobs use `--network host` (`executor.go:614-616`) and bind services to `127.0.0.1:<port>`, forcing all concurrent jobs using `:5432`/`:6379` on the same runner to serialize behind `hostPortLocks` (`internal/runner/services.go:206-218, 351-426`).
- **Upgrade**: Run privileged jobs on per-job isolated bridge networks (preserving `localhost:<port>` service reachability via `--network container:<job>` + port-forwarders, exposing daemon via `docker.sock` + `host.docker.internal`), unlocking concurrent Postgres/Redis integration jobs on one host.

### Acceptance Criteria
- [ ] `runners.concurrency` persisted and used by `ClaimJob` and `FleetSlots` (`split: auto`).
- [ ] Go/Python/linter caches persist across runs with measurable warm-run speedup; shallow/mirror checkout avoids full-history clones.
- [ ] No unconditional `docker pull` for immutable images, no throwaway `imageUserHome` container, no empty `docker exec` for `uses:`, parallel service boot.
- [ ] Two jobs requiring Postgres `:5432` run simultaneously on one multi-slot runner without port conflict or serialization.


## Issue #373 — Auto-Healing & Resilience: Fix disk/mem GC bugs, add reactive mid-run remediation, rolling self-update, flaky quarantine, and service health recovery

### Summary

KERN already has a strong foundation for self-healing (allow-listed remediation, PSI claim gate, transient retry, self-update). Source inspection revealed several bugs neutralizing disk GC and self-update on real fleets, plus high-leverage upgrades to make runner recovery, retries, rollouts, and flake handling genuinely autonomous.

---

### Verified Bugs (`file:line`)

#### 1. Disk/Memory Gate & GC Bugs (High)
- **All disk-pressure reclaim is dead code on `host` and `kern-docker` runners** (`internal/runner/gc.go:322, 639-642`, `internal/runner/agent.go:447-462`): `maybePruneDisk` exits early on `if a.executor() != "docker" { return }`, disabling both Docker/BuildKit pruning on `kern-docker` hosts AND workspace/cache/tmp pruning on bare-metal Linux/macOS/Windows host runners.
- **`totalDisk` reports FREE bytes instead of TOTAL bytes** (`internal/runner/disk.go:11-20`): returns `stat.Bavail * stat.Bsize` instead of `stat.Blocks * stat.Bsize` (unlike `disk_windows.go:11-14`).
- **100%-full disk suppresses alerts** (`internal/runner/metrics.go:71-74`, `internal/api/handlers_runner_agent.go:195`): `if free > 0` omits `DiskFreeBytes` when `free == 0`, silencing `maybeAlertRunnerDisk`.
- **`KERN_JOB_DISK: "20g"` silently ignored** (`internal/runner/executor.go:957`, `internal/runner/metrics.go:219-231`): uses `metrics.go`'s strict-case `parseHumanBytes` (`"GiB"`/`"GB"`) instead of `cmd/runner/main.go:559`'s parser (`"20g"`).
- **Mount & cgroup blind spots** (`agent.go:595-616, 699, 729-746`, `gc.go:647`, `metrics_linux.go:107-126`): probes only `WorkRoot` (missing separate `/var/lib/docker` mounts); `memClaimPaused` reads host `/proc/meminfo` instead of cgroup v2 `memory.max`, lacks resume hysteresis, and latches stale values when `overLoaded()` short-circuits `workerLoop`.
- **GC races & active-workspace deletion hazard** (`gc.go:33-41, 78, 82-112, 128, 347-368, 385, 468`, `composite.go:518, 599-623`): unlocked non-atomic `image-usage.json` writes corrupt the ledger; `sweepWorkspaces` ignores `activeJobs` and checks only top-level dir mtime; `maybePruneActionCache` deletes under `_cache/_actions` mid-job without `actionCacheLocks`.

#### 2. Self-Update & Toolset Footguns (High)
- **25s claim-vs-update race** (`internal/runner/agent.go:563-576, 591-636`): `workerLoop` parks in `claim(ctx, 25)` while `a.active == 0`; `maybeSelfUpdate` sees `busy() == false` and stops/re-execs the runner mid-claim, burning an attempt + 90s lease expiry.
- **Broken self-inspect & lost container config** (`internal/runner/selfupdate.go:33-125, 144-148`): `os.Hostname()` fails whenever started with custom `--hostname`; `planReplace` drops `HostConfig.Mounts`, `Memory`/`NanoCpus`, `Devices`, `SecurityOpt`, `ExtraHosts`, `Config.Labels`, `/udp` port suffixes (`98`), and multiline env values (`110-115`).
- **Binary update staging leak & no HTTP timeout** (`internal/runner/selfupdate_binary.go:69-75, 103`): leaks `exe.new` if `busy()` triggers; `downloadFile` uses `http.DefaultClient` (no timeout).
- **`kern-updater.sh` skips compose runner** (`deployments/kern-updater.sh:117`): runs `up -d --build --no-deps api`, leaving the compose `runner` stale.
- **Toolset self-heal races & hangs** (`agent.go:284-346`, `toolset.go:55`, `diagnosis.go:274-284`): no single-flight mutex on `healToolset`; Linux curated plans assume writable `/usr/local/bin`; `apt-get` runs without `DEBIAN_FRONTEND=noninteractive`.

---

### Autonomous Resilience Enhancements

1. **Reactive Mid-Run Disk & Memory Remediation**:
   - Trigger `maybePruneDisk` reactively via signal channel (not just 15m tick at `gc.go:118`) on `diskClaimPaused`, `startDiskSampler` crossing 70% budget, any `disk_full`/`disk_quota` failure, or `active` hitting 0 while paused.
   - Run non-Docker sweeps on all executors and Docker/BuildKit prunes whenever `dockerOK()` is true (including `kern-docker`); step down `buildxKeep` progressively (`50% -> 25% -> 0` + `docker volume prune -f`) under critical pressure; probe both `WorkRoot` and `DockerRootDir`; add cgroup v2 `memory.max` + PSI watch with hysteresis.
2. **Smarter Failure Taxonomy, Exponential Backoff & OOM Auto-Upsize** (`diagnosis.go:73-249`, `handlers_runner_agent.go:729-857`, `steps.go:779`, `jobs.go:446`):
   - Expand transient signatures (`502/503/504`, `socket hang up`, `EAI_AGAIN`, `EPIPE`, `unexpected EOF`, `toomanyrequests`, `Hash Sum mismatch`, `early EOF`, `RPC failed`, `remote end hung up`, `hang`, prepare-phase `panic`).
   - Replace 8 KiB tail cap (`steps.go:779`) with 64 KiB ring + error-line ring; inspect `docker inspect .State.OOMKilled` and cgroup `memory.events` (`oom_kill`) authoritatively.
   - Add `not_before timestamptz` exponential backoff with jitter on `RequeueJob`; separate `infra_retries` from `transient_retries`; on `oom` retry, prefer runners with higher free RAM and allow a 1-step memory-tier bump (e.g., 1.5x-2x up to host cap).
3. **Automatic Stale/`dev` Runner Quarantine & Controlled Rolling Self-Update** (`manifest.go:109-111`, `handlers_update.go:53-55, 384-403`, `lease.go:26-42`):
   - Add `min_runner_version` / `quarantine_dev_runners` org setting that auto-drains non-compliant/`dev` runners (emitting audit + alert) and excludes them in `ClaimJob` when compliant peers exist.
   - Replace the in-memory 15m global `runnerOffer` lock with a Postgres-persisted rollout state machine (`update_pending -> draining -> swapping -> verifying -> online`, 1 runner per label pool), cleanly draining before swap, releasing immediately on healthy heartbeat, and auto-rolling back to `<name>-prev` if the new container fails to heartbeat within 90s.
   - Resolve container ID via cgroup/mountinfo (or `KERN_RUNNER_CONTAINER_NAME`) instead of `os.Hostname()`, preserving full `HostConfig`/`Config`.
4. **Flaky Test Detection & Quarantine**:
   - Ingest test results (JUnit XML / `go test -json` / Playwright JSON) + track SHA-level fail-then-pass flips (`(project_id, workflow_id, job_key, test_name, sha, passed, duration_ms)`).
   - Detect flakes (>N% flip rate over 14d or pass-on-retry on same SHA), surface via `KERN_QUARANTINED_TESTS`, and optionally auto-rerun only failed test cases once in-step before marking red.
5. **Service Container Readiness, Runtime Health Watch & Recovery** (`services.go:70-121, 439-467`):
   - Fall back to active TCP dial probes on declared `svc.Ports` when no Docker `HEALTHCHECK` exists (preventing step-1 `ECONNREFUSED`).
   - Surface `State.Health.Log` + last 30 lines of container logs on `"unhealthy"`, allow 1 bounded recreate-and-retry on transient startup crash, and inspect `kern-svc-*` (`OOMKilled`/`ExitCode`) inside `diagnose()` to emit transient-retryable `service_oom`/`service_crashed` verdicts.

### Acceptance Criteria
- [ ] All disk, GC, cgroup memory, self-update, and toolset bugs fixed with tests.
- [ ] Mid-run disk/memory pressure triggers reactive reclaim across all executor modes.
- [ ] Rolling self-update drains cleanly without claim races, preserves full container config, verifies health, and rolls back on boot failure.
- [ ] Transient retries use jittered backoff; OOM retries inspect authoritative kernel state and prefer higher-memory placement; service crashes report explicit diagnoses.


## Issue #374 — API, DB & Observability: Add missing indexes, replace hot polling with LISTEN/NOTIFY, fix N+1 & sync webhooks/checks, and add OTel + valid Prometheus

### Summary

Database, API, and observability audit across all 36 migrations and `internal/{api,store,github,telemetry}/*` found critical missing indexes on hot polling paths, N+1 and secret-decryption storms, synchronous GitHub/webhook calls risking 10s timeouts, Prometheus data races & invalid exposition format, and HA scaling bottlenecks.

---

### Verified Findings (`file:line`)

#### 1. Critical Missing Database Indexes (Verified Across Migrations `0001`–`0036`)
| Table / Hot Query | Location | Missing Index & Impact |
|---|---|---|
| `job_leases` (`WHERE job_id = $1 ORDER BY created_at DESC LIMIT 1`) | `store/jobs.go:123-125, 280, 392` | **Zero index on `job_id`** (`0001_init.sql:220-228`). Full seq scan of unbounded table per candidate job on every 500ms `ClaimJob`, renew, and finish. Add `(job_id, created_at DESC)` + retention prune. |
| `org_members` (`JOIN ON user_id = u.id`) | `store/accounts.go:67, 136, 299, 443` | PK is `(org_id, user_id)` (`0001_init.sql:24`). Every auth request scans by `user_id`. Add `(user_id, created_at)`. |
| `jobs` (`ClaimJob`, `ReapExpiredLeases`, `TimeoutOverdueJobs`) | `store/jobs.go:174, 482, 551` | `jobs_queue_idx` misses `priority DESC` & GIN on `runs_on`; add partial indexes on `(lease_expires_at)` and `(started_at)` `WHERE status = 'in_progress'`. |
| `cache_entries` (`starts_with(key, $2)` + 2s sweep) | `store/cache.go:61, 93-95`, `orchestrator.go:299` | Needs `(project_id, scope, key text_pattern_ops, created_at DESC)` + indexes on `last_used_at` and `expires_at`. |
| `artifacts` (2s expiry sweep) | `store/artifacts.go:108`, `orchestrator.go:291` | Unindexed `expires_at` full-scanned every 2s. |
| `workflow_runs` (`RunPrefix`, concurrency, SHA, workflow) | `store/runs.go:269, 295, 757-812` | `id::text LIKE $1 || '%'` full-scans (replace with UUID min/max range); add `(project_id, concurrency_group, status, created_at, id)`, `(sha)`, `(workflow_id, created_at DESC)`. |
| `workflow_versions` (`ORDER BY created_at DESC LIMIT 1`) | `store/projects.go:124-128, 205`, `store/schedules.go:33` | Add `(workflow_id, created_at DESC)`. |
| `steps` (`StepMedians`, histogram, `PruneStepBlobs`) | `store/runs.go:562`, `store/metrics.go:78-81`, `store/jobs.go:585` | Add partial indexes on `(completed_at)` and `WHERE log_ref <> ''`. |
| `projects`, `webhook_deliveries`, `deployments`, `sessions`, `api_tokens` | `store/projects.go:62`, `store/webhooks.go:58, 78, 128`, `store/environments.go:274, 461` | Add `LOWER(projects.repo_full_name)`, `webhook_deliveries(received_at DESC)`, `deployments(run_id)`, partial `deployments(status, wait_until)`, `sessions(user_id)` (+ session prune), `api_tokens(user_id)`. |

#### 2. N+1 Queries, Decryption Storms & Hot Polling
- **Log-ingest secret decryption storm** (`handlers_runner_agent.go:505-529, 564-589, 617-621`): every 1s log chunk runs 6 SQL queries (`Job` + `StepsForJob` + `Run` + `ProjectByID` + `SecretsForProject` + `SecretsForEnvironment`) and AES-GCM decrypts every secret from scratch; `finishStep` calls `maskerForJob` twice (8+ queries). Fix: cache per-job `secrets.Masker` with short TTL + eviction on job finish, and use a lightweight ownership check in `runnerJob`.
- **N+1 / 2N+1 read amplification**:
  - `JobsForRun` (`store/runs.go:485-491`) runs 1 `StepsForJob` query per job (replace with `WHERE job_id = ANY($1)`); `kern_get_run` (`handlers_mcp.go:290-299`) calls `StepsForJob` a **second** time per job (`2N+1`).
  - `listRunners` (`handlers_runners.go:39-45`) & `kern_list_runners` (`handlers_mcp.go:338-341`) run 1 `RunnerActiveJobs` query per runner.
  - `CacheLookup` (`store/cache.go:55-69`) loops 1 SQL query per restore-key prefix.
  - `getRun` (`handlers_runs.go:205-232`) runs fleet-wide `UnmatchedQueuedJobs` / `ConcurrencyCappedJobs` even on completed runs.
  - `cancelCheck` (`handlers_runner_agent.go:859-877`) loads full `Job` + all `Steps` twice per poll.
  - `api_tokens.last_used_at` (`store/accounts.go:454`) writes synchronously on every API request; `ExpireStaleExecs` (`handlers_runner_agent.go:243`) sweeps table on every runner heartbeat; `artifacts` upload (`store/artifacts.go:47-58`) locks project row `FOR UPDATE` + full `SUM(size_bytes)`.
- **Replace hot polling with Postgres `LISTEN/NOTIFY` push**:
  - Idle workers fire `ClaimJob` every **500 ms** (`handlers_runner_agent.go:312-335`) against a hardcoded 16-conn pool (`store.go:30`). Wake parked claim handlers on `LISTEN kern_jobs_ready` (signaled on `SetJobQueued`/`RequeueJob`/`FinishJob`) with empty-queue short-circuit.
  - SSE log viewers (`handlers_runs.go:456-490`) poll DB + 2 S3 RPCs every 400 ms per tab; replace with in-process + `NOTIFY` log-append pubsub.

#### 3. Synchronous Webhooks, GitHub Check-Run Bugs & HA Bottlenecks
- **Synchronous webhook processing** (`handlers_misc.go:143-192`, `handlers_webhook.go:63-234`, `store/webhooks.go:15-35`): decrypts all DB GitHub App secrets per delivery and synchronously compiles workflows + posts GitHub checks inside the HTTP request (risking GitHub's 10s timeout), while `SaveWebhookDelivery` records `'received'` before processing so crashes permanently drop retries. Fix: enqueue deliveries to an async worker pool (processing/retryable state), cache decrypted App secrets, and add `singleflight` on `github.App.token`/`installation` (`github/app.go:158-209`).
- **GitHub Check Run bugs & sync blocking** (`github/checks.go:59-163`, `api/check_annotations.go:42-105`, `api/public_run.go:287-362, 400-402, 561-568`, `github/client.go:83-91, 119, 196, 249`, `api/api.go:139-143`):
  - `checkSummary` (`public_run.go:561-563`) panics on `s[:6000-len(diag)]` when `len(diag) > 6000` and slices mid-UTF-8; `checkLogExcerpt` (`401`) fails to escape triple backticks; `PostCommitStatus` (`client.go:84-86`) slices `description[:140]` by bytes; `HeadSHA`/`TreeEntries`/`FileContent` omit URL escaping; zero 429/5xx retry logic.
  - `UpsertCheckRun` sends unsanitized annotations (empty `Message`, >255 title, >64 KiB body) causing HTTP 422 that leaves checks permanently `in_progress`; `findCheckRun` doesn't paginate (`per_page=100`) and races creating duplicates; per-job checks omit annotations; file-less `::error::` annotations are dropped (`check_annotations.go:67-70`); `finishStep` synchronously runs 90-day `StepMedians` + 2 GitHub API calls.
  - Fix: persist `github_check_run_id` on `workflow_runs` & `jobs`, dispatch coalesced check updates via async per-(repo, sha) worker with 429 backoff, sanitize/truncate annotations, post annotations per job, and maintain incremental rolling step duration summaries instead of live 90-day `percentile_cont`.
- **Multi-API HA bottlenecks** (`store/store.go:29-30`, `store/leader.go:23-55`, `orchestrator.go:247-391`, `middleware.go:272-307`): make DB `MaxConns` configurable; add bounded timeout to `LeaderSession.Release()`; split maintenance/notify/scheduler loops so 200s of slow webhook retries cannot starve `Tick()`; bound/evict `loginLimiter` map keys (and support DB/shared rate-limit backend).

#### 4. Prometheus Data Race, Invalid Exposition & Native OpenTelemetry
- **Bugs**: `api/metrics.go:99-111` returns raw `*histogram` pointers after `m.mu.Unlock()`, racing `observe*` vs `write` (`handlers_misc.go:134-135`); unsorted map iteration (`telemetry/metrics.go:134-156`) and interleaved runner families (`handlers_misc.go:93-114`) violate Prometheus 0.0.4 contiguous family grouping; `/metrics` runs 7 heavy DB queries synchronously per scrape behind user auth (`api.go:266`).
- **Enhancement**: Adopt `prometheus/client_golang` on a dedicated scrape listener/token backed by an async background collector; instrument OpenTelemetry distributed tracing (`traceparent` persisted on runs/jobs and propagated to `kern-runner`) plus structured job phase spans (`queue_ms`, `image_pull_ms`, `checkout_ms`, `services_ms`, `cache_restore_ms`, `exec_ms`) and DAG critical-path calculation.

### Acceptance Criteria
- [ ] Migration adds all missing indexes above; `EXPLAIN ANALYZE` confirms zero seq scans on `ClaimJob`, `job_leases`, `org_members`, `cache_entries`, `artifacts`, and `RunPrefix`.
- [ ] `ClaimJob` and SSE log streaming wake via `LISTEN/NOTIFY` instead of tight polling loops.
- [ ] `maskerForJob` is cached per job; N+1 queries in `JobsForRun`/`listRunners`/`kern_get_run` eliminated.
- [ ] Webhooks and GitHub Check Runs process asynchronously with `github_check_run_id` persistence, sanitized per-job annotations, and 429 retry.
- [ ] `go test -race ./...` passes on metrics; Prometheus exposition passes `promtool check metrics`; OTel traces + job phase timings exported.


## Issue #375 — CLI Bugs & Contract: Fix global flag stripping, 60s timeout kills, log truncation, testbox host-container reaping, short-ID traps, --json & OpenAPI/SKILL drift

### Summary

Full audit of `cmd/kern/*`, `internal/runner/local.go`, `internal/api/handlers_logsearch.go`, `openapi.yaml`, and `skills/kern-ops/SKILL.md` identified broken commands, silent truncation, a dangerous `kern testbox` container-reaping bug, inconsistent `--json`/exit-code contracts, and widespread spec/documentation drift.

---

### Verified Findings (`file:line`)

#### 1. Broken Commands & Dangerous Edge Cases (Critical / High)
- **`extractGlobals` strips `--server` and `--project` before subcommands read them** (`cmd/kern/main.go:227-261, 283-288`):
  - `kern install --server <url>` (`cmd_auth.go:38-61`) reads empty `opts["server"]` instead of `c.server` — fails on fresh setup and overwrites with old server on existing configs.
  - `kern login --project <slug>` (`cmd_auth.go:65-105`) reads empty `opts["project"]`, never persisting default project.
  - `kern logs --search Q --project SLUG` (`cmd_projects.go:313-316, 610-617`) passes empty project, silently searching the whole org.
  - Global flag & `--help` scanning never stops at `--`, hijacking remote flags in `kern ssh <id> ls --help` or `curl --json ...`.
- **`kern ssh -c CMD` broken + raw-mode terminal corruption** (`cmd/kern/cmd_ssh.go:27-29, 149-159, 170-175`, `cmd/kern/cmd_auth.go:304`, `internal/runner/exec.go:67`, `internal/api/handlers_exec.go`):
  - `parseFlagsEx` only matches `--` prefixes, so documented `-c CMD` (`main.go:52`) is passed literally to `sh -c "-c <cmd>"`.
  - `pumpSSHStdin` enables `term.MakeRaw` in a goroutine even though `runExec` runs `docker exec -i` without a PTY (`-t`), breaking Enter (`` vs `
`), echo, and SIGINT, and `streamExec` exits via `os.Exit` without waiting for `term.Restore` (corrupting the user's terminal).
  - Ctrl-C exits locally with no server-side cancel endpoint (`DELETE /api/v1/exec/:id`), orphaning the remote `docker exec`.
- **`kern testbox` kills unrelated host Docker containers & runs privileged by default** (`cmd/kern/cmd_testbox.go:61, 74, 79-89, 170-175`, `internal/runner/executor.go:591-593, 639, 757-765, 939-946, 968-970, 1168, 1201-1216`):
  - `Concurrency: 0` triggers `reapOrphanContainers`, running `docker rm -f` on **any** unrelated container created on the developer's machine while `testbox` ran.
  - Hardcoded `TrustLevel: "trusted"` runs as root with `--network host`, `/var/run/docker.sock`, and mutates `/github/workspace`.
  - On failure, `os.Exit(1)` (`87-89`) bypasses `defer os.Remove(bundle)` and `defer os.RemoveAll(workRoot)`, leaking `/tmp/kern-*`.
  - `loadEnvFile` (`186-203`) keeps literal quotes and fails on `export KEY=VAL`; `localShim` (`local.go:77-79`) writes unsynchronized logs with no step headers; hardcoded `Event: "testbox"` / `Ref: "refs/heads/testbox"` breaks `if: github.event_name == ...` conditions; no CLI override for 1g/1CPU limits.
- **Hard 60s `dispatch` timeout kills long-running commands** (`cmd/kern/main.go:276`, `cmd_projects.go:383-428, 530-606`, `cmd_fleet.go:280-297`): kills `kern runs wait --timeout 30m`, `kern logs -f` (which also uses uninterruptible `time.Sleep(800ms)` at `596`), and large `kern artifacts download` after 60s.
- **Silent 256 KiB log truncation in `kern logs` & `kern exec`** (`cmd/kern/cmd_projects.go:531-549, 593`, `cmd/kern/cmd_exec.go:173-188`):
  - Non-followed `kern logs` / `runs logs --out DIR` exits after 1 request (server cap 256 KiB at `handlers_runs.go:378`).
  - `streamRun` exits as soon as run status is terminal without draining remaining step chunks and swallows log fetch errors.
  - `runLogs` snapshots steps once — fails with `no steps matched` on newly queued runs and blocks parallel jobs sequentially.
- **`kern runs wait` exits `0` on failed/cancelled runs** (`cmd/kern/cmd_projects.go:406-418`), breaking CI/agent scripting.
- **Other verified CLI/API bugs**:
  - `kern optimize` (`cmd_optimize.go:36`) skips `c.resolve()`, silently disabling runner label validation unless `--server` is passed.
  - `kern exec --pull` (`cmd_exec.go:202, 219`, `grant.go:215-219`) calls `GET /artifacts?limit=500` without `run_id` (403 on project-scoped tokens, misses >500 history) and joins `a.Name` without `filepath.Base`; `triggerRunByPath` (`handlers_projects.go:551-557`) pollutes the permanent `workflows` table with ad-hoc files; `bundleDir` (`cmd_exec.go:270-304`) leaks temp tarballs on error.
  - `kern lint <file>` (`cmd_lint.go:42, 87, 132, 148-155`, `cmd_optimize.go:61`) passes the file path as `repoRoot` (breaking `./` reusable lookup) and shells out to non-portable `base64 -d` instead of `encoding/base64`.
  - `kern migrate` (`cmd_migrate.go:102`) prints wrong command `kern secrets set <slug> NAME` (real syntax: `kern secrets set NAME --project <slug>`).
  - `kern logout` (`cmd_auth.go:114-117, 258, 270`) revokes stale `TokenID` after `config set token`; `kern config list` iterates unsorted map; `kern config get token` prints secret unmasked.
  - `searchLogs` (`handlers_logsearch.go:52-55, 92-121`): `bufio.Scanner` silently aborts on >256 KiB lines; `text[:300]` corrupts multi-byte UTF-8; `limit <= 0` escalates to 200; `store.Runs` (`store/runs.go:370-381`) count query omits `f.Ref`, `f.PR`, `f.Workflow`.
  - Minor papercuts: `showRun` prints `cpu 0% · mem 0B` on `{}`; `cmdDiagnose` makes 2 calls per step despite `st.LogBytes` and prints `last 8.0K of 300B`; `cmdSecrets` lacks `url.PathEscape(name)` & `--environment`; `artifacts download` leaves 0-byte files on error; `runners enroll` (`cmd_fleet.go:185`) prints unstamped `kern-runner:local`.

#### 2. Contract & Documentation Drift (Medium)
- **Short-ID UX trap**: `artifacts list` & `cache list` print 8-char `shortID(...)`, yet `artifacts download/delete`, `artifacts list --run`, `cache delete`, `kern ssh`, `runs remediate`, `deployments approve/reject`, and all MCP tools require full UUIDs; `runIDParam` (`handlers_runs.go:126-135`) accepts 4-32 hex while claiming `"8+ char prefix"` and rejects hyphenated prefixes.
- **Missing `--json` & inconsistent exit codes**: `--json` is ignored by `optimize`, `login`, `logout`, `token delete`, `config`, `runners drain|undrain|self-update|wake|delete|install`, `artifacts download|delete`, `cache delete`, `settings set`, `secrets set|delete`, `vars set|delete`, `environments delete`, `runs remediate`, `runs logs`, `hooks install`, `testbox`, `ssh`, and `version`. `kern status --watch --json` emits invalid concatenated multi-line JSON and repeats version-skew warnings every 3s.
- **Completions, usage, OpenAPI & SKILL drift**: `cmd_completion.go` omits `ssh`/`testbox`/`optimize`; `main.go` usage has stale `projects create` & missing subcommands; `openapi.yaml` omits 14 live routes (`/logs/search`, `/runs/day-counts`, `/jobs`, `/runs/clear-cancelled`, `/runners/:id/auto-update`, 7 `/exec` routes, `/vars*`, `/ghcache/*`, `/cache-dl/:token`) and mismatches `Runner.last_seen_at` vs `last_heartbeat_at`; `skills/kern-ops/SKILL.md:22-24` omits required args on `self-update`/`wake`/`logs -f`.

### Acceptance Criteria
- [ ] All flag-parsing, `--` terminator, 60s timeout, exit-code, log pagination, `ssh` PTY/restore/cancel, `testbox` safety, `lint`, `optimize`, `exec`, `migrate`, `logout`, and `searchLogs` bugs fixed with tests.
- [ ] Consistent short-ID resolution across all CLI, REST, and MCP ID parameters.
- [ ] Every CLI command honors `--json` (with NDJSON on streaming/`--watch` modes) and consistent exit codes (`0` ok, `1` runtime/CI failure, `2` usage).
- [ ] `cmd_completion.go`, `main.go`, `openapi.yaml`, and `skills/kern-ops/SKILL.md` synced (with a CI check guarding OpenAPI/route parity).


## Issue #376 — CLI v2 Power-User Suite: Multiplexed SSE live watch & TUI, warm local testbox v2, rich diagnose, batch fleet rollout, profiler/LPT sharding, init & upgrade

### Summary

Elevate `kern` into a genuinely superior, ultra-fast self-hosted CI CLI that outperforms `gh` / `act` for both daily developer iteration and fleet operations.

---

### Proposed Capabilities (Grounded in Current `cmd/kern/*`)

#### 1. Multiplexed SSE Live Watch & Interactive TUI (`kern watch`, `kern logs -f`, `kern top`)
- Server already streams step logs via SSE (`GET /api/v1/steps/:stepID/logs/stream`, `handlers_runs.go:427-491`), yet CLI polls every 700-800ms (`cmd_exec.go:192`, `cmd_projects.go:596`).
- Add a run-level multiplexed SSE stream (`GET /api/v1/runs/:id/stream`) pushing job/step state transitions + interleaved log chunks so `kern logs -f`, `kern exec`, and `kern runs watch <id>` display parallel jobs in real time with colored `[job/step]` prefixes and zero poll lag.
- Upgrade `kern status --watch` (and add `kern top`) with clean alternate-screen rendering (caching `/meta` once, fetching `/overview` & `/runners` in parallel), NDJSON output under `--json`, and keyboard jump from failed run -> failed step logs -> rerun/cancel/ssh.

#### 2. Fast Local Runner v2 (`kern testbox` + `kern plan` / `--dry-run` + Step-Level Re-Run)
- Upgrade `RunLocalJob` (`internal/runner/local.go:41-66`) into a lightweight local DAG runner honoring `needs:`, matrix selection (`--matrix key=val`), job outputs, `--event`, `--ref`, `--var`, `--input`, `--memory`, `--cpus`, and `--sandbox` (safe unprivileged default, never reaping host containers).
- Add `kern plan` / `kern testbox --dry-run`: compile workflow and print expanded DAG, resolved expressions, images, services, required secrets/vars, and matching fleet runners without starting containers.
- Add warm iteration flags (`--keep-container`, `--step N`, `--from-step N`), bind-mount / CoW workspace (skipping `bundleDir` re-tarring), and a persistent `~/.cache/kern/testbox/_cache` wired into `localShim` (`local.go:104-109`) for sub-second single-step repro loops.

#### 3. Structured `kern diagnose` & Interactive Failure Explorer
- Back `cmdDiagnose` (`cmd/kern/cmd_diagnose.go:54-90`) with a single server endpoint (`GET /api/v1/runs/:id/diagnose`) instead of `2 + 2*N` sequential HTTP calls.
- Include `steps.annotations` (`file:line:col`), `GITHUB_STEP_SUMMARY`, `jobs.metrics` peaks vs cgroup limits, `queue_reason`, extracted compiler/test failure frames (Go `--- FAIL:`, Vitest/Playwright, Rust `error[E...]`), and regression diff against the last green run on the same workflow/branch (highlighting new vs recurring flake + runner version delta).
- Support `--format text|json|md|prompt` and an interactive TUI explorer (`kern diagnose -i <run-id>`).

#### 4. Batch Fleet Operations, Rolling Rollout & Missing Runner CLI Commands
- Extend `kern runners drain|undrain|self-update|wake|delete` (`cmd_fleet.go:93-163`) to accept multiple targets plus selectors: `--all`, `--label <lbl>`, `--outdated` (using `fleetNewestVersion`), `--offline`, `--dry-run`.
- Add `kern runners rollout [--label L] [--max-unavailable 1]` (rolling drain -> wait `active_jobs==0` -> trigger update -> verify version bump -> undrain).
- Expose already-built API endpoints missing from CLI: `kern runners env get|set|unset <runner>` (`PATCH /runners/:id` at `handlers_runners.go:118-128`) and `kern runners jit [--name N] [--labels a,b]` (`POST /api/v1/runners` at `handlers_runners.go:251-289`).

#### 5. Critical-Path Profiler, Flamegraph & Duration-Balanced Sharding (`kern optimize`, `kern split`)
- Upgrade `kern optimize` (`cmd/kern/cmd_optimize.go:17-119`) beyond its 4 static heuristics by querying historical run/step timings, queue wait, cache hit/miss ratios, and `jobs.metrics` peaks:
  - Render critical-path waterfall and Speedscope JSON flamegraph (`kern optimize --profile [run-id] --flamegraph out.json`).
  - Recommend `KERN_JOB_MEMORY`/`KERN_JOB_CPUS` right-sizing, missing caches on slow installs, flaky/retried steps, and optimal `strategy.split: N` factoring checkout/boot overhead.
- Upgrade `kern split` (`cmd/kern/cmd_split.go:15-78`) with `--timings <junit|go-test-json|playwright-json>` for Longest-Processing-Time (LPT) duration-balanced sharding (falling back to FNV-32a for unseen tests).

#### 6. Zero-Config Repo Bootstrap (`kern init`) & Self-Updating CLI (`kern upgrade`)
- `kern init`: inside any git repo, detects remote + default branch, creates/links project & syncs workflows, statically scans `.github/workflows/*.yml` for referenced `secrets.*`, `vars.*`, `runs-on:` labels, and `environment:` names, diffs against the server, optionally imports from `--env-file`, writes `.kern.toml`, and optionally installs git hooks.
- `kern upgrade [--check] [--version vX.Y.Z]`: reuses KERN's signed release manifests (`internal/update` + `GET /api/v1/instance/update`) to verify SHA-256 + minisign signature and atomically replace the `kern` binary (eliminating CLI/control-plane version skew).

### Acceptance Criteria
- [ ] `kern logs -f` and `kern runs watch` stream over SSE with zero poll lag and support parallel job prefixes.
- [ ] `kern testbox` supports `needs:` DAG, `--dry-run`, warm cache, and `--from-step` re-runs in sandboxed mode.
- [ ] `kern diagnose` uses single-request structured endpoint with `--format text|json|md|prompt`.
- [ ] Batch runner selectors, `runners rollout`, `runners env`, `runners jit`, `optimize --profile`, `split --timings`, `kern init`, and signed `kern upgrade` ship with unit/CLI tests.


## Issue #377 — Agentic CI & MCP Superpowers: Streamable HTTP MCP (2025-03-26/06-18), annotations/resources/prompts, failure bundles, sandboxed repro/exec, scoped agent tokens & autonomous CI-fixer loop

### Summary

KERN already ships a built-in MCP server (`POST /api/v1/mcp`), deterministic diagnosis, and token grants — putting it a step away from being the premier agentic-powered self-hosted CI. Closing verified MCP protocol/bug gaps and adding agent-native primitives (single-call failure bundles, sandboxed pre-push repro, ephemeral scoped agent tokens, and event-driven CI-fixer dispatch) turns KERN into a closed-loop CI platform built for coding agents.

---

### Verified MCP Bugs & Protocol Gaps (`internal/api/handlers_mcp.go`, `api.go:267`)
- **Transport non-compliance**: advertises `protocolVersion: "2025-06-18"` (`handlers_mcp.go:26`), yet mounts only `POST /api/v1/mcp` — missing `GET` (SSE / 405), `DELETE`, `Mcp-Session-Id`, `MCP-Protocol-Version` header & `initialize` negotiation, `Origin` DNS-rebinding check (`cors` at `api.go:397` omits MCP headers), JSON-RPC batch arrays, `jsonrpc: "2.0"` validation, proper HTTP status on parse error (`65`), `"id": null` notification bug (`69`), and `serverInfo.version` hardcoded to `"1"` (`77`) instead of `version.Version`.
- **Missing annotations, structured outputs, resources & prompts**: `mcpTool` (`56-60`) omits `annotations` (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) — preventing agent clients from safely auto-approving read-only tools — and omits `outputSchema`/`structuredContent`, `resources`, `prompts`, and pagination.
- **Handler bugs**:
  - `kern_trigger_run` (`389-436`) accepts undocumented `trust_level`, skips `resolveTriggerSHA`/`workflowAtSHA` (runs stale sync-time YAML/SHA), and skips `enforceDispatchLabels`.
  - `kern_rerun_run` (`459-476`) drops `ConcurrencyGroup`, skips `ClearRun`, and lacks `failed: true` / `jobs[]` in-place rerun.
  - `kern_step_log` (`308-328`) breaks on `tail <= 0`, discards `total_size`/`next_offset`, and returns `isError: true` on skipped/cancelled steps.
  - `kern_get_run` (`297`) and `kern_list_runners` (`339`) swallow DB errors; `kern_approve_run` (`568`) returns plain text instead of JSON; line `584` checks `"no rows"` (dead check against `store.ErrNotFound = "not found"` at `accounts.go:15`) and leaks raw DB/FS error strings and host paths verbatim; `mcpArg*` (`201-244`) re-unmarshals `req.Params` on every field lookup.
  - `grantRouteAllowed` (`grant.go:161-173`) blocks `/api/v1/mcp` and `/api/v1/logs/search` for all project-scoped tokens, making least-privilege per-project agent tokens impossible.

---

### Agentic-First Upgrade Plan

#### 1. Spec-Compliant Streamable HTTP MCP + Full Tool, Resource & Prompt Surface
- Mount `POST`, `GET` (SSE notifications/progress), and `DELETE` on `/api/v1/mcp`; negotiate `2025-03-26` / `2025-06-18`; enforce `Origin`, `Mcp-Session-Id`, `MCP-Protocol-Version`, JSON-RPC batching, and `notifications/cancelled`.
- Add `ToolAnnotations` + `outputSchema`/`structuredContent` to every tool.
- Add **MCP Resources**: `kern://fleet/status`, `kern://projects/{slug}/workflows`, `kern://runs/{id}/diagnose`, `kern://runs/{id}/jobs/{job}/steps/{n}/log`, `kern://runs/{id}/jobs/{job}/steps/{n}/summary`.
- Add **MCP Prompts**: `triage_failed_run`, `fix_pr_ci`, `optimize_repo_ci`, `quarantine_flaky_runner`.
- Expand MCP tools for full operational parity: `kern_diagnose_run`, `kern_search_logs`, `kern_wait_run` (streaming MCP `notifications/progress` over SSE until terminal!), `kern_sync_project`, `kern_list_artifacts`, `kern_remediate_job`, `kern_approve_deployment`, `kern_doctor`, `kern_optimize_project`, and `kern_predict_run(project, changed_files[])`.

#### 2. Single-Call Token-Budgeted Failure Bundles (`kern_diagnose_run` + `GET /api/v1/runs/:id/diagnose`)
- Replace 4-10 agent round trips with one bounded (`max_tokens`/`max_bytes`, default ~16 KB) structured payload:
  - `verdict`: `{class: "code"|"infra"|"flake"|"config", retryable, confidence, summary}` combining `diagnosis.go` + runner drift (`missing_tools`, `claims_paused`, version skew, OOM/disk).
  - `run_context`: repo, PR #, base/head SHA, last-green SHA for workflow/branch, workflow path.
  - `failures[]`: per failed job/step — parsed `annotations` (`file:line:col`), ANSI-stripped deduplicated error frame + bounded tail, `step_summary`, `metrics` peaks vs ceilings, and allow-listed `remediation` commands.
  - `suggested_next_actions[]`: machine-callable MCP tool calls (e.g. `kern_rerun_run(failed=true)` for transient infra flakes vs exact `file:line` targets for code fixes).

#### 3. Safe Sandboxed Remote Execution & Pre-Push Repro over MCP
- **`kern_exec_repro`**: accepts `{project, ref, sha, patch_diff | files, workflow_or_command, job, timeout_seconds}` and runs an ephemeral, **non-persisted** job forced to `TrustLevel: "fork"` (read-only rootfs, dropped caps, no `docker.sock`, egress-filtered, zero production secrets unless explicitly granted) so an agent validates candidate fixes in the exact CI container before pushing.
- **`kern_job_exec`**: wraps live exec (after fixing `internal/runner/exec.go:100` deadlock & offset truncation) behind an explicit per-token `exec` capability, hard timeout (default 30s), output byte cap, secret masking, and full audit logging.

#### 4. Scoped Ephemeral Agent Tokens & Autonomous PR CI-Fixer Loop
- Fix `grant.go:161-173` so project-scoped tokens work on `/api/v1/mcp` and `/api/v1/logs/search`, enforcing `org_id` + `projects` + `labels` uniformly inside every MCP tool.
- Extend `api_tokens` (`handlers_auth.go:216-297`) with: sub-day `ttl_minutes` (15m–4h), `allowed_tools[]`, `refs[]` patterns (`refs/pull/42/*`), `max_runs_per_hour`, and CLI preset `kern token mint-agent --project P --pr 42 --ttl 60m --profile ci-fixer`.
- Add an **Agent Dispatch Hook** (`agent_webhook_url` + SSE stream `GET /api/v1/events/stream`) emitting signed `ci.failure.actionable` events **only** on actionable code/config failures (suppressing transient infra flakes until retries exhaust), embedding the failure bundle + an auto-minted short-TTL PR-scoped MCP token for orchestrators (T3 Code, Claude Code, Codex, Devin, custom bots).

### Acceptance Criteria
- [ ] All verified `handlers_mcp.go` bugs fixed; Streamable HTTP (`POST`/`GET`/`DELETE`), `ToolAnnotations`, `structuredContent`, Resources, and Prompts pass MCP inspector/conformance tests.
- [ ] `kern_diagnose_run` returns a complete token-budgeted failure bundle in 1 call.
- [ ] Project/label/tool/ref/TTL-scoped agent tokens work cleanly over `/api/v1/mcp` and cannot escalate outside their grant.
- [ ] `kern_exec_repro` validates patches safely in `fork` sandbox without polluting `workflows`; `ci.failure.actionable` fires on non-transient failures.


## Issue #378 — Web UI/UX: Fix broken rerun/undrain/log-truncation/SSE bugs, eliminate 150-request fan-out storm, improve accessibility & polish run/fleet UX

### Summary

Direct source audit of `apps/web/src` (`routes/{run-detail,runners,runs,overview,public-run}.tsx`, `components/{layout,log-view,gantt}.tsx`) uncovered several broken operator workflows, silent log truncation & SSE stream freezes, a 150-request HTTP fan-out storm on page load, accessibility gaps, and high-value UX polish opportunities.

---

### Verified UI/UX Bugs (`file:line`)

#### 1. Broken Operator Actions & Silent Errors (High)
- **Run detail "Re-run" button gives zero feedback / never navigates** (`apps/web/src/routes/run-detail.tsx:41-43, 127-129`): `rerun` mutation has no `onSuccess` (unlike `runs.tsx:372-375` which navigates to the new run and invalidates `["runs"]`). There is also no "Re-run failed jobs only" button (`{"failed": true}`), and `rerun.error`, `cancel.error`, and `approve.error` are never displayed.
- **No way to undrain a runner in the Web UI** (`apps/web/src/routes/runners.tsx:42-45, 257-264`): `drain` hardcodes `{ draining: true }` and disables the button once `r.draining` is true — operators can drain a runner in the UI, but must drop to CLI/API to undrain it.
- **Finished step logs silently truncate at 256 KiB; live SSE permanently dies on transient errors** (`apps/web/src/components/log-view.tsx:131-165`):
  - For finished steps (`!active`), `LogView` issues a single `fetch('/api/v1/steps/:id/logs')` (server default 256 KiB cap), truncating larger logs with no warning, pagination, or "Load full log" button, and silently swallows `!res.ok` errors.
  - For live steps (`active`), `es.onerror` (`163-165`) immediately calls `es?.close()`, permanently freezing the log stream on any transient network blip instead of reconnecting from `lastOffset`.
- **Tab switching wipes active project & day filters** (`apps/web/src/routes/runs.tsx:145`): `setParams(t.key ? { tab: t.key } : {})` drops `?project=` and `?day=`. Branch filter (`35, 99`) is client-side only over loaded pages and not synced to URL/API `?ref=`.
- **Auto-selection misses failed job & failed step** (`apps/web/src/routes/run-detail.tsx:68-69, 211-214`): defaults to `jobs[0]` and `steps[steps.length - 1]` instead of focusing the first failed job and failed step on red runs.
- **Runner UI defects** (`apps/web/src/routes/runners.tsx:95, 169, 270, 353, 444-446, 496-499`):
  - Slot squares (`169`) render `Math.min(8, Math.max(4, r.cpus))` instead of real worker `concurrency` and style all non-running slots as `j` whenever 1 job is busy.
  - `EnrollCard` (`353`) generates `kern-runner:local` + `-e KERN_RUNNER_CONCURRENCY=1` (unstamped `dev` image breaks self-update).
  - `EnvEditor` (`444-446`) silently aborts form submission with zero error message if a line lacks `=`.
  - Destructive deletes (`95, 270`) use blocking browser `window.confirm(...)`.
  - `GeoEditor` (`496-499`) calls public `nominatim.openstreetmap.org` directly from the browser (breaks air-gapped/strict-CSP deployments and leaks user IP).
- **Hardcoded visual placeholders**: `runs.tsx:332` Cache sidebar meter hardcodes `width: "100%"`; `overview.tsx:271-287` hardcodes job legend swatches (`lint`, `build`, `web`) unrelated to actual repo jobs.

#### 2. Performance Bottlenecks (High)
- **Up to 150 parallel `GET /runs/:id` requests on `/runs` load** (`apps/web/src/routes/runs.tsx:17, 100-101`, `apps/web/src/routes/overview.tsx:119-124`): `useRunDetails` fans out one full detail request (with all steps + fleet diagnostic scans) per visible run row just to paint `<Fingerprint>` strips. Fix: embed lightweight job status/duration summaries in `GET /api/v1/runs` (or a single batch endpoint).
- **O(N^2) log re-parsing & unvirtualized DOM** (`apps/web/src/components/log-view.tsx:174-206, 311-327`): every SSE chunk re-strips ANSI, re-splits, and re-classifies the entire accumulated log string and renders every line DOM node without virtualization, causing jank on 10k+ line CI logs.

#### 3. Accessibility & UX Polish Enhancements
- **Command Palette (`CommandK`)** (`apps/web/src/components/layout.tsx:256-386`): add `role="dialog"` / `aria-modal="true"` / focus trap, clamp `sel` when `items.length` shrinks, call `scrollIntoView` on keyboard navigation, and add quick actions (toggle theme, jump to failed runs, trigger workflow, log search).
- **Semantic interactive elements**: replace clickable `<div onClick>`s (`layout.tsx:398` `Row`, `runs.tsx:300` `fleetrow`, `overview.tsx:224` webhook banner which nests `<button>` inside `<div onClick>`) with accessible links/buttons.
- **Superior Log & Run Detail UX**:
  - Virtualize `LogView`, parse incrementally per appended chunk, render collapsible `##[group]`/`##[endgroup]` blocks (`log-view.tsx:40-42`), sync selected line to `#L<n>` URL hash, add "Jump to first error" and full-log pagination/download.
  - Make Gantt bars directly clickable (`gantt.tsx:226`), visualize queue-wait vs execution segments and critical-path highlight, sync `?job=&step=` in URL, add live 1s elapsed timer (`useNow` from `public-run.tsx:87`) on `RunDetailPage`, and add one-click **"Copy AI diagnosis bundle"** + **"Re-run failed jobs"** buttons.
  - Add bulk runner selection/actions (drain/undrain/rollout/version-skew badge) on `RunnersPage` and inline confirmation UI replacing `window.confirm`.

### Acceptance Criteria
- [ ] Clicking Re-run on `RunDetailPage` navigates to the new run (and Re-run failed works in-place); mutation errors render inline; failed runs auto-open the failing job & step.
- [ ] `RunnersPage` toggles drain/undrain cleanly, displays real worker `concurrency` slots, validates `EnvEditor` errors visibly, and avoids blocking `window.confirm`.
- [ ] `LogView` loads complete logs (>256 KiB), reconnects SSE from `lastOffset` on transient drops, virtualizes large logs, and supports collapsible `##[group]` + `#L<n>` links.
- [ ] `/runs` and `/` load without per-row `GET /runs/:id` fan-out; tab switches preserve `?project=`/`?day=`.
- [ ] `CommandK` and interactive rows meet keyboard & ARIA dialog accessibility standards.

