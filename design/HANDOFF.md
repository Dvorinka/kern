# HANDOFF — KERN "Plot Soft" port continuation

**Goal:** the real app (`apps/web`) must be pixel-1:1 with `design/kern-soft.html`,
then enhancements on top. User reviews by screenshot — deliver clean captures.

## State

- Repo: `/home/tdvorak/Desktop/PROG+HTML/KERN`, branch `feat/console-interactivity`.
- **Everything is uncommitted.** All work lives in the working tree. User hasn't
  asked for a commit yet — ask before committing.
- `pnpm build` and `pnpm lint` are green (in `apps/web`, `pnpm@12.15.0`).
- Live verification stack: `just dev` (repo root, API on `:8080`) +
  `pnpm dev` (`apps/web`, vite on `:5173` proxying `/api` → `:8080`).
  Postgres already runs on `:5432` with seeded data (runs `3d ago` etc).
- Login: `admin@kern.local` / `kern-admin-password`.

## Design source of truth

`design/kern-soft.html` — self-contained mockup. Open it via
`python3 -m http.server -d design 8000` → `localhost:8000/kern-soft.html`, or
`file://` URL. `t` toggles paper/ink inside the mockup too.
Reference selectors the user explicitly compared against:

| Mockup | App file | Status |
|---|---|---|
| `.band` ink stats row | `.kpis` in `index.css` + `overview.tsx` | done |
| `.rl/.rw` graph lanes + rows | `overview.tsx` `graphSVG()/laneOf()` | done — `.rw` rows navigate, hover now paints yellow marker (`.rw:hover .t`, just added) |
| `.rw.sel` yellow marker | n/a (mockup is master/detail; app navigates) | hover marker added; if user wants persistent selection, highlight the run matching the URL when on `/runs/:id` |
| `.lbar/.lchip/.lsrch/.log` dark log | `log-view.tsx` + `.slog` CSS | done + search/level chips/copy/save/jump-to-latest |
| `.chart` runs-per-day | `runschart.tsx` | **NOT 1:1 — user flagged.** Mockup draws an area curve via `chart()`/`smooth()`/`areaw()` fns inside kern-soft.html (grep `function chart` / `smooth(`); app draws a different smooth curve. Port mockup's exact path math + fill + axis labels. |
| `.dots` dot-matrix world map | `fleetmap.tsx` | **NOT 1:1 — user flagged "map make it truly 1:1".** Mockup embeds a compressed land mask (`LAND` const + `unz()`/`dots`/`mapx`/`draw()` fns — grep `dots.js`/`const LAND` in kern-soft.html) rendered as a dot grid with halo pings, bezier arcs to hub, markers. App renders a real land-path SVG with graticule. To go 1:1: port the mask + `draw()` into a `DotMap` component (canvas or SVG circles), feed it real `fleetMap` data (`FleetMapPin.geo_lat/lng`, `control`, `jobs` arcs) and keep pan/zoom/tooltip machinery — or restyle pins/arcs/legend to the mockup look. |
| `.rhead` tag pills, `.clock`, `dl.kv`, `.acts/.pill` | `run-detail.tsx` hero | done — `.rtag/.clock/dl.rkv/.pill`, "← All runs" back pill |
| `.steps` rail + connector | `.slist` + `.step` | done — `::before` connector line, ring nodes |
| `.fr` runner slot tracks | `runners.tsx` `.frow`/`.slots` | done — headline "N of M runners are online." |
| `.login/.lp/.lbox` split hero | `login.tsx` | done |
| Topbar `.top/.brand/.nav/.find/.inv/.av` | `layout.tsx` `.hdr/.brand/.navp/.find/.inv/.avb` | done — `.navp` scrolls all 11 pages |
| `.pal` ⌘K palette | `CommandK` in `layout.tsx` | done |

## Still not verified / to fix next

- Ink theme + 390px mobile: coded but **no clean screenshot yet** —
  playwright captures kept truncating this session. Verify visually.
- `.mnav` CSS block in `index.css` is dead (no markup emits it) — remove or reuse.
- `Runs per day` and `FleetMap` = the two flagged 1:1 gaps (table above).
- Optional parity details: mockup `.steps` page section order on mobile puts
  `.stack` first (`.ov{grid-template-areas}` swap in CSS); axis flag/tick styling
  of `.trace .tscale`; hover tooltips on map pins (`.dottip`).
- `runners.tsx` still imports `PageHeader`-era bits? Check unused imports — lint was green last run.
- Playwright auth trick (React state): set inputs via native setter
  `Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(el,v)`
  + `input` event, **then click in a separate eval** — same-eval click submits stale state.
- Class-collision gotcha already hit once: `.ln` clashed with live-now cards → log
  line numbers are `.lnum`. Keep new mockup class names namespaced when app classes collide.

## How to verify per page (user asked for t3code browser integration)

Backend `just dev` + web `pnpm dev` → `localhost:5173` (log in once, session cookie persists).
Use the T3 `preview_*` browser tools per page (the user's preferred browser surface),
or `playwright-cli -s=k2 open/goto/eval/screenshot --filename=/tmp/kshots/…`.
Compare each page against the mockup file side by side — the user wants **pixel-1:1
restructure**, not "close". Deliver screenshots in the final report:
overview, run detail (log open), runners, login; paper + ink; 1440 + 390.

## User taste (don't regress)

- Modern, rounded, monochrome — paper #ececec / ink #0d0d0d, Archivo + JetBrains Mono.
- Colour only means state: blue live, red fault, yellow = selection/attention.
- Job identity = fill patterns (lint solid, build stripes, web dots), never hues.
- He rejected boxy Plot; "more modern on it, still monochrome"; wants real logs,
  searchable/filtrable/interactive; "map everything, do not keep anything, full port".
- Voice: JARVIS — dry, brief, no emojis/exclamation.

## If asked to commit

No commits made so far. When asked: `git add -A && git commit` with a conventional
message on `feat/console-interactivity`. No PR unless asked.
