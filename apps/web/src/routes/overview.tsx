import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { useMemo } from "react";
import { api } from "../lib/api";
import type { FleetMapResponse, OverviewResponse, Run, Runner } from "../lib/types";
import { PageHeader, Spinner } from "../components/ui";
import { StatusSq } from "../components/status";
import { Fingerprint, jobHue, useRunDetails } from "../components/fingerprint";
import { FleetMap } from "../components/fleetmap";
import { RunsChart, runsPerDay } from "../components/runschart";
import { bytes, duration, projectColor, relTime, seconds } from "../lib/format";

export function OverviewPage() {
	const nav = useNavigate();
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
	const { data: runners } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
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

	const runs = useMemo(() => runsData?.runs ?? [], [runsData]);
	const liveRun = runs.find((r) => r.status === "in_progress" || r.status === "queued");
	// the live run earns its own slot in the detail fan-out — it is not
	// necessarily inside the recent-six window
	const detailRuns = useMemo(
		() => (liveRun && !runs.slice(0, 6).includes(liveRun) ? [liveRun, ...runs.slice(0, 5)] : runs.slice(0, 6)),
		[runs, liveRun],
	);
	const recent = detailRuns.slice(0, 6);
	const details = useRunDetails(detailRuns);

	const liveDetail = useMemo(
		() => (liveRun ? details[detailRuns.indexOf(liveRun)]?.data : undefined),
		[liveRun, details, detailRuns],
	);

	// runner_id → hue of the job it's currently running, for the fleet tint
	const runnerJob = useMemo(() => {
		const m = new Map<string, string>();
		for (const d of details) {
			for (const j of d.data?.jobs ?? []) {
				if ((j.status === "in_progress" || j.status === "waiting") && j.runner_id) {
					m.set(j.runner_id, jobHue(j.job_key || j.name));
				}
			}
		}
		return m;
	}, [details]);

	const day = useMemo(() => runsPerDay(runs), [runs]);

	if (isLoading || !data) return <Spinner />;
	const o = data.overview;
	const todayStart = new Date();
	todayStart.setHours(0, 0, 0, 0);
	const runsToday = runs.filter((r) => Date.parse(r.created_at) >= todayStart.getTime()).length;
	const failsToday = runs.filter(
		(r) => r.conclusion === "failure" && Date.parse(r.created_at) >= todayStart.getTime(),
	).length;
	const successRate = o.runs_total ? Math.round((o.runs_success / o.runs_total) * 100) : null;

	const kpis: { k: string; v: string; vs?: string; c: string; cls: string; to: string }[] = [
		{ k: "Runs today", v: String(runsToday), c: `${o.runs_total} total`, cls: "fl", to: "/runs" },
		{
			k: "Success",
			v: successRate != null ? `${successRate}%` : "—",
			c: `${o.runs_failure} failed of ${o.runs_total}`,
			cls: "fl",
			to: "/usage",
		},
		{
			k: "Queue p95",
			v: o.p95_queue_seconds ? seconds(o.p95_queue_seconds) : "—",
			c: o.jobs_queued ? `${o.jobs_queued} waiting` : "no queue waiting",
			cls: "fl",
			to: "/runs?tab=running",
		},
		{
			k: "Runners",
			v: `${o.runners_online}`,
			vs: `/${o.runners_total}`,
			c: o.jobs_running ? `${o.jobs_running} jobs running` : "fleet idle",
			cls: "fl",
			to: "/runners",
		},
		{ k: "Cache", v: String(data.cache.entries), c: bytes(data.cache.bytes), cls: "fl", to: "/cache" },
	];

	return (
		<>
			<PageHeader
				title={(meta?.name ?? "kern").toLowerCase()}
				sub={`self-hosted · ${o.runners_online} runner${o.runners_online === 1 ? "" : "s"} · every push runs on contact`}
				actions={
					<>
						<span className={failsToday === 0 && runsToday > 0 ? "tag ok" : "tag"}>
							<span
								className={`sq ${failsToday === 0 && runsToday > 0 ? "ok" : "dim"}`}
								style={{ width: 7, height: 7 }}
							/>
							{failsToday === 0 && runsToday > 0 ? "healthy" : "watching"}
						</span>
						<span className="text-[11px] text-[var(--faint)]">
							updated {relTime(new Date(dataUpdatedAt).toISOString())}
						</span>
					</>
				}
			/>

			{liveRun && (
				<div
					className="panel rowlink mt-4 flex cursor-pointer items-center gap-3.5 px-4 py-3"
					onClick={() => nav(`/runs/${liveRun.id}`)}
				>
					<span className="sq live" />
					<span className="max-w-[46%] flex-none truncate text-[13px] font-medium">
						{liveRun.workflow_name} #{liveRun.run_number}
						{liveRun.commit_message ? ` · ${liveRun.commit_message.split("\n")[0]}` : ""}
					</span>
					<div className="min-w-[140px] flex-1">
						<Fingerprint run={liveRun} jobs={liveDetail?.jobs} />
					</div>
					<span className="mono flex-none text-[11px] text-[var(--faint)]">
						{duration(liveRun.started_at ?? liveRun.created_at, null)}
					</span>
					{o.avg_run_seconds > 0 && (
						<span className="flex-none text-[11px] text-[var(--faint)]">
							est ~{seconds(o.avg_run_seconds)}
						</span>
					)}
					<button className="lnk flex-none" onClick={() => nav(`/runs/${liveRun.id}`)}>
						View
					</button>
				</div>
			)}

			{(data.webhooks?.failed_24h ?? 0) > 0 && (
				<div
					className="panel rowlink mt-4 flex cursor-pointer items-center gap-3.5 border-[var(--err)]/40 px-4 py-3"
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

			<div className="panel kpis mt-4">
				{kpis.map((k) => (
					<Link className="kpi rowlink" key={k.k} to={k.to}>
						<div className="k">{k.k}</div>
						<div className="v">
							{k.v}
							{"vs" in k && k.vs ? <small>{k.vs}</small> : null}
						</div>
						<div className={`c ${k.cls}`}>{k.c}</div>
					</Link>
				))}
			</div>

			<div className="mt-4 grid grid-cols-[3fr_2fr] items-start gap-4 max-lg:grid-cols-1">
				<div className="panel">
					<div className="phead">
						<span className="pt">Runs per day</span>
						<span className="r">
							<span className="jc">
								<i style={{ background: "var(--ink)" }} />
								runs
							</span>
							<span className="jc">
								<i style={{ background: "var(--err)" }} />
								failed
							</span>
						</span>
					</div>
					<RunsChart days={day} version={meta?.version} />
				</div>
				<div className="panel">
					<div className="phead">
						<span className="pt">Fleet</span>
						<span className="r">utilisation now</span>
					</div>
					{(runners?.runners ?? []).map((r) => {
						const hue = runnerJob.get(r.id);
						const busy = (r.active_jobs ?? 0) > 0;
						const pct = Math.round(r.cpu_percent ?? 0);
						return (
							<div className="fr rowlink" key={r.id} onClick={() => nav("/runners")}>
								<StatusSq status={busy ? "in_progress" : r.status} />
								<span className="rn">{r.name}</span>
								<div className="ubar">
									<i
										className={hue ? `u-${hue}` : "u-grey"}
										style={{ width: `${Math.max(pct, busy ? 4 : 0)}%` }}
									/>
								</div>
								{hue && <span className={`chip ${hue}`}>{hue}</span>}
								<span className="rs">{pct}%</span>
							</div>
						);
					})}
					{!runners?.runners.length && (
						<div className="emptynote">
							No runners registered — a push waits in queue until one joins.
						</div>
					)}
					{o.jobs_queued ? (
						<Link className="emptynote rowlink block" to="/runs?tab=running">
							{o.jobs_queued} job{o.jobs_queued === 1 ? "" : "s"} queued for a runner —
							click to watch.
						</Link>
					) : (
						<div className="emptynote">
							Queue is clear. A push picks a runner on contact.
						</div>
					)}
				</div>
				{fleetMap && (
					<div className="panel col-span-full">
						<div className="phead">
							<span className="pt">Fleet map</span>
							<span className="r">
								{fleetMap.control
									? `control at ${fleetMap.control.city || "?"}${fleetMap.control.country ? `, ${fleetMap.control.country}` : ""}`
									: "control plane unlocated"}
								{" · "}arcs are live jobs
							</span>
						</div>
						<FleetMap data={fleetMap} />
					</div>
				)}
			</div>

			<div className="mt-4 grid grid-cols-[3fr_2fr] items-start gap-4 max-lg:grid-cols-1">
				<div className="panel">
					<div className="phead">
						<span className="pt">Recent runs</span>
						<span className="r">
							<span className="jc">
								<i className="j-lint" />
								lint
							</span>
							<span className="jc">
								<i className="j-build" />
								build
							</span>
							<span className="jc">
								<i className="j-web" />
								web
							</span>
							<button className="lnk" onClick={() => nav("/runs")}>
								View all
							</button>
						</span>
					</div>
					{recent.length === 0 && (
						<div className="emptynote">
							No runs yet — push to a wired repo or trigger a workflow.
						</div>
					)}
					{recent.map((r, i) => {
						const live = r.status === "in_progress" || r.status === "queued";
						return (
							<Link
								key={r.id}
								to={`/runs/${r.id}`}
								className="rowlink grid grid-cols-[14px_44px_minmax(0,1fr)_84px_76px_52px] items-center gap-3 border-b border-[var(--line)] px-4 py-[9px] last:border-0"
							>
								<StatusSq status={r.status} conclusion={r.conclusion} />
								<span className="font-mono text-xs text-[var(--faint)]">#{r.run_number}</span>
								<Fingerprint run={r} jobs={details[i]?.data?.jobs} small />
								<span
									className="truncate text-right font-mono text-[10px]"
									style={{ color: projectColor(r.project_slug) }}
									title={r.project_name}
								>
									{r.project_slug}
								</span>
								<span className="text-right font-mono text-xs text-[var(--dim)]">
									{duration(r.started_at, r.completed_at)}
								</span>
								<span className="text-right text-[11px] text-[var(--faint)]">
									{live ? "now" : relTime(r.created_at)}
								</span>
							</Link>
						);
					})}
				</div>
				<div className="panel rowlink cursor-pointer" onClick={() => nav("/cache")}>
					<div className="phead">
						<span className="pt">Cache</span>
						<span className="r">7d eviction · click to manage</span>
					</div>
					<div className="bwrap">
						<div className="flex justify-between text-xs">
							<span className="pt">Stored</span>
							<span className="mono text-[var(--dim)]">{bytes(data.cache.bytes)}</span>
						</div>
					</div>
					<div className="kv">
						<span className="k">Entries</span>
						<span className="v">{data.cache.entries}</span>
					</div>
					<div className="kv">
						<span className="k">Avg run</span>
						<span className="v">{o.avg_run_seconds ? seconds(o.avg_run_seconds) : "—"}</span>
					</div>
					<div className="kv">
						<span className="k">Last run</span>
						<span className="v">{o.last_run_at ? relTime(o.last_run_at) : "—"}</span>
					</div>
				</div>
			</div>
		</>
	);
}


