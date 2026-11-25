# Remote execution offload (`kern exec`)

Problem: the laptop is too weak for large test suites or image builds. The fleet
is not. `kern exec` ships a working tree to a KERN runner, executes the job
there, and streams the result back so it looks local.

## Design

```
laptop:  kern exec --project p --workflow <id> [--dir .] [--pull out/]
            │  1. tar.gz the working tree (git-aware: tracked + untracked, no .git)
            │  2. PUT /projects/:p/source         (session auth → blob sources/<p>/<uuid>.tar.gz)
            │  3. POST /projects/:p/workflows/:id/runs {bundle_ref}
            ▼
control:   run.source.bundle_ref → claim payload
            ▼
runner:    GET /runners/jobs/:id/source → untar into workspace (replaces git clone)
           execute steps → logs/artifacts back to control plane as usual
            ▲
laptop:    4. stream step logs to stdout (SSE /steps/:id/logs/stream)
           5. --pull: download run artifacts into ./out after completion
           6. exit code = run conclusion (0 success, 1 otherwise)
```

## Result-return paths

- **Files** — `actions/upload-artifact` in the remote workflow → `kern exec --pull`
  or `kern artifacts download` → local directory. Compose can mount it.
- **Docker images** — remote step `docker build` + `docker save img | gzip` →
  upload-artifact → laptop `docker load` → `docker compose up` uses the local tag.
- **Caches** — `actions/cache` keeps dependency caches on the fleet side;
  repeat `kern exec` runs get faster without touching the laptop.

## Trust model

Exec runs are `trusted` (operator-initiated, secrets allowed) but the bundle is
treated like a checkout: it replaces `git clone`, nothing else changes about
isolation. Bundle upload is session-authed; runner fetch is job-scoped token.

## Status

Implemented and verified end to end: `PUT /api/v1/projects/:p/source` (session
auth, tar.gz → `sources/<slug>/<uuid>.tar.gz`), `bundle_ref` on the run trigger,
`GET /api/v1/runners/jobs/:jobID/source` for the leasing runner, and `kern exec
<workflow> [--dir D] [--pull DIR]` which bundles via `git ls-files` (tracked +
untracked, honoured .gitignore, no `.git`), uploads, streams step logs live to
stdout, and downloads run artifacts after completion. Exit code follows the run
conclusion.

Uploads are content-addressed: the CLI hashes the bundle, resolves it through
`GET /projects/:p/source/resolve`, and skips the upload entirely on a hit;
`PUT ?sha256=` stores under the digest key so the server dedupes too.

Since 0.21: Ctrl-C during `kern exec` cancels the remote run (fire-and-forget
is `kern runs trigger`), and the project is inferred from the directory's git
remote when `--project` and config are unset — inside a clone, `kern exec`
needs no flags at all.

## Agents

Agents offload through the same machinery; pick by capability:

- **Terminal agents** (kern CLI installed): `kern exec` exactly as above. The
  `kern-offload` skill teaches when and how, with an Android/Kotlin workflow
  template and fleet lane labels.
- **MCP agents**: `kern_trigger_run` accepts `workflow_yaml` + `workflow_path`
  + `bundle_ref` — the inline trigger core is shared with the HTTP by-path
  route. Binary transfer rides the REST endpoints with the same bearer token
  (`PUT /projects/:p/source` up, `GET /artifacts/:id/download` back): any
  agent that can produce a tar.gz necessarily has a shell, so bytes never
  travel inside MCP JSON. The `offload_heavy_build` prompt walks the exact
  command sequence.

Not yet: compose wrapper (`kern compose up` that loads pulled images then
invokes docker compose).
