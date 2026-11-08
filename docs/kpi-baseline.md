# KPI baseline — 2026-11-08

First measured baseline after the observability work (PR #80/#82) reached
production. Scraped from host (`https://kern.example.com`, commit
`84fc9e4`) via authenticated `GET /api/v1/metrics`; dev-side figures from
the local stack on :8180. Re-scrape quarterly and diff against this.

## Reference production fleet

5 runners online, all `ephemeral=false`, reporting `v0.4.1`:
`kern-908`, `ci-runner-<n>`, `ci-runner-<n>`, `ci-runner-<n>`,
`ci-runner-<n>`.

### Queue wait — `kern_job_queue_wait_seconds` (n=481, trailing 24h)

| ≤ bucket | jobs | cumulative |
| --- | --- | --- |
| 1s | 245 | 51% |
| 15s | 265 | 55% |
| 60s | 278 | 58% |
| 300s | 335 | 70% |
| 900s | 451 | 94% |
| 3600s | 481 | 100% |

Average 236s, p95 ≈ 930s (`kern_job_queue_seconds_p95` gauge: 929.5).
The distribution is bimodal: half of all jobs claim in under a second;
the tail waits 5–60 minutes during bursts. Queue pain is real but
burst-shaped, not steady-state.

### Claim latency — `kern_claim_query_seconds` (n=187)

Every observation ≤ 10ms, nearly all ≤ 5ms; sum 0.113s → ~0.6ms average.
The claim path is not the bottleneck.

### Step duration — `kern_step_duration_seconds` (n=2197)

94% of steps finish ≤ 60s; tail extends past 1h for long jobs.

### Scheduler — `kern_scheduler_tick_duration_seconds`

n=5 since api restart: all ≤ 25ms, ~17ms average.
`kern_scheduler_leader` = 1 (single replica holds the advisory lock).

### Runner utilization

All `active_jobs` = 0 at scrape; CPU ≈ 9% per runner; reported memory
≈ 28.7GB (host-wide figure). Disk free:

| Runner | Free | Free % |
| --- | --- | --- |
| ci-runner-<n> | 19.7GB / 79G | 24% |
| ci-runner-<n> | **2.8GB / 79G** | **3.4%** |
| ci-runner-<n> | 46.1GB / 118G | 36% |
| ci-runner-<n> | 11.6GB / 79G | 14% |
| kern-908 | 16.6GB | — |

### Cache — `kern_cache_lookups_total`

All zeros at scrape time: the counter is process-local and resets on api
restart, and no `actions/cache` traffic had run yet. It populates with
normal dogfood traffic (`apps/web` CI uses `cache: pnpm`).

### Runs

`kern_runs_total` 460, success 285, failure 157; `runs_active` 0,
`jobs_queued` 0, `jobs_running` 0 at scrape.

## What the numbers say

- **Queue waits are host-capacity, not scheduler cost.** CT902's runner
  log shows the load gate pausing claims at `loadavg1 ≈ 12–13` against
  `gate=12` throughout the day — jobs wait because runner CPUs saturate
  during bursts, while claim queries answer in ~0.6ms and scheduler
  ticks in ~17ms.
- **CT902 is in a disk incident.** 3.4% free is far under the 10%
  `KERN_RUNNER_DISK_MIN_FREE_PCT` default — but its runner image
  (`ghcr.io/dvorinka/kern-runner`, built 2026-11-07 15:05) predates the
  disk-GC, ephemeral, and egress code (`strings` finds none of it), so
  no GC can fire. Fleet runner images need a rebuild/redeploy before
  the pressure handlers exist in production. Manual `docker image
  prune` on CT902 is the interim remediation.
- **Cache hit rate** could not be measured on prod yet (counter resets
  per api process and no cache traffic had occurred). Dev evidence
  below.

## Dev cache probe — hit rate

`cache-probe` workflow on the dev stack (`actions/cache/restore|save@v4`,
stable key `probe-static-key`):

- Run `c62587fe` — `kern_cache_lookups_total{result="miss"}` +1; save
  stored the key.
- Run `97711559` — `{result="exact"}` +1; step log reads
  `Cache restored from key: probe-static-key`.
- Two earlier misses (`44b1eac9`, `ca101fe8`) used path `/tmp/cacheprobe`
  — outside the workspace, so save logged
  `KERN: no files found for cache path(s)` and nothing was stored.

Steady-state hit rate for an unchanged key is therefore ~100%; the
workspace-relative path rule is the only caveat (already documented in
`docs/compatibility.md`).

## Disk-pressure GC — dev demonstration

`dev-verify` restarted with `KERN_RUNNER_DISK_MIN_FREE_PCT=99` against a
host at 14.8% free. Startup sweep logged:

```text
WARN disk pressure: pruning docker {free_bytes: 74122010624,
     total_bytes: 499680116736, min_free_pct: 99}
INFO disk prune args=["builder","prune","-af"]   → 35.58MB
INFO disk prune args=["container","prune","-f"] → 4KB
INFO disk prune args=["image","prune","-f"]     → 1.227GB reclaimed
INFO disk prune args=["network","prune","-f"]
```

Threshold → prune → reclaim-only confirmed; tagged images untouched
(`image prune -f` without `-a`). Production cannot exercise this until
the fleet runner image carries the GC code — see CT902 above.
