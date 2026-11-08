# Connecting a repository

KERN does not work like a GitHub-hosted runner pool. Nothing needs to change
inside the workflow file beyond the `runs-on` labels — and even that is only a
label match, not an image selection. Three pieces have to line up:

## 1. The GitHub App is installed on the repo

The App (`kern-ci` on the production instance, or a per-org App created via
**Projects → Connect GitHub**) is the event source. It watches `push` and
`pull_request` on every repo it can see and delivers them to
`POST /api/v1/webhooks/github`. If the App is not installed on the repo,
nothing reaches the control plane.

## 2. A KERN project is bound to the repo

```sh
kern projects create --slug myrepo --repo-full-name Dvorinka/myrepo
```

`repo_full_name` (`owner/name`) is the binding key. When a push arrives for
`Dvorinka/myrepo`, the control plane finds the project, reads
`.github/workflows/` at the pushed SHA through the contents API, compiles each
workflow, and queues a run.

For repos on the same host you can bind `local_path` instead and sync
manually, but webhook-driven runs need `repo_full_name`.

## 3. `runs-on` must match a runner's labels — usually zero YAML changes

Every registered KERN runner implicitly carries `self-hosted` and `kern`, plus
whatever `--labels` it was enrolled with. A job is claimable when its
`runs-on` set is a subset of some online runner's labels.

On this fleet every runner is also enrolled with `ubuntu-latest` and
`ubuntu-24.04`, so an unmodified GitHub workflow claiming `ubuntu-latest`
matches verbatim — the file does not need to change at all. If your runners
lack that label, point `runs-on` at `self-hosted` (or `kern`) instead, or add
the label at enroll time: `kern-runner join --labels ubuntu-latest,linux,x64`.

Two things people trip on:

- **`runs-on` is label-matching, not image selection.** The OS/toolchain
  comes from the job's `container:` (or `KERN_DEFAULT_IMAGE`, and the
  compiler's label→image map for common GitHub labels). `ubuntu-latest` as a
  *label* says "any self-hosted box answers this," nothing more.
- **KERN does not emulate GitHub's runner images.** Pick the toolchain image
  you want (`container: golang:1.26`); don't rely on a preinstalled
  `ubuntu-latest` toolcache — there isn't one.

## What does NOT change

- `on:` triggers, `needs`, matrices, `if`, expressions, `env`, secrets
  scoping, `actions/checkout`, `actions/cache`, artifacts — the compiler
  handles the YAML as written. The honest feature matrix lives in
  `docs/compatibility.md`; unsupported constructs produce diagnostics, never
  silent passes.
- Secrets stay server-side. Fork PRs run with `trust_level=fork`: no secrets,
  their own cache scope, and (when `fork_gate` is set) an approval hold before
  the first job claims.

## tl;dr

Install the App, create the project, done. On this fleet `runs-on` needs no
edit at all — `ubuntu-latest` already resolves to a live runner.

## MCP (agents)

`POST /api/v1/mcp` speaks the Model Context Protocol (streamable HTTP, stateless). Any MCP-capable agent connects with an API token — create one under Settings → API tokens or `kern login`.

Client config:

```json
{
  "mcpServers": {
    "kern": {
      "url": "https://kern.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Tools cover the console's read surface plus the run lifecycle:

| Domain | Tools |
| --- | --- |
| Projects | `kern_list_projects`, `kern_get_project` |
| Runs | `kern_list_runs`, `kern_get_run`, `kern_trigger_run`, `kern_rerun_run`, `kern_approve_run`, `kern_cancel_run` |
| Logs | `kern_step_log` (tail by default, `offset` to page) |
| Runners | `kern_list_runners`, `kern_set_runner_drain` |
| Secrets | `kern_list_secrets` — names and scopes only, never values |

Write actions are audited under the token owner's identity. Secrets and raw logs stay masked server-side — the endpoint exposes nothing the console doesn't. Artifacts, cache, environments, deployments, settings, members and audit export remain HTTP-only for now.

Scopes apply: list/get/log/secret tools need `read`, `kern_trigger_run`/`kern_rerun_run`/`kern_cancel_run` need `write`, `kern_approve_run`/`kern_set_runner_drain` need `admin` plus the admin org role — the same gates as the HTTP routes they mirror.
