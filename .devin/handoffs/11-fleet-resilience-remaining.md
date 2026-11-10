# HANDOFF 11 — fleet resilience, remaining slices + console polish

Written 2026-11-10. Repo `~/Desktop/PROG+HTML/KERN` (master, remote
Dvorinka/kern). Read `~/agent-memory/projects/kern.md` first — tail
sections "fleet resilience session" + "slice 2" have the full incident
context, fleet ops already done, and the gotchas. `pull --ff-only` memory
before editing; commit+push after.

## Where things stand

Merged/deployed:
- PR #258 (master 4b8eeb3): Gradle mirror init script +
  `GRADLE_USER_HOME=/kern/cache/gradle`; `$GITHUB_ENV`→`env` ctx parity;
  `oom`/`network` diagnosis classes; transient auto-retry
  (`max_transient_retries`); docker prunes run under load.
- **v0.7.9 tagged** at c87c693 — release run was queued at write time;
  fleet self-updates as runners go idle (verify `/api/v1/meta` + runner
  versions before continuing).

Open PRs at write time:
- PR #259 feat/fleet-resilience-2 (head 67554a2, base master): CARGO_HOME,
  NPM_CONFIG_PREFIX, trusted KERN_JOB_MEMORY/CPUS/DISK hints, work-root
  disk sampler → `disk_quota`, silence watchdog → `hang`. Checks were
  IN_PROGRESS — merge when green, it rides the next tag.
- PR #260 feat/mobile-polish: `.tbl` scroll + sticky col ≤820px,
  hover:none affordances, page gutters. Checks in flight — merge when green.

Fleet ops done (no follow-up needed): CT901+906 resized to 98G, LOAD_GATE=2
on all four CTs, CT903 label bug fixed (`lxc-901,lxc-903`), all runners
v0.7.8→v0.7.9 via self-update, Android SDK `.complete` everywhere.
Loose: `de-oracle-a1` v0.6.1 with ~1.2G free disk (off-site, needs a pass).

## Remaining work, in priority order

1. **Verify v0.7.9 + #259 fleet rollout** — `gh run list --workflow
   release.yml`; `/api/v1/meta` → version; runners endpoint → all
   v0.7.9. Then watch one Gradle job's log for `kern-central-mirror`
   pickup (Nothing_Modes is the canary; its CI was already green).
2. **Failure classification as a first-class job field** (P1.8):
   `steps.diagnosis->>'kind'` already exists per step. Roll the dominant
   kind up to `jobs.classification` (migration), populate at FinishJob
   (reuse `JobFailureKinds` in internal/store/jobs.go — pick the first
   failed step's kind), surface in `/runs` + `kern runs list --json` +
   metrics counter. Extends #209: prepare failures bypass diagnose —
   write `jobErrorHint` classes into the field too.
3. **Env/secret audit trail** (P1.9): at job claim, log the env var
   NAMES materialized (jobEnv keys + secret names, never values) into the
   job log header — one `KERN: env:` line would have made the 92c54930
   GITHUB_ENV bug a 30s diagnosis. Cheapest version: runner-side
   `logStep` at first step; richer version: `jobs.env_names` column +
   run-detail card.
4. **LAN egress proxy** (P2.10): nginx pull-through cache on a dedicated
   CT (903 or a new small CT) covering maven2 layout first; point
   `KERN_MAVEN_MIRROR` at it (the init script already honors the env —
   no code change). Extend later to npm/go/pypi per the design note.
5. **Warm prime** (P2.11): cheap first step (`gradlew dependencies`, `go
   mod download`, `npm ci`) to fill the shared cache — now mostly moot
   for Gradle since `_cache/gradle` persists; still useful for cold
   runners. Optional per-project `prime:` config.
6. **Console redesign polish**: PR #260 was the first pass. Remaining:
   real-viewport QA at 375px/768px across every page (runners, run
   detail DAG + gantt, settings forms, cache/artifacts), `kern logs`
   readability at 360px, tap-target sweep ≥32px, form field
   `font-size:16px` (iOS zoom), then the broader "whole ui fixed and
   polished" pass the user asked for — keep the monochrome shadcn
   grammar + radius scale (12/8/4/2) from memory's UI direction.
7. **Cloudreve release-create 403** (handoff 10 §1): instrument effective
   perms at claim, verify deployed `kern-api:local` mints like HEAD
   source, or PAT-with-repo workaround via project secret. Probe recipe
   is in handoff 10.
8. **Addendum leftovers** (handoff 10 addendum): `shell: pwsh` detection/
   warning on windows runners, `kern exec --workflow-file`/ref override,
   runner-panic → infra-fail class + degraded flag, fleet version-drift
   surfacing on /runners, macOS headless DMG note in docs.

## Working style notes

- Stack branches; on squash-merge of a base, `git rebase --onto master
  <base> <stacked>` + force-push + `gh pr edit --base master`.
- `git add -A` sweeps stray binaries (`go build` outputs land in repo
  root — check `git status` before committing).
- `git checkout <branch> -- <files>` DESTROYS uncommitted edits in those
  files — commit or stash first.
- `splitLabels` in cmd/runner is comma-only — `KERN_RUNNER_LABELS` wants
  `a,b` not `a b` or newlines.
- `pct exec` PATH lacks /usr/local/bin; use `bash -lc` or absolute paths.
- Never push to master directly from an agent session except
  docs/changelog commits that carry a release tag.
- Merge via `gh pr merge --squash` (repo convention).

## Copy-paste handoff prompt for the next agent

```
Work in ~/Desktop/PROG+HTML/KERN (master, Dvorinka/kern). Read
~/agent-memory/projects/kern.md tail + .devin/handoffs/11-fleet-resilience-remaining.md
first; pull --ff-only memory before editing, commit+push after.

1. Verify v0.7.9 rollout: release.yml green, /api/v1/meta version,
   all runners on v0.7.9; merge PR #259 and #260 once checks green;
   watch a Gradle job for the kern-central-mirror in the repo list.
2. First-class failure classification: roll steps.diagnosis->>'kind' up
   to a jobs.classification column (migration), populate at FinishJob
   (first failed step's kind, plus jobErrorHint classes for prepare
   failures), expose in /runs, `kern runs list --json`, and a metrics
   counter.
3. Env/secret audit trail: log materialized env var + secret NAMES
   (never values) into the job log header at claim time.
4. LAN egress proxy: nginx pull-through on a dedicated CT covering
   maven2 layout; set KERN_MAVEN_MIRROR on the fleet (no code change —
   the init script already honors the env). Extend to npm/go/pypi after.
5. Console polish continues: real-viewport QA (375/768px) on every page,
   tap targets, iOS font-size, then the broader redesign polish.
6. Cloudreve 403 probe per handoff 10 §1 (instrument claim perms or
   PAT workaround).

Conventions: ponytail/smallest-diff, tests for non-trivial logic, squash
merge, v* tag to release, fleet self-updates via signed manifest.
Gotchas in handoff file §"Working style notes".
```
