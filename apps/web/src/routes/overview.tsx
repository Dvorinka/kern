import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { useMemo, useState } from "react";
import { api } from "../lib/api";
import type { DayCount, FleetMapResponse, OverviewResponse, QueuedJob, Run } from "../lib/types";
import { Spinner } from "../components/ui";
import { Fingerprint, jobTint, useRunDetails } from "../components/fingerprint";
import { FleetMap, fleetSites } from "../components/fleetmap";
import { RunsChart, type DayPoint } from "../components/runschart";
import { ActivityMap, type DayTotals } from "../components/activitymap";
import { duration, projectColor, refName, relTime, seconds, shortSha } from "../lib/format";

const live = (r: Run) => r.status === "in_progress" || r.status === "queued";

/* ── run graph: a commit-graph spine behind the recent list.
	 main/master rides lane 0; every other ref gets its own lane and merges
	 back into the spine below, like the git log you already read. ── */
const RH = 68;
const LX = [16, 40, 64];
const GW = 80;

function laneOf(runs: Run[]): number[] {
	const lanes = new Map<string, number>();
	let next = 1;
	return runs.map((r) => {
		const b = refName(r.ref || "") || "main";
		if (b === "main" || b === "master") return 0;
		if (!lanes.has(b)) lanes.set(b, Math.min(next++, LX.length - 1));
		return lanes.get(b)!;
	});
}

function graphSVG(runs: Run[], lanes: number[], selId: string | null) {
	const H = runs.length * RH;
	const rr = 14;
	const y = (i: number) => i * RH + RH / 2;
	let p = "";
	runs.forEach((_, i) => {
		const a = lanes[i]!;
		// continue the lane to the next run on the same ref
		const same = runs.findIndex((_, j) => j > i && lanes[j] === a);
		if (same > i) {
			p += `<path d="M${LX[a]!} ${y(i)}V${y(same)}" fill="none" stroke="var(--ink)" stroke-opacity="${a === 0 ? 1 : 0.5}" stroke-width="${a === 0 ? 3 : 2}" stroke-linecap="round"/>`;
		} else if (a > 0) {
			// no more runs on this ref — merge into the next main-lane run below
			const m = runs.findIndex((_, j) => j > i && lanes[j] === 0);
			const yb = m > i ? y(m) : H;
			p += `<path d="M${LX[a]!} ${y(i)}V${yb - rr}Q${LX[a]!} ${yb} ${LX[a]! - rr} ${yb}H${LX[0]}" fill="none" stroke="var(--ink)" stroke-opacity="0.5" stroke-width="2" stroke-linecap="round"/>`;
		} else if (i === runs.length - 1) {
			p += `<path d="M${LX[0]} ${y(i)}V${y(i) + 40}" fill="none" stroke="url(#gfade)" stroke-width="3" stroke-linecap="round"/>`;
		}
	});
	let n = "";
	runs.forEach((r, i) => {
		const cx = LX[lanes[i]!]!;
		const cy = y(i);
		if (r.id === selId) {
			n += `<circle cx="${cx}" cy="${cy}" r="15" fill="none" stroke="var(--ink)" stroke-width="2"/>`;
		}
		if (live(r)) {
			n += `<circle class="halosvg" cx="${cx}" cy="${cy}" r="8" fill="var(--live)"/><circle cx="${cx}" cy="${cy}" r="8" fill="var(--paper)" stroke="var(--live)" stroke-width="4"/>`;
		} else if (r.conclusion === "failure" || r.status === "cancelled") {
			n += `<circle cx="${cx}" cy="${cy}" r="9" fill="var(--err)"/><path d="M${cx - 3.4} ${cy - 3.4}l6.8 6.8M${cx + 3.4} ${cy - 3.4}l-6.8 6.8" stroke="#fff" stroke-width="2.2" stroke-linecap="round"/>`;
		} else if (r.conclusion === "success" || r.status === "completed") {
			n += `<circle cx="${cx}" cy="${cy}" r="9" fill="var(--ink)"/><path d="M${cx - 3.6} ${cy + 0.2}l2.6 2.8 4.6-5.6" stroke="var(--paper)" stroke-width="2.2" fill="none" stroke-linecap="round" stroke-linejoin="round"/>`;
		} else {
			n += `<circle cx="${cx}" cy="${cy}" r="8" fill="var(--paper)" stroke="var(--ink)" stroke-width="3"/>`;
		}
	});
	return `<svg width="${GW}" height="${H}" viewBox="0 0 ${GW} ${H}" aria-hidden="true"><defs><linearGradient id="gfade" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="var(--ink)"/><stop offset="1" stop-color="var(--ink)" stop-opacity="0"/></linearGradient></defs>${p}${n}</svg>`;
}

const mmss = (ms: number) =>
	`${Math.floor(ms / 60000)}:${String(Math.floor((ms % 60000) / 1000)).padStart(2, "0")}`;

const SEL_KEY = "kern-sel-run";

export function OverviewPage() {
	const nav = useNavigate();
	const [selRun, setSelRun] = useState<string | null>(() => sessionStorage.getItem(SEL_KEY));
	const { data, isLoading, dataUpdatedAt } = useQuery({
		queryKey: ["overview"],
		queryFn: () => api.get<OverviewResponse>("/overview"),
		refetchInterval: 5000,
	});
	const { data: runsData } = useQuery({
		queryKey: ["runs", "all"],
		queryFn: () => api.get<{ runs: Run[]; total: number }>("/runs?limit=100"),
		refetchInterval: 8000,
	});
	const { data: fleetMap } = useQuery({
		queryKey: ["fleetmap"],
		queryFn: () => api.get<FleetMapResponse>("/instance/map"),
		refetchInterval: 4000,
	});
	const { data: meta } = useQuery({
		queryKey: ["meta"],
		queryFn: () => api.get<{ name: string; version: string; env: string }>("/meta"),
		staleTime: Number.POSITIVE_INFINITY,
	});
	// Per-day aggregates come from the table — the chart and activity map
	// show real history instead of the tail of the paged runs list.
	const { data: dayCounts } = useQuery({
		queryKey: ["day-counts", 70],
		queryFn: () => api.get<{ days: DayCount[] }>("/runs/day-counts?days=70"),
		refetchInterval: 30000,
	});
	// The live queue, server-side — replaces per-run detail fan-out.
	const { data: queueData } = useQuery({
		queryKey: ["queue"],
		queryFn: () => api.get<{ jobs: QueuedJob[] }>("/jobs?status=queued"),
		refetchInterval: 4000,
	});

	const runs = useMemo(() => runsData?.runs ?? [], [runsData]);
	const liveRuns = useMemo(() => runs.filter(live), [runs]);
	const recent = useMemo(() => runs.slice(0, 7), [runs]);
	// job timings power the mini traces and the queue — fan out detail reads
	const detailRuns = useMemo(
		() => [...new Map([...liveRuns, ...recent].map((r) => [r.id, r])).values()],
		[liveRuns, recent],
	);
	const details = useRunDetails(detailRuns);
	const detailOf = (id: string) => details[detailRuns.findIndex((r) => r.id === id)]?.data;

	// Server aggregates → chart points + activity cells. Buckets are UTC
	// days; the ?day= drill-down parses the same instant server-side.
	const { day, totals } = useMemo(() => {
		const byDay = new Map<string, DayPoint>();
		const tmap = new Map<string, DayTotals>();
		for (const c of dayCounts?.days ?? []) {
			const p =
				byDay.get(c.day) ??
				(() => {
					const d: DayPoint = {
						label: new Date(`${c.day}T00:00:00Z`).toLocaleDateString(undefined, {
							month: "short",
							day: "numeric",
						}),
						dayISO: `${c.day}T00:00:00Z`,
						total: 0,
						failed: 0,
						cancelled: 0,
						projects: [],
						today: false,
					};
					byDay.set(c.day, d);
					return d;
				})();
			p.total += c.total;
			p.failed += c.failed;
			p.cancelled += c.cancelled;
			p.projects.push({ slug: c.project, count: c.total });
			const t = tmap.get(c.day) ?? { date: c.day, total: 0, failed: 0, cancelled: 0, live: 0 };
			t.total += c.total;
			t.failed += c.failed;
			t.cancelled += c.cancelled;
			t.live += c.live;
			tmap.set(c.day, t);
		}
		const todayKey = new Date().toISOString().slice(0, 10);
		const days = [...byDay.values()]
			.sort((a, b) => a.dayISO.localeCompare(b.dayISO))
			.slice(-14);
		days.forEach((d) => {
			d.today = d.dayISO.slice(0, 10) === todayKey;
			d.projects.sort((a, b) => b.count - a.count);
		});
		return { day: days, totals: [...tmap.values()] };
	}, [dayCounts]);
	const lanes = useMemo(() => laneOf(recent), [recent]);
	const sites = useMemo(() => fleetSites(fleetMap?.runners ?? []), [fleetMap]);

	if (isLoading || !data) return <Spinner />;
	const o = data.overview;
	// Same UTC-day bucket the chart marks "today" — counting the fetched
	// run page would cap this KPI at the list's limit.
	const todayKey = new Date().toISOString().slice(0, 10);
	const runsToday = totals.find((t) => t.date === todayKey)?.total ?? 0;
	const successRate = o.runs_total ? Math.round((o.runs_success / o.runs_total) * 100) : null;

	const kpis: { k: string; v: string; vs?: string; c: string; to: string }[] = [
		{ k: "runs today", v: String(runsToday), c: `${o.runs_total} total`, to: "/runs" },
		{
			k: "passed",
			v: successRate != null ? `${successRate}%` : "—",
			c: `${o.runs_failure} failed of ${o.runs_total}`,
			to: "/usage",
		},
		{
			k: "median run",
			v: o.avg_run_seconds ? seconds(o.avg_run_seconds) : "—",
			c: `queue p95 ${o.p95_queue_seconds ? seconds(o.p95_queue_seconds) : "—"}`,
			to: "/runs?tab=running",
		},
		{
			k: "queue wait",
			v: o.jobs_queued ? String(o.jobs_queued) : "0",
			c: o.jobs_queued ? "jobs waiting" : "clear",
			to: "/runs?tab=running",
		},
		{
			k: "runners",
			v: `${o.runners_online}`,
			vs: `/${o.runners_total}`,
			c: o.jobs_running ? `${o.jobs_running} jobs running` : "fleet idle",
			to: "/runners",
		},
	];

	// the real queue, server-side claim order
	const queued = queueData?.jobs ?? [];

	const pick = (id: string) => {
		sessionStorage.setItem(SEL_KEY, id);
		setSelRun(id);
		nav(`/runs/${id}`);
	};

	return (
		<>
			{(data.webhooks?.failed_24h ?? 0) > 0 && (
				<div
					className="panel rowlink mb-4 flex cursor-pointer items-center gap-3.5 px-6 py-3.5"
					onClick={() => nav("/deliveries")}
				>
					<span className="sq bad" />
					<span className="text-[13px] text-[var(--dim)]">
						<span className="font-medium text-[var(--err)]">
							{data.webhooks!.failed_24h} webhook{data.webhooks!.failed_24h === 1 ? "" : "s"}
						</span>{" "}
						failed in the last 24h — pushes may not have become runs.
						{data.webhooks!.last_error && (
							<span className="block truncate font-mono text-[11px] text-[var(--faint)]">
								{data.webhooks!.last_error}
							</span>
						)}
					</span>
					<button className="lnk ml-auto flex-none" onClick={() => nav("/deliveries")}>
						Deliveries
					</button>
				</div>
			)}

			<div className="panel kpis">
				{kpis.map((k) => (
					<Link className="kpi rowlink" key={k.k} to={k.to}>
						<div className="k">{k.k}</div>
						<div className="v">
							{k.v}
							{k.vs ? <small>{k.vs}</small> : null}
						</div>
						<div className="c fl">{k.c}</div>
					</Link>
				))}
			</div>

			<div className="ov">
				{/* left — recent runs on the commit graph */}
				<div className="panel ovl">
					<div className="phead">
						<span className="pt">Recent runs</span>
						<span className="r">
							live, newest first
							<span className="hidden sm:inline">
								· updated {relTime(new Date(dataUpdatedAt).toISOString())}
							</span>
							{meta?.name && <span className="hidden sm:inline">· {meta.name.toLowerCase()}</span>}
						</span>
					</div>
					<div className="key">
						<span>
							<i className="sw h1" />
							lint
						</span>
						<span>
							<i className="sw h2" />
							build
						</span>
						<span>
							<i className="sw h3" />
							web
						</span>
						<Link to="/runs" className="keygo">
							All runs
						</Link>
					</div>
					{recent.length === 0 && (
						<div className="emptynote">
							No runs yet — push to a wired repo or trigger a workflow.
						</div>
					)}
					<div className="rl">
						<div
							className="rlg"
							dangerouslySetInnerHTML={{ __html: graphSVG(recent, lanes, selRun) }}
						/>
						{recent.map((r) => (
							<div
								key={r.id}
								className={`rw${r.id === selRun ? " sel" : ""}`}
								role="link"
								tabIndex={0}
								onClick={() => pick(r.id)}
								onKeyDown={(e) => {
									if (e.key === "Enter") pick(r.id);
								}}
							>
								<div style={{ minWidth: 0 }}>
									<div className="t">
										<span>
											{r.commit_message?.split("\n")[0] ||
												`${r.workflow_name} #${r.run_number}`}
										</span>
									</div>
									<div className="m">
										<b>
											<span style={{ color: jobTint(r.workflow_name) }}>
												{r.workflow_name}
											</span>{" "}
											#{r.run_number}
										</b>
										<span>{refName(r.ref)}</span>
										<code className="font-mono">{shortSha(r.sha)}</code>
										<Link
											to={`/projects/${r.project_slug}`}
											onClick={(e) => e.stopPropagation()}
											className="lnk"
											style={{ color: projectColor(r.project_slug) }}
										>
											{r.project_slug}
										</Link>
									</div>
								</div>
								<div
									className={`du ${live(r) ? "live" : r.conclusion === "failure" ? "fail" : ""}`}
								>
									{duration(r.started_at, r.completed_at)}
									<small>{live(r) ? "now" : relTime(r.created_at)}</small>
								</div>
								<Fingerprint run={r} jobs={detailOf(r.id)?.jobs} small tinted className="fp" />
							</div>
						))}
					</div>
				</div>

				{/* right — live runs, the fleet, and the shape of the work */}
				<div className="stack">
					{liveRuns.length > 0 && (
						<div className="panel">
							<div className="phead">
								<span className="pt">Live now</span>
								<span className="r">
									{liveRuns.length} run{liveRuns.length === 1 ? "" : "s"} across the fleet
								</span>
							</div>
							{liveRuns.slice(0, 10).map((r) => (
								<div
									key={r.id}
									className="lrow"
									role="link"
									tabIndex={0}
									onClick={() => pick(r.id)}
									onKeyDown={(e) => {
										if (e.key === "Enter") pick(r.id);
									}}
								>
									<i className="sq live" />
									<div className="lt2">
										<div className="t">
											{r.commit_message?.split("\n")[0] ||
												`${r.workflow_name} #${r.run_number}`}
										</div>
										<div className="m">
											{r.workflow_name} #{r.run_number} · {refName(r.ref)} · {shortSha(r.sha)} ·{" "}
											{r.project_slug}
											{(detailOf(r.id)?.jobs ?? []).filter(
												(j) => j.status === "in_progress" || j.status === "waiting",
											).length > 0 &&
												` · ${(detailOf(r.id)?.jobs ?? [])
													.filter((j) => j.status === "in_progress" || j.status === "waiting")
													.map((j) => j.name)
													.join(", ")} running`}
										</div>
									</div>
									<div className="du live">
										{duration(r.started_at ?? r.created_at, null)}
										<small>now</small>
									</div>
									<Fingerprint
										run={r}
										jobs={detailOf(r.id)?.jobs}
										small
										tinted
										className="fp"
									/>
								</div>
							))}
							{liveRuns.length > 10 && (
								<Link className="qrow rowlink dim" to="/runs?tab=running">
									+{liveRuns.length - 10} more live — watch them all
								</Link>
							)}
						</div>
					)}

					{fleetMap && (
						<div className="panel">
							<div className="phead">
								<span className="pt">Fleet</span>
								<span className="r">
									{`${sites.length} site${sites.length === 1 ? "" : "s"} · arcs are live jobs`}
									<Link to="/runners" className="keygo">
										Manage
									</Link>
								</span>
							</div>
							<div className="map">
								<FleetMap data={fleetMap} />
							</div>
							<div className="mapfoot">
								{sites.map((s) => (
									<span className="chip2" key={s.key}>
										<i
											className={`sq ${s.st === "live" ? "live" : s.st === "idle" ? "idle" : "dim"}`}
										/>
										{s.code} · {s.runners.length} runner{s.runners.length === 1 ? "" : "s"}
										{s.st === "live" ? ` · ${s.jobs.length} live` : s.st === "off" ? " · down" : ""}
									</span>
								))}
								{fleetMap.control && (
									<span className="chip2">
										<i className="sq ok" />
										{siteLabel(fleetMap.control.city)} · control plane
									</span>
								)}
							</div>
							{o.jobs_queued ? (
								<Link className="emptynote rowlink block" to="/runs?tab=running">
									{o.jobs_queued} job{o.jobs_queued === 1 ? "" : "s"} queued for a runner —
									click to watch.
								</Link>
							) : null}
						</div>
					)}

					<div className="duo">
						<div className="panel">
							<div className="phead">
								<span className="pt">Runs per day</span>
								<span className="r">14d bars · 10w grid</span>
							</div>
							<RunsChart days={day} version={meta?.version} />
							<ActivityMap counts={totals} weeks={10} />
						</div>
						<div className="panel">
							<div className="phead">
								<span className="pt">Queue</span>
								<span className="r">
									{o.jobs_queued} job{o.jobs_queued === 1 ? "" : "s"} waiting
								</span>
							</div>
							<div className="qwrap">
								{queued.length === 0 && (
									<div className="qrow dim">nothing waiting — the fleet is clear</div>
								)}
								{queued.slice(0, 5).map((j) => (
									<Link
										to={`/runs/${j.run_id}?job=${encodeURIComponent(j.job_key || j.id)}`}
										className="qrow rowlink"
										key={j.id}
									>
										<i
											className="sw"
											style={{
												background: jobTint(j.job_key || j.name),
												borderColor: jobTint(j.job_key || j.name),
											}}
										/>
										<div>
											<b>{j.name}</b>
											<small>
												{j.status === "waiting_approval"
													? "needs an approval"
													: j.needs.length
														? `waits for ${j.needs.join(", ")}`
														: `waiting for a runner · ${j.project_slug}`}
											</small>
										</div>
										<span className="wt">
											~{mmss(Date.now() - Date.parse(j.queued_at))}
										</span>
									</Link>
								))}
								{queued.length > 5 && (
									<div className="qrow dim">+{queued.length - 5} more waiting</div>
								)}
							</div>
						</div>
					</div>
				</div>
			</div>
		</>
	);
}

const siteLabel = (city: string) => city.replace(/\s.*/, "").slice(0, 3).toLowerCase() || "hub";
