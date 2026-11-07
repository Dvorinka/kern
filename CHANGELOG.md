# Changelog

All notable changes to KERN. Format follows [Keep a Changelog](https://keepachangelog.com/);
versions follow [SemVer](https://semver.org/) — minor bumps for features, patch for fixes.

## [v0.4.0] - 2026-11-07

### Added
- Interactive dashboard: clickable overview KPIs and panels, pan/zoom fleet map with
  live job arcs and runner hover details, runs-per-day chart with per-project breakdown,
  project filters and color coding in the runs list.
- GitHub App management: org-scoped app registration with unique manifest names,
  status endpoint, disconnect, and a Settings card; environment app shown alongside
  registered apps.
- Cross-page drill-downs: project links inside run rows and artifact rows, filtered
  run lists from projects/environments/usage charts, clickable gantt lanes that open
  a job's steps, scope-aware secret management with rotation, expandable webhook
  payloads with copy, and linkified audit-trail targets.

### Fixed
- Runner `active_jobs` reporting: column mismatch made every runner report zero live jobs.
- Orphaned queued/waiting jobs under finished runs are now cancelled by the scheduler;
  remediation approvals on terminal runs are refused and stale proposals withdrawn.
- Queue-duration metrics no longer report negative values.
- Self-heal no longer proposes package installs for daemon/socket/service commands
  that cannot be fixed that way; `self_heal=auto` retries allow-listed fixes once.
- Runner: writable action directory for trusted jobs; job-spawned containers reaped.
- Updater: no longer idles on a stale binary when the checkout already matches the
  release SHA.
