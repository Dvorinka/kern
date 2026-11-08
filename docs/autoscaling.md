# Runner autoscaling: options and decision

How the fleet grows when the queue backs up. Measured context: claim p95 is
~8 ms under a 184-job backlog (`internal/store` benchmarks), queue wait is
exported as `kern_job_queue_wait_seconds` on `/metrics`, and adding a runner
is already a one-command operation (`kern-runner join` + systemd unit, or any
docker host running the agent).

## Options

### (a) Kubernetes: scale runner pods on a queue metric

`actions-runner-controller` scale sets speak GitHub's runner protocol — they
cannot drive KERN's claim API, so ARC itself does not apply. The K8s-native
equivalent is a Deployment of `kern-runner` pods scaled by an external
signal:

- KEDA `prometheus` trigger on `kern_job_queue_wait_seconds` p95 (or queue
  depth, if a `kern_jobs_queued` gauge is added later).
- Or a plain HPA on a custom metric.
- Pods need docker: DinD sidecar or a remote docker host. The work-root path
  rules in `deploy.md` apply inside the pod unchanged.

Cost: a cluster plus KEDA. Benefit: queue-proportional scale-out with no KERN
code. This is the documented upgrade path once a fleet outgrows hand-managed
hosts — the metrics this branch exports are the trigger.

### (b) Proxmox pct-clone scaler

A controller clones a template CT per N queued jobs. Requires: a desired-count
signal (poll `/metrics` or a small API), clone → cloud-init → enroll-token →
join → drain → destroy orchestration, and stuck-clone handling. The most
expensive option and the least portable; only justified if (c) shows real pain
and (a) is unavailable (bare-metal Proxmox shop, no Kubernetes).

### (c) Status quo + enroll-token runbook — recommended now

- `kern-runner join` already mints a per-runner credential; systemd unit
  generation exists. Adding capacity is one command on any docker host.
- Labels (`self-hosted`, `kern`, OS/arch, custom) make jobs land correctly
  without fleet changes; least-loaded placement spreads work automatically.
- Scale-up stays a human or script decision; scale-down is `drain` + stop.

## macOS VM on-demand power

The macOS runner (VM 904 on the Proxmox host) is powered by a host-side
scaler, `deployments/macos-vm-scaler.sh`, installed as a systemd timer on
the Proxmox host. It starts the VM when queued jobs need the `macos` label
(`KERN_MAC_TRIGGER_LABEL`, default `macos`) and shuts it down after the
runner is offline/idle past `KERN_MAC_IDLE_MINUTES` (default 15). Boot
latency is roughly two minutes — queue wait on a cold VM is expected.
`KERN_MAC_WARM_CRON` can pre-warm the VM on a schedule. See
`docs/production.md` for the launchd runner layout on the guest.

## Decision

(c) — status quo plus the enroll-token runbook — for now. Rationale: the
fleet is small and hand-managed; ARC cannot drive KERN's claim protocol, so
the K8s path means KEDA/custom scaling anyway; and (b) is the most expensive
option for a queue-wait problem that has not been measured as painful. The
gate for revisiting is `kern_job_queue_wait_seconds` p95 on `/metrics` —
once it shows real pain, (a) is the documented path where Kubernetes exists,
and (b) only where it does not and owner sign-off is explicit.

## Measured revisit — 2026-11-08

The p95 gate has now been measured on production (CT908, `84fc9e4`,
`docs/kpi-baseline.md`):

- `kern_job_queue_wait_seconds` p95 ≈ **930s**, average ≈ 236s over 481
  jobs/24h — but bimodal: 51% of jobs claim in <1s. Pain is burst-shaped.
- `kern_claim_query_seconds` ≈ 0.6ms average — claiming is not the cost.
- `kern_scheduler_tick_duration_seconds` ≈ 17ms — the scheduler is not
  the cost.
- Runner logs show the actual constraint: CT902's load gate pauses claims
  at `loadavg1 ≈ 12–13` vs gate 12 — **host CPU saturation during bursts**,
  not queue machinery.

Reading: real queue pain exists, but its cause is finite runner CPU on a
hand-managed fleet — adding runners relieves it directly, and no scaler
code is needed to justify that. Kubernetes does not exist in this
deployment (Proxmox CTs), so (a) remains inapplicable infrastructure-wise;
(b) stays disproportionate for a problem solved by "run one more CT". The
macOS VM scaler above is the same pattern applied to one specialty host.

What changed since the original decision: the JIT/ephemeral path is now
verified live (`docs/security-isolation.md`). `POST /api/v1/runners` mints
a self-deregistering runner credential — a future scaler can grow the
fleet by minting ephemeral runners on spare docker hosts and letting them
expire, with no k8s and no persistent fleet bookkeeping. That makes (c)
strictly cheaper to extend than when this doc was written, and it is the
natural substrate if queue p95 stays >15min *after* the fleet image is
rebuilt (see the stale-runner caveat in `docs/kpi-baseline.md` — the CI
fleet currently runs a runner image predating GC/ephemeral/egress, so
scale-out today would replicate missing features).

Decision stands: **(c)**, with the JIT mint as the documented scale-out
primitive. Revisit (b) only if p95 stays above ~15min sustained *and*
host-level autoscaling is wanted on bare metal; revisit (a) only if a
Kubernetes cluster appears.

Recorded decision: (c) adopted 2026-11; (a) documented as the K8s upgrade
path; (b) deferred pending queue-wait p95 evidence and owner sign-off.
Re-measured 2026-11-08 against production — (c) confirmed, JIT mint noted
as the scale-out primitive.
