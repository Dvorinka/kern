# Porting repos onto KERN — readiness ledger

Measured 2026-11-06 by `kern lint` (offline compile of every
`.github/workflows` file) plus a run-level audit of the execution model
(uid 1000 job containers, no docker socket, no `sudo`, label-subset
runner matching). Repos scanned: Invico, oru-mail, desloppify, kern,
Cloudreve, Primora, Dash, Cal, TimeSpent, tock, runway, Relay,
Nothing_Modes — 60 workflow files total.

**Compile verdict: 60/60 valid, zero errors.** Every workflow parses
into a plan KERN would schedule. The remaining work is run-level and
repo-level, listed below by leverage.

## Cross-cutting blockers

Ordered by how many repos they touch.

| Gap | Repos affected | What it takes |
| --- | --- | --- |
| Non-Linux `runs-on` labels | oru-mail (windows-2022, macos-26-intel, ubuntu-24.04-arm, ubuntu-22.04), Cloudreve (windows/macos lanes), Invico (windows, hp-win11, android), Cal, Primora, runway (macos/windows matrix lanes) | Windows + macOS host-executor runners exist: `hp-win11-kern` (Windows 11, labels `windows`/`windows-latest`/`hp-win11`, scheduled task, live-verified) and `macos-904` (VM904, scaler-powered). Host mode = no containers/services/docker socket; node actions work via the bundled node dist. Versioned labels (`windows-2022`, `macos-*`) need `KERN_RUNNER_LABELS`. `ubuntu-22.04`/`ubuntu-24.04-arm` need a fleet label + a matching job image / ARM runner. |
| Docker-in-job | Invico, oru-mail, Primora, Dash, Cal, tock, runway, Relay, Cloudreve (every docker build/push/release lane) | Partially closed: `trusted`/`protected` runs mount `/var/run/docker.sock` on docker-executor runners (the job image still needs the `docker` CLI), and `needs_docker` jobs prefer `kern-docker` host-executor runners where the daemon is native. `pull_request`/`fork` runs get no daemon by design — keep those lanes on GHA or set project `pr_trust=trusted` for same-repo PRs. DinD/`dind` service remains unimplemented. |
| `sudo` / root steps | oru-mail, Invico, Primora, Cal, runway, tock (playwright `install-deps`) | Job containers run uid 1000, read-only rootfs, no sudo. The self-heal allowlist already installs missing binaries as root (`self_heal=auto` on prod) — for everything else, bake deps into a job image (`KERN_LABEL_IMAGES`-style label→image map) instead of installing per run. |
| `GITHUB_TOKEN` not injected | every `softprops/action-gh-release`, `gh` CLI call, `tauri-action`, `goreleaser` upload | Resolved on trusted tiers: `trusted`/`protected` runs get a per-run GitHub App installation token in `GITHUB_TOKEN` (honouring the workflow `permissions:` ceiling) and the runner persists it as a git `http.extraheader`, so `git push` authenticates. `pull_request`/`fork` runs get no token by design — keep those lanes on GHA or set `pr_trust=trusted`. |
| `release` / `repository_dispatch` events | Nothing_Modes fdroid (`release`), oru-mail release-next, any repo dispatching builds | Both now mint runs — `release` runs the workflow at the tagged commit (`types:` actions, absent = `published`; `branches` filters match `target_commitish`), `repository_dispatch` requires `types:` event-type strings and runs on the default head. Both need the GitHub App subscribed to the event — tick it in the app's GitHub settings for pre-existing apps. |
| `vars` context empty | anything reading `${{ vars.* }}` | `vars` is stubbed to `{}` in the run context. Repo variables need a table + secret-free config surface, or document "use plain env". |
| `runs-on: ${{ inputs.runner }}` | Invico + Relay ployz-build | Resolved: trigger accepts `inputs` (`POST /projects/:p/workflows/:w/runs` body `inputs:{...}`, `kern runs trigger --inputs '{"runner":"hp-win11"}'`); runs-on re-resolves at seed time against matrix + dispatch inputs. Missing input keeps the literal label — job stays queued (same as any unmatched label). Declared `on.workflow_dispatch.inputs` defaults are not evaluated — pass every input the workflow reads. |
| Secrets/vars migration | all | Every repo's GitHub secrets must be re-created per project (`PUT /api/v1/secrets/:name?project=<id>` or the dashboard). The `secrets` expression context is live — `${{ secrets.X }}` resolves in env/if/run/with — so migration is purely a data-copy step. Cal's `ANDROID_KEYSTORE_*` trio is already populated. `vars` still needs a store; use plain `env` until then. |

## Per-repo verdicts

✅ = ports as-is. ⚠️ = needs workflow tweaks or fleet labels. ❌ = has
lanes KERN cannot run today.

| Repo | Verdict | Notes |
| --- | --- | --- |
| kern | ✅ dogfooded | runs on the fleet today |
| desloppify | ✅ | all `ubuntu-latest`; setup-python/dotnet via JS actions; PyPI publish needs secrets |
| tock | ⚠️ | 11 workflows; postgres/mailpit/gotenberg `ports` now work (localhost mode); playwright `install-deps` needs a job image with browser deps baked, `install` alone works as uid 1000 |
| Nothing_Modes | ⚠️ | setup-java/setup-android work (user-space SDK); fdroid lane fires on `release` event — needs the release-event arm |
| Invico | ⚠️ | ci/staging/backup/logs port cleanly (pg service on localhost works now); mobile/desktop release lanes pin `hp-win11`/`android`/`windows` — keep on GHA or native runners |
| Dash | ⚠️ | ci ports as-is; release chains on `workflow_run` + goreleaser + docker push |
| runway | ⚠️ | rust lanes fine; release has macos/windows matrix + docker push |
| Relay | ⚠️ | backend/web/openapi fine (pg on localhost works now); docker lane needs dind; trivy needs docker; apk needs gradle (works) |
| Primora | ⚠️ | ci ports (pg service fixed); release is heavy: `workflow_run`, win/arm matrix, buildx, sbom, npm publish |
| Cal | ⚠️ | ci fine; release has macos matrix + docker buildx + gh-release (needs `GITHUB_TOKEN` secret) |
| Cloudreve | ⚠️ | ci mostly fine (paths-filter, rust, gradle); desktop/release lanes are windows/macos matrix + tauri-action + docker |
| oru-mail | ⚠️/❌ | test.yaml probably fine (sudo steps need removing); the repo is release-heavy: windows-2022, macos, arm, snap (snapd needs privileged — docker model can't), AWS key-based creds work as secrets |
| TimeSpent | — | no CI to port |

## Porting order (when approved)

1. **desloppify, tock(ci lanes), kern-style repos** — lowest risk, prove
   the multi-project UX immediately.
2. **Nothing_Modes, Relay, Dash, runway** — after the `release`/
   `workflow_run` event arms + `GITHUB_TOKEN` secret convention.
3. **Invico, Primora, Cal, Cloudreve** — split lanes: Linux lanes on
   KERN, windows/macos/android lanes stay on GHA until native runners or
   a dind decision lands.
4. **oru-mail** — last; snap + codesign + windows lanes are the hardest
   fit for the container model.

## Account-wide GitHub setup

Two paths exist; they compose.

- **GitHub-side:** self-hosted runners can be registered at *org* scope
  (org Settings → Actions → Runners → runner groups) — any repo in the
  org picks them up via `runs-on: ubuntu-latest` etc. Personal accounts
  have no account-level runners; repo-level only. Moving repos under an
  org (e.g. ELXTECH) unlocks org-scoped runners and runner groups.
- **KERN-side:** the `kern-ci` GitHub App is already the account-wide
  primitive — install it on "All repositories" and every repo's
  push/PR/check events arrive at one control plane. The missing piece is
  auto-connecting a repo to a kern project on `installation`/`push`
  events so a new repo works with zero clicks; today each repo needs
  `kern projects create` + a first sync.
