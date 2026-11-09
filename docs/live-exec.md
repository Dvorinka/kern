# Live-job exec (`kern ssh`)

Shell into a running job's container while the job is still executing — the
`depot ci ssh` equivalent for debugging stuck builds (#132, #133).

## Design

```
user:    kern ssh <job-id|run-id> [--job KEY] [-c CMD]
            │  1. POST /jobs/:id/exec {command, interactive}   (admin role)
            ▼
control:   exec_requests row, state=pending
            │  2. delivered on the runner's next heartbeat (~10s)
            ▼
runner:    POST /runners/exec/:id/start → docker exec -i <container> sh -c CMD
           (interactive: `sh -c 'exec bash -l || exec sh -l'`)
            │  3. output chunks → POST /runners/exec/:id/output → blob
            │     exec/<job>/<exec>.out   (masked like step logs)
            │  4. stdin poll   → GET  /runners/exec/:id/stdin?after=N
            │     exec/<job>/<exec>.stdin (user appends via API)
            ▼
user:    5. GET /exec/:id?after=N every 400ms → stream output, exit code
```

All state lives in Postgres + the blob store, so the channel works across
API replicas — no session pinning needed.

## Gates

- **Role**: create + stdin require `admin`; reading state/output is member
  (same as step logs).
- **Trust**: fork/pull_request runs are refused at creation — an operator
  shell must never be invited into a sandbox running untrusted code.
- **Executor**: docker only. Host executors have no container boundary;
  the create call returns `host_executor`.
- **Lifecycle**: a job finishing sweeps its live exec rows to `failed`;
  a pending request nobody picks up expires after two minutes.

## Limits

- Line-mode shell — no PTY framing yet, so `vim`, `top`, etc. will not
  render. Interactive means stdin is forwarded; readline works, curses
  does not.
- stdout and stderr merge into one output blob.
- Pickup latency is one heartbeat interval (~10s worst case).

## Usage

```
kern ssh <job-id> -c 'cat /etc/os-release'
kern ssh <run-id> --job build            # interactive shell
echo 'make test' | kern ssh <job-id> -c sh
```
