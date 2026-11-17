# Handoff 08 — GitHub Actions → KERN migration

Goal: move the user's GitHub-Actions workload onto KERN. Evidence base: full
workflow scan of all active Dvorinka repos (2026-11-08) + `internal/api/
handlers_webhook.go` trigger-filter code. Fleet already proven: linux
containers, macOS host executor (VM 904 live), services, cache, artifacts,
annotations, self-update, signed releases through v0.5.1.

## Phase 1 — tag-push filters (unblocks every release.yml)

`pushTriggerMatch` (`internal/api/handlers_webhook.go` ~line 460) only honours
`branches`/`branches-ignore`; `tags`, `tags-ignore`, `paths`, `paths-ignore`
return "unsupported push filter" → run skipped. Every repo's `release.yml`
uses `on: push: tags: ['v*']`.

- Extend push matching: `ref` arrives as `refs/tags/v1.2.3` or
  `refs/heads/main` — branch patterns must not match tag refs and vice versa.
- Honour `tags`/`tags-ignore` glob patterns (`v*`, `v*.*.*`) against the tag
  name; keep `branches*` semantics unchanged for branch pushes.
- Decide `paths`/`paths-ignore` here or defer to Phase 3 — if deferred keep the
  unsupported-skip (safe) rather than firing wrongly.
- Tests: table-driven over ref shapes × filter combos in the api package.
- Update `docs/compatibility.md` trigger row to match reality.

## Phase 2 — `workflow_run` events (Primora, Dash)

Parsed but no provider events. Minimum viable: when a run completes, find
workflows whose `on.workflow_run.workflows` lists the completed workflow's
name/path and `types` includes the conclusion mapping (`completed`,
`success`, `failure`). Reuse the webhook event path with a synthetic payload
shape the compiler already tolerates. Scope: same-repo only first.

## Phase 3 — `paths`/`paths-ignore` filters (runway ci.yml and friends)

Needs the push diff — webhook payload has `head_commit` but not file lists.
Options: fetch compare/diff via GitHub API at trigger time (runner already
has repo credentials path — check `internal/github`), or evaluate lazily at
first job. Cheapest correct: API diff at trigger, cache per SHA. If API
unavailable → keep skipping with diagnostic, never fire blindly.

## Phase 4 — docker-in-job for image builds (relay, Primora, Invico)

`docker build`/`build-push-action`/`docker login` inside job containers have
no daemon. Options in preference order: (a) document+detect → route image-build
jobs to `executor=host` trusted runners (docker CLI exists there); (b)
rootless DinD sidecar per job (heavy); (c) Kaniko-style daemonless build
action path. (a) is smallest correct diff — host executor already exists,
trust gate already protects it. Add a runner label convention
(`kern-docker`) so workflows can target it explicitly.

## Phase 5 — label bridging + pilot migration

- `lxc-901` (Invico pins every job): add the label to the intended KERN
  runner's label set, or sed workflows to KERN labels — prefer labels, zero
  repo churn.
- `${{ matrix.runner }}`/`inputs.runner` dynamic runs-on: verify expression
  resolution covers them; if not, small expressions fix.
- Pilot order: `cal`, `freeflow`, `Soika`, `desloppify` ci.yml only (push/PR,
  `x64`/`ubuntu-latest` labels already match). Connect via GitHub App
  manifest flow (verified working). Keep GitHub Actions enabled for
  release/Windows/workflow_run jobs until their phases land.
- Windows (Invico hp-win11 Android job): out of scope — stays on GitHub or a
  future Windows host-executor.

## Rules

Same as always: JARVIS voice, ponytail smallest diff, PR-first never main,
trailer `Authored By: TDvorak <info@tdvorak.dev>`, pre-push gate
(gofmt/build/test/vet + pnpm lint/build), `KERN_TEST_DATABASE_URL=.../kern_test`,
`GOTMPDIR=$HOME/.cache/go-tmp`, never `pkill -f`, memory updates in
`~/agent-memory/projects/kern.md`.

DONE = Phases 1–4 merged with tests + compat doc updated; Phase 5 pilot
repos connected with at least one green KERN run each; evidence (run URLs,
webhook logs) in PRs. Windows + private-actions remain documented gaps.

## Phase 6 — issue burn-down (#62–#70, close what can be closed)

After Phases 1–5, work the open roadmap issues in dependency order. Each
issue closes only with evidence (code + tests + docs); descope explicitly
with a comment when blocked on a user decision.

- #62 Observability: per-job metrics, resource graphs, log search —
  metrics registry + Grafana dashboards exist; finish job-level series and
  log search (check what #82 already shipped before building).
- #63 Debugging: live-job exec — ssh/exec into running job containers is
  the depot-ci-ssh parity ask; runner-side endpoint + API route + UI entry.
- #64 Speed: pre-warmed runners + docker layer cache + co-located caches —
  warm pools conflict with ephemeral/JIT model; evaluate image prepull
  (exists) + registry layer cache first, document decision.
- #65 GitHub surface: commit-status edge cases, deployment protection,
  more triggers — overlaps Phases 1–3; remaining: statuses API parity,
  environments/deployment branches.
- #66 Enterprise: audit log exists (#82); RBAC roles, SAML (OIDC impl
  pending IdP decision — document blocker), secret controls inventory.
- #67 Runner platforms: macOS DONE (host executor live on VM 904);
  Windows remains out of scope — comment and keep open or close as
  wontfix-with-rationale per maintainer call.
- #68 CLI parity: kern doctor/logs -f/ssh/diagnose/migrate — check what
  cmd/ already has; smallest useful subset first.
- #69 MCP server maturity: endpoint exists (PR #35); tools coverage,
  auth, docs.
- #70 Testbox/pre-push checks: Semaphore-style ephemeral check runs —
  overlaps runner fleet; scope honestly.

Rules for burn-down: one issue per commit/PR where reasonable; comment on
each issue with what shipped or why deferred; never close without evidence.
