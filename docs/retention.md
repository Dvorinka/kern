# Retention policies

KERN keeps history forever by default only where storage is cheap. Bounded
artifacts are governed by the env vars below; `0` disables the sweep for
that class.

## Control plane (`deployments/docker-compose.yml`, `api` service)

| Var | Default | Governs |
|---|---|---|
| `KERN_LOG_RETENTION` | `720h` | Step-log and summary blobs in the blob store |
| `KERN_CACHE_MAX_AGE` | `168h` | `actions/cache` entries |
| `KERN_WEBHOOK_RETENTION` | `720h` | `webhook_deliveries` rows |
| `KERN_AUDIT_RETENTION` | `8760h` | `audit_events` rows (one year) |
| `KERN_NOTIFY_RETENTION` | `720h` | `notify_deliveries` rows |
| `KERN_SESSION_TTL` | — | Web session lifetime, not data retention |

## Runner

| Var | Default | Governs |
|---|---|---|
| `KERN_RUNNER_IMAGE_MAX_AGE` | `168h` | Pulled job images unused past this age are GC'd |
| `KERN_RUNNER_CACHE_MAX_BYTES` | — | `_cache` dir budget; oldest entries evicted first |
| `KERN_RUNNER_TOKEN_TTL` | — | Enroll-token lifetime at mint, not data |

## Not retained — by design

- **Run/job rows** are never deleted; the `cleared` flag hides them from
  list views (`kern runs clear-cancelled`). Postgres stays the source of
  truth for history.
- **Exec session blobs** (`exec/<job>/<exec>.{out,stdin}`) follow
  `KERN_LOG_RETENTION` — they are log-class data.
- **Artifacts** are quota-bound per project, not time-bound.
