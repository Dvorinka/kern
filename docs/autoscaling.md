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

## Decision

(c) — status quo plus the enroll-token runbook — for now. Rationale: the
fleet is small and hand-managed; ARC cannot drive KERN's claim protocol, so
the K8s path means KEDA/custom scaling anyway; and (b) is the most expensive
option for a queue-wait problem that has not been measured as painful. The
gate for revisiting is `kern_job_queue_wait_seconds` p95 on `/metrics` —
once it shows real pain, (a) is the documented path where Kubernetes exists,
and (b) only where it does not and owner sign-off is explicit.

Recorded decision: (c) adopted 2026-11; (a) documented as the K8s upgrade
path; (b) deferred pending queue-wait p95 evidence and owner sign-off.
