import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { useMemo, useState } from "react";
import { api } from "../lib/api";
import type { UsageResponse } from "../lib/types";
import { ActivityMap } from "../components/activitymap";
import { Card, CardHeader, EmptyState, PageHeader, Spinner, cn } from "../components/ui";
import { seconds } from "../lib/format";

const WINDOWS = [7, 30, 90];

function minutes(sec: number): string {
	if (sec < 60) return `${Math.round(sec)}s`;
	const m = sec / 60;
	if (m < 60) return `${m.toFixed(0)}m`;
	const h = m / 60;
	if (h < 100) return `${h.toFixed(1)}h`;
	return `${Math.round(h)}h`;
}

function usd(n: number): string {
	return n < 1 ? `$${n.toFixed(3)}` : `$${n.toFixed(2)}`;
}

export function UsagePage() {
	const [days, setDays] = useState(30);
	const { data, isLoading } = useQuery({
		queryKey: ["usage", days],
		queryFn: () => api.get<UsageResponse>(`/usage?days=${days}`),
	});
	const { data: dayCounts } = useQuery({
		queryKey: ["day-counts", 98],
		queryFn: () => api.get<{ days: import("../lib/types").DayCount[] }>("/runs/day-counts?days=98"),
	});
	const dayTotals = (dayCounts?.days ?? []).reduce((m, c) => {
		const t = m.get(c.day) ?? { date: c.day, total: 0, failed: 0, cancelled: 0, live: 0 };
		t.total += c.total;
		t.failed += c.failed;
		t.cancelled += c.cancelled;
		t.live += c.live;
		m.set(c.day, t);
		return m;
	}, new Map<string, import("../components/activitymap").DayTotals>());

	const u = data?.usage;
	const t = u?.totals;
	const successRate = t && t.runs > 0 ? `${Math.round((t.runs_success / t.runs) * 100)}%` : "—";

	const kpis = t
		? [
				{
					k: "Runner time",
					v: minutes(t.runner_seconds),
					c: `${t.jobs} jobs · ${t.runs} runs`,
				},
				{
					k: "Hosted equivalent",
					v: usd(data!.hosted_equiv_usd),
					c: `${t.billable_minutes} billed min · GitHub Linux 2-core @ ${usd(data!.rate_usd_per_min)}/min`,
				},
				{
					k: "Median queue wait",
					v: t.queue_median_seconds > 0 ? seconds(t.queue_median_seconds) : "—",
					c: "queued → started",
				},
				{
					k: "Median job",
					v: t.job_median_seconds > 0 ? seconds(t.job_median_seconds) : "—",
					c: "per job, wall clock",
				},
				{ k: "Success rate", v: successRate, c: `${t.runs_success} of ${t.runs} runs` },
				{
					k: "Billed waste dodged",
					v: (() => {
						const waste = t.billable_minutes * 60 - t.runner_seconds;
						return waste > 0 ? minutes(waste) : "—";
					})(),
					c:
						t.billable_minutes * 60 - t.runner_seconds > 0
							? `GitHub would meter ${Math.round(((t.billable_minutes * 60 - t.runner_seconds) / Math.max(t.runner_seconds, 1)) * 100)}% beyond real compute`
							: "billing matches compute exactly",
				},
				{
					k: "Queue-free share",
					v: `${Math.max(0, Math.round(100 - (t.queue_median_seconds / Math.max(t.job_median_seconds, 1)) * 100))}%`,
					c: `jobs spend ${seconds(t.queue_median_seconds) || "0s"} waiting — the rest is work`,
				},
			]
		: [];

	return (
		<>
			<PageHeader mark="web"
				title="Usage"
				sub="What the fleet consumed — and what GitHub-hosted runners would have billed for the same work. Billable minutes round each job up to the whole minute, exactly as GitHub meters."
				actions={
					<div className="tabs" style={{ border: 0 }}>
						{WINDOWS.map((w) => (
							<button key={w} className={w === days ? "on" : ""} onClick={() => setDays(w)}>
								{w}d
							</button>
						))}
					</div>
				}
			/>

			{isLoading || !u ? (
				<Spinner />
			) : (
				<>
					<div className="panel kpis">
						{kpis.map((k) => (
							<div className="kpi" key={k.k}>
								<div className="k">{k.k}</div>
								<div className="v">{k.v}</div>
								<div className="c fl">{k.c}</div>
							</div>
						))}
					</div>

					<MinutesChart u={u} days={days} />

					<div className="mt-4 grid grid-cols-[3fr_2fr] items-stretch gap-4 max-lg:grid-cols-1">
						<div className="panel">
							<div className="phead">
								<span className="pt">Activity</span>
								<span className="r">cell = dominant status · weight = run count</span>
							</div>
							<ActivityMap counts={[...dayTotals.values()]} weeks={14} />
						</div>
						<ShareDonut u={u} />
					</div>

					{u.failures.length > 0 && (
						<Card className="mt-4">
							<CardHeader
								title="Failure causes"
								sub="failed steps by diagnosis kind — which workflow line keeps breaking"
								actions={
									<Link to="/runs?tab=failed" className="lnk text-xs">
										failed runs
									</Link>
								}
							/>
							<table className="tbl">
								<thead>
									<tr>
										<th>Cause</th>
										<th className="num">Steps</th>
										<th className="num">Fixable</th>
									</tr>
								</thead>
								<tbody>
									{u.failures.map((f) => (
										<tr key={f.kind}>
											<td className="lead font-mono text-xs">{f.kind}</td>
											<td className="num">{f.count}</td>
											<td className="num">{f.fixable > 0 ? f.fixable : "—"}</td>
										</tr>
									))}
								</tbody>
							</table>
						</Card>
					)}

					<div className="mt-4 grid grid-cols-2 items-start gap-4 max-lg:grid-cols-1">
						<Card>
							<CardHeader title="By project" sub="runner time" />
							{u.projects.length === 0 ? (
								<EmptyState title="No completed jobs in this window." />
							) : (
								<table className="tbl">
									<thead>
										<tr>
											<th>Project</th>
											<th className="num">Runs</th>
											<th className="num">Time</th>
											<th className="num">Share</th>
										</tr>
									</thead>
									<tbody>
										{u.projects.map((p) => (
											<tr key={p.slug}>
												<td className="lead">
													<Link to={`/projects/${p.slug}`} className="lnk">
														{p.name || p.slug}
													</Link>
												</td>
												<td className="num">{p.runs}</td>
												<td className="num">{minutes(p.seconds)}</td>
												<td className="num">
													{t!.runner_seconds > 0 ? (
														<span className="shcell">
															<span className="meter sm">
																<i style={{ width: `${(p.seconds / t!.runner_seconds) * 100}%` }} />
															</span>
															{Math.round((p.seconds / t!.runner_seconds) * 100)}%
														</span>
													) : (
														"—"
													)}
												</td>
											</tr>
										))}
									</tbody>
								</table>
							)}
						</Card>
						<Card>
							<CardHeader title="By runner" sub="jobs claimed" />
							{u.runners.length === 0 ? (
								<EmptyState title="No runner activity in this window." />
							) : (
								<table className="tbl">
									<thead>
										<tr>
											<th>Runner</th>
											<th className="num">Jobs</th>
											<th className="num">Time</th>
										</tr>
									</thead>
									<tbody>
										{u.runners.map((r) => (
											<tr key={r.name}>
												<td className="lead">
													<Link to="/runners" className="lnk">
														{r.name}
													</Link>
												</td>
												<td className="num">{r.jobs}</td>
												<td className="num">{minutes(r.seconds)}</td>
											</tr>
										))}
									</tbody>
								</table>
							)}
						</Card>
					</div>
				</>
			)}
		</>
	);
}

function MinutesChart({ u, days }: { u: UsageResponse["usage"]; days: number }) {
	const nav = useNavigate();
	// Backfill days the fleet was idle so the axis reads as a real calendar.
	const buckets = useMemo(() => {
		const byDate = new Map(u.days.map((d) => [d.date.slice(0, 10), d]));
		const out: { label: string; date: string; mins: number; failed: number; today: boolean }[] = [];
		for (let i = days - 1; i >= 0; i--) {
			const d = new Date();
			d.setDate(d.getDate() - i);
			const key = d.toISOString().slice(0, 10);
			const row = byDate.get(key);
			out.push({
				label: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
				date: key,
				mins: row?.billable_minutes ?? 0,
				failed: row?.failed_jobs ?? 0,
				today: i === 0,
			});
		}
		return out;
	}, [u, days]);

	const max = Math.max(...buckets.map((b) => b.mins), 1);
	const yMax = Math.max(Math.ceil(max / 5) * 5, 5);
	const any = buckets.some((b) => b.mins > 0);
	const skip = Math.max(1, Math.floor(days / 6));

	return (
		<div className="panel mt-4">
			<div className="phead">
				<span className="pt">Billable minutes per day</span>
				<span className="r">
					per-job ceiling — the meter GitHub would read
					<span className="g3" style={{ marginLeft: 10 }}>0–{yMax}m</span>
				</span>
			</div>
			{!any && <div className="emptynote">No completed jobs in this window.</div>}
			{any && (
				<div className="runschart">
					<div className="bars" style={{ height: 96 }}>
						{buckets.map((b, i) => (
							<i
								key={i}
								className={b.today ? "tdy" : ""}
								role="link"
								tabIndex={0}
								style={{ height: `${Math.max((b.mins / yMax) * 100, b.mins > 0 ? 3 : 0)}%` }}
								title={`${b.label} · ${b.mins} billed min${b.failed ? `, ${b.failed} failed jobs` : ""} — open runs`}
								onClick={() => nav(`/runs?day=${b.date}`)}
								onKeyDown={(e) => {
									if (e.key === "Enter") nav(`/runs?day=${b.date}`);
								}}
							>
								{b.failed > 0 && (
									<em
										style={{
											height: `${Math.min((b.failed / Math.max(b.mins, 1)) * 100, 100)}%`,
										}}
									/>
								)}
							</i>
						))}
					</div>
					<div className="bax">
						{buckets.map((b, i) => (
							<span key={i} style={{ visibility: b.today || i % skip === 0 ? undefined : "hidden" }}>
								{b.today ? "today" : b.label}
							</span>
						))}
					</div>
				</div>
			)}
		</div>
	);
}

// Runner-time share by project as a monochrome donut — slices reuse the
// pattern idiom (solid, stripes, dots) instead of invented colours.
// Hover or legend-focus isolates a slice; click opens the project.
function ShareDonut({ u }: { u: UsageResponse["usage"] }) {
	const nav = useNavigate();
	const [active, setActive] = useState<number | null>(null);
	const total = Math.max(
		u.projects.reduce((n, p) => n + p.seconds, 0),
		1,
	);
	const R = 42;
	const C = 2 * Math.PI * R;
	let acc = 0;
	const pats = ["solid", "stripes", "dots"];
	const sel = active != null ? u.projects[active] : null;
	return (
		<div className="panel">
			<div className="phead">
				<span className="pt">Time by project</span>
				<span className="r">share of runner seconds · hover a slice</span>
			</div>
			<div className="donut">
				<svg viewBox="0 0 120 120" className="dsvg">
					<defs>
						<pattern id="dstripes" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
							<rect width="5" height="5" fill="var(--ink)" opacity="0.55" />
							<rect width="2" height="5" fill="var(--paper)" opacity="0.55" />
						</pattern>
						<pattern id="ddots" width="6" height="6" patternUnits="userSpaceOnUse">
							<rect width="6" height="6" fill="var(--ink)" opacity="0.55" />
							<circle cx="3" cy="3" r="1.3" fill="var(--paper)" opacity="0.6" />
						</pattern>
					</defs>
					<circle cx="60" cy="60" r={R} fill="none" stroke="var(--raise2)" strokeWidth="18" />
					{u.projects.map((p, i) => {
						const frac = p.seconds / total;
						const dash = frac * C;
						const dim = active != null && active !== i;
						const el = (
							<circle
								key={p.slug}
								cx="60"
								cy="60"
								r={R}
								fill="none"
								stroke={pats[i % 3] === "solid" ? "var(--ink)" : pats[i % 3] === "stripes" ? "url(#dstripes)" : "url(#ddots)"}
								strokeWidth={active === i ? 22 : 18}
								strokeDasharray={`${dash} ${C - dash}`}
								strokeDashoffset={-acc * C}
								transform="rotate(-90 60 60)"
								opacity={dim ? 0.25 : 1}
								style={{ cursor: "pointer", transition: "opacity .15s, stroke-width .15s" }}
								onMouseEnter={() => setActive(i)}
								onMouseLeave={() => setActive(null)}
								onClick={() => nav(`/projects/${p.slug}`)}
							>
								<title>{`${p.name || p.slug} — ${minutes(p.seconds)} · ${Math.round(frac * 100)}%`}</title>
							</circle>
						);
						acc += frac;
						return el;
					})}
					{sel ? (
						<>
							<text x="60" y="57" textAnchor="middle" className="dv" style={{ fontSize: 13 }}>
								{Math.round((sel.seconds / total) * 100)}%
							</text>
							<text x="60" y="70" textAnchor="middle" className="dk" style={{ fontSize: 8 }}>
								{(sel.name || sel.slug).slice(0, 14)}
							</text>
						</>
					) : (
						<>
							<text x="60" y="57" textAnchor="middle" className="dv">
								{minutes(total)}
							</text>
							<text x="60" y="70" textAnchor="middle" className="dk">
								{u.projects.length} project{u.projects.length === 1 ? "" : "s"}
							</text>
						</>
					)}
				</svg>
				<div className="dleg">
					{u.projects.slice(0, 6).map((p, i) => (
						<Link
							key={p.slug}
							to={`/projects/${p.slug}`}
							className={cn("dl", active === i && "on")}
							onMouseEnter={() => setActive(i)}
							onMouseLeave={() => setActive(null)}
						>
							<i
								className={`phm pat-${pats[i % 3]}`}
								style={{ width: 10, height: 10, borderWidth: 1.5, borderRadius: 3, opacity: active != null && active !== i ? 0.3 : 1 }}
							/>
							<span className="truncate">{p.name || p.slug}</span>
							<b>{Math.round((p.seconds / total) * 100)}%</b>
						</Link>
					))}
				</div>
			</div>
		</div>
	);
}
