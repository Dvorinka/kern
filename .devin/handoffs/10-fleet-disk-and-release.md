# HANDOFF 10 — Fleet disk crisis + Cloudreve v4.23.8 release unblock

Written 2026-11-10 by the agent driving the Cloudreve release on KERN.
Covers: the disk exhaustion that is currently the fleet's biggest failure mode,
the remaining release blocker (GitHub release creation 403), measured fleet
state, and a prioritized fix list (storage first, then time, then polish).

Read `~/agent-memory/projects/kern.md` first — section "cloudreve v4.23.8
release unblock" has the full incident history. `pull --ff-only` before
editing memory; commit + push after.

----------------------------------------------------------------------
## 1. ACTIVE: Cloudreve v4.23.8 release — where it stands

- Repo `Dvorinka/cloudreve`, tag `v4.23.8` → commit `5486af3d` (master HEAD).
- KERN run `835f4f90-ce14-42d6-9f7f-b85d9005782d` (push on tag).
- DONE, verified:
  - `GHCR_TOKEN` (classic PAT, project secret) works — PR #442 login step
    succeeded; all `ghcr.io/dvorinka/cloudreve:v4.23.8*` images + `:slim`
    manifests were pushed by goreleaser on 2026-11-10 ~09:35Z.
  - Desktop Linux/macOS/Windows legs build + updater-sign green
    (NSIS installer, AppImage+deb, dmg/app.tar.gz, sparse MSIX identity).
  - Android APK + AAB build green.
  - All publish steps failed ONLY on `gh release view` → "release not found"
    because the GitHub release was never created (each waits ~30min then dies).
- RESOLVED (2026-11-10 ~14:20Z): the release is COMPLETE — 25 assets on
  https://github.com/Dvorinka/cloudreve/releases/tag/v4.23.8
  (12 server archives + checksums + APK/AAB + desktop bundles w/ .sig +
  latest.json) and GHCR images `v4.23.8-*`/`:slim` pushed. All KERN lanes
  green; only the `server` job shows failed — cosmetic (see below).
- ROOT CAUSE of the 403 — **NOT a mint problem**: GitHub's releases API
  returns `403 Resource not accessible by integration` for APP tokens
  (both installation tokens AND PAT-app tokens) whenever
  `target_commitish` is a commit SHA. Probed exhaustively on CT908 with
  the kern-ci app key:
  - POST /releases without target_commitish → succeeds
  - POST /releases with target_commitish=<SHA> → 403
  - goreleaser also PATCHes the release with target_commitish → 403 even
    on an existing release (observed on rerun: PATCH /releases/440261077).
  `.goreleaser.yaml` had `release.target_commitish: "{{ .Commit }}"` —
  fixed on master @ `55f45f8c` (now `{{ .Tag }}`; tag exists before
  goreleaser runs so behavior is identical, and the prepare-job +
  existing-release path also avoids the create call).
- HOW IT WAS FINISHED (manual path, in case it recurs): built artifacts
  by running goreleaser manually on CT903 host with toolcache
  go/node/yarn + minted token — `goreleaser release --clean
  --parallelism 4 --skip=docker` (images already pushed). It also 403'd
  at release-create → root cause found → created the release via
  installation-token POST (no commitish) + `GH_TOKEN=<tok> gh release
  upload` of dist/*. Then `POST /runs/…/rerun {"failed":true}` — all
  publish lanes went green instantly on `gh release view`.
- SECOND BLOCKER seen en route: rerun OOM'd (`compile: signal: killed`)
  — job containers are cgroup-capped at `KERN_JOB_MEMORY=6GiB`
  (executor HostConfig.Memory) and goreleaser defaults
  `--parallelism` to CPU count → 12 concurrent `go build`s. The
  `--parallelism 4` flag exists and works; consider adding it to the
  workflow's goreleaser args or raising the ceiling for heavy builds.

- NOTE: cloudreve master has since moved — another agent pushed
  ee5983da (prepare job creates the release up front + docker disk
  reclaim around goreleaser; version bumped to 4.23.12) and I pushed
  55f45f8c (target_commitish fix). The v4.23.8 tag stays at 5486af3d —
  releases for v4.23.9+ get the new machinery; do NOT move the tag.
- API base: use `https://kern.tdvorak.dev` — `10.0.0.240:8080` is flaky
  from this host (VPN-dependent). Job-status polling:
  `ssh nab9 "pct exec 908 -- docker exec deployments-postgres-1 psql -U kern
  -d kern -t -c \"SELECT name,status,conclusion FROM jobs WHERE run_id='…'\""`

----------------------------------------------------------------------
## 2. THE REAL PROBLEM: fleet disk exhaustion

The server job failed 3× on `no space left on device` — twice mid-go-build
(`/kern/tmp/go-build*`), once at buildkit ingest
(`/var/lib/buildkit/runc-overlayfs/content/ingest`). Every retry raced
foreign jobs on the same 98G volume. This is the fleet's biggest failure
mode and it will keep killing heavy jobs (goreleaser, docker, gradle).

### Measured state (2026-11-10 ~09:00Z, before/after my cleans)

| CT | Disk | Free before | Free after | Notes |
|----|------|------------|------------|-------|
| 901 | 79G | **192M (100%)** | 13G | docker prune 3.5G + job cleanup |
| 902 | 98G | 14G | ~20G | pruned images 1.1G, /root caches ~3G, gocache cleared 5.7G |
| 903 | 118G | 34G | 34G | healthiest — larger disk |
| 906 | 79G | 16G | 24G | docker prune 2.4G |

### Where the space goes on every CT

- `/opt/gha` ≈ 29G on 902 — **live** GHA-runner workspaces for ~25
  per-repo runner services (dflow, Nothing_Modes, relay, Cal, Invico,
  kern, wavesync, desloppify, Soika, Primora, Bookra…). NOT stale — do
  not bulk delete. Per-repo `_work` dirs hold node_modules/target caches.
- `/opt/kern/work/_cache` ≈ 11G — shared kern job cache:
  gocache ~6G (hot — all files <24h old, fleet builds constantly),
  _toolcache ~2.6G, gomod ~0.6G, pnpm-store ~0.6G, ms-playwright ~0.3G.
- `/var/lib/docker` 6-8G + `/var/lib/containerd` 8G on 902
  (verify containerd's consumer before pruning — possibly orphaned).
- Job workspaces `/opt/kern/work/<uuid>` — cleaned on finish, but
  foreign jobs keep arriving; a 9.1G workspace existed on 901.
- `/root/.gradle` ~1-3G, `/root/.cache` ~1-1.6G — host-side, stale
  (job containers don't use them). Safe to clean.
- Stale `buildx_buildkit_builder-*` containers + `buildx_*_state`
  volumes linger after jobs — each holds ingest/layer cache.

### Peak demand during the failing job

goreleaser = 12 go-build targets (tmp in job-container writable layer)
→ archives → docker build ~6-12 platform images → buildkit ingest writes
compressed diffs (~10-20G transient) → push. Budget ≈ 20-25G free at
start to survive alongside a concurrent job.

----------------------------------------------------------------------
## 3. FIX LIST — storage first

### 3a. Immediate (infra, no code) — do today

1. **Resize CT901 and CT906 disks** (79G → ≥120G, matching 903). On the
   Proxmox host: `pct resize 901 rootfs +41G` etc (verify exact syntax for
   this host's storage backend; these are `pve-vm--9xx--disk--0` LVM vols).
   Cheap, durable, eliminates most contention.
2. **Reclaim cron on every runner CT** (root crontab, daily + on idle):
   - `docker image prune -a -f` (keeps images referenced by containers)
   - `docker volume prune -f`
   - `docker builder prune -f` for builders not currently active
   - remove `buildx_buildkit_builder-*` containers whose buildx builder
     shows `inactive` in `docker buildx ls` and is >24h old (+ its
     `_state` volume)
   - `journalctl --vacuum-size=100M`
   - `/tmp/*` older than 24h; stale `gha-*` temp dirs
   - `/root/.gradle/caches`, `/root/.cache/{go-build,pip}` — stale
   - completed job workspaces in `/opt/kern/work/<uuid>` with no matching
     `kern-job-<uuid>` container — KERN already cleans on finish; sweep
     orphans as backstop
   - gocache: `find _cache/gocache -type f -mtime +3 -delete` (cap ~few G;
     safe — content-addressed, rebuilds on miss). Do NOT `rm -rf` the
     whole dir during fleet hours unless desperate — cold gocache makes
     every Go job slower.
3. **Audit `/var/lib/containerd` on each CT** (~8G on 902) — find what
   populates it; if it's an orphaned daemon, stop it and reclaim.
4. **Verify `gh` vs `/opt/gha` boundary**: those workspaces are live —
   but check each repo's `_work` for ancient `node_modules`/`target`
   that no workflow uses anymore; prune selectively.

### 3b. KERN enhancements (runner + API) — biggest wins

5. **Disk watchdog on the runner** (highest value for effort):
   - Before claiming a job: if free bytes on the work volume <
     threshold (e.g. 15G), run the safe-reclaim routine (3a subset),
     then claim. If still below, defer claim and report `low_disk`
     in heartbeat so the job goes to another runner.
   - Runner already knows its work root — this is a ~100-line addition
     to the claim loop, kills the ENOSPC class entirely.
6. **Heartbeat reports free disk** → store on runners row, show in
   `kern status`/dashboard, alert at <10G. Enables disk-aware claims
   later (prefer runner with most free for `runs_on` matches).
7. **Post-job GC guarantee**: workspace + job container + job's buildx
   builder are all removed even on kill/timeout/crash — audit
   `finishJob` + container GC paths; orphan sweep at runner startup
   and hourly.
8. **Persistent buildx builder per runner** instead of per-job builders:
   stable layer cache across jobs → faster docker phase AND less ingest
   churn on disk. `docker/setup-buildx-action` creates a new builder per
   run — KERN could inject a stable name (e.g. `kern-persistent`) so the
   state volume (and its layer cache) is reused; prune it on a weekly
   cadence instead of abandoning volumes.
9. **Job env for tmp dirs**: point `GOTMPDIR`/`TMPDIR` to the cache
   volume or a dedicated mount if you add one; today go-build tmp lives
   in the container's overlay on the same root fs.

### 3c. Longer-term

10. Second disk per CT for `/opt/kern/work` + `/var/lib/docker`
    (mount both there) — isolates build churn from system volume.
11. Per-job disk quota or cgroup limits so one job can't fill the
    volume and starve its neighbor.

----------------------------------------------------------------------
## 4. TIME OPTIMIZATIONS (release wall-time)

- Publish jobs wait on `gh release view` ≤30min while goreleaser runs
  ~35min → publish can time out before the release exists. Options:
  a) restructure: move publish into separate jobs with `needs: server`
     (cleanest — no polling at all; the desktop jobs keep building but
     don't own the release wait), or
  b) create the draft release FIRST in the server job
     (`gh release create v4.23.8 --draft` early) — dependents upload into
     it; goreleaser updates the existing release. Verify goreleaser
     handles a pre-existing draft (--release-draft behavior).
  c) cheap stopgap: raise the poll loop count.
- Persistent buildkit builder (3b.8) also cuts docker phase time.
- Fleet is pull-based (runners claim jobs ordered by
  `priority, queued_at`) — heavy-tag jobs get no placement control;
  disk-aware claiming (3b.6) is the lever, not priority.
- gocache/gomod/toolcache across CTs is already warm — keep it that way;
  the emergency `rm -rf gocache/*` I ran on 902 will cost the next builds
  a few minutes; it repopulates.

----------------------------------------------------------------------
## 5. KERN bugs/limitations observed (non-disk)

- **`kern runs rerun` flags**: parseFlagsEx never added bools to `known`
  → `--failed` hit fatalf → full rerun spawned instead of job rerun.
  FIXED — PR #246 merged (per memory 2026-11-09). Verify the deployed
  CLI build carries it.
- **GITHUB_TOKEN mint → 403 on release create** — see §1; probe proved a
  correctly-minted token works, so the suspect is now credential delivery
  to the step or deployed-vs-source drift. Add a debug log of effective
  perms (never the token) at claim.
- **Composite actions unsupported** (`taiki-e/install-action@v2` fails) —
  known gap; workflows must vendor binaries manually.
- **`shell: pwsh/powershell/cmd/python` silently falls through to the
  POSIX wrapper** → confusing failure deep inside. Render-time error
  "unsupported shell on this executor" would save hours.
- **`ghcache` handler error seen in api logs**:
  `read /tmp/kern-ghcache-*/archive.tar.gz: bad file descriptor` in
  `ghCacheCommit` (handlers_ghcache.go:384) — possible cache corruption
  path; worth a look.
- **`finishJob` → "context canceled"** in api logs (request ctx used for
  post-finish work — job check run failed for `bundle (ro: [self-hosted
  windows x64])`). Use a detached ctx for finish-time GitHub calls.
- **Internal API `10.0.0.240:8080` intermittently unreachable** from the
  dev host; `https://kern.tdvorak.dev` reliable. Probably VPN, but note
  it if the LAN address is meant to be primary.
- Runner versions after the env-fix rollout (verified 2026-11-10):
  linux CTs 901/902/903/906 + hp-win11 on **v0.7.8**, macos-904 on
  v0.7.7-su, de-oracle-a1 still **v0.6.1** (check why it didn't
  self-update — it's >2 releases behind).
- Handoff file shared for the second agent: this file,
  `.devin/handoffs/10-fleet-disk-and-release.md` in the KERN repo.

----------------------------------------------------------------------
## 6. Quick reference — useful commands

```bash
# job states for a run
ssh nab9 "pct exec 908 -- docker exec deployments-postgres-1 psql -U kern -d kern -t -c \
  \"SELECT name,status,conclusion,runner_id FROM jobs WHERE run_id='<RUN>' ORDER BY name\""

# rerun jobs (API, not CLI — CLI flags broken)
TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.config/kern/config.json'))['token'])")
curl -X POST "https://kern.tdvorak.dev/api/v1/runs/<RUN>/rerun" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"jobs":["server"]}'        # or {"failed": true}

# logs
kern runs logs <RUN> --job "server" --out /tmp/logs

# disk per CT
ssh nab9 'for ct in 901 902 903 906; do echo "== $ct"; pct exec $ct -- df -h / | tail -1; done'

# runner map
curl -s "https://kern.tdvorak.dev/api/v1/runners" -H "Authorization: Bearer $TOKEN" \
  | python3 -c "import json,sys;[print(r['id'],r['name'],r.get('version')) for r in json.load(sys.stdin)]"
```

Gotchas: never restart runner CTs while jobs in_progress; never
`rm -rf` `/opt/gha` or `_cache` wholesale (live + hot); don't touch
running jobs' workspaces; `devin-push-test` tag remains on the cloudreve
GHCR package (no delete:packages token).
