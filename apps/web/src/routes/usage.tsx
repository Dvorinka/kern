import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useMemo, useState } from "react";
import { api } from "../lib/api";
import type { UsageResponse } from "../lib/types";
import { Card, CardHeader, EmptyState, PageHeader, Spinner } from "../components/ui";
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

	const u = data?.usage;
	const t = u?.totals;
	const successRate =
		t && t.runs > 0 ? `${Math.round((t.runs_success / t.runs) * 100)}%` : "—";

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
			]
		: [];

	return (
		<>
			<PageHeader
				title="Usage"
				sub="What the fleet consumed — and what GitHub-hosted runners would have billed for the same work. Billable minutes round each job up to the whole minute, exactly as GitHub meters."
				actions={
					<div className="tabs" style={{ border: 0 }}>
						{WINDOWS.map((w) => (
							<button
								key={w}
								className={w === days ? "on" : ""}
								onClick={() => setDays(w)}
							>
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
													{t!.runner_seconds > 0
														? `${Math.round((p.seconds / t!.runner_seconds) * 100)}%`
														: "—"}
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
												<td className="lead">{r.name}</td>
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
	// Backfill days the fleet was idle so the axis reads as a real calendar.
	const buckets = useMemo(() => {
		const byDate = new Map(u.days.map((d) => [d.date.slice(0, 10), d]));
		const out: { label: string; mins: number; failed: number; today: boolean }[] = [];
		for (let i = days - 1; i >= 0; i--) {
			const d = new Date();
			d.setDate(d.getDate() - i);
			const key = d.toISOString().slice(0, 10);
			const row = byDate.get(key);
			out.push({
				label: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
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
		<Card className="mt-4">
			<CardHeader
				title="Billable minutes per day"
				sub="per-job ceiling — the meter GitHub would read"
			/>
			<div className="chart">
				<div className="ct">
					<span>{yMax}m</span>
					<span>{Math.round((yMax * 2) / 3)}m</span>
					<span>{Math.round(yMax / 3)}m</span>
					<span>0</span>
				</div>
				{!any && <div className="empty-lbl">No completed jobs in this window.</div>}
				{buckets.map((b, i) => (
					<div className={`col${b.today ? " today" : ""}`} key={i}>
						<div
							className="b"
							style={{ height: `${(b.mins / yMax) * 100}%` }}
							title={`${b.label} · ${b.mins} billed min${b.failed ? `, ${b.failed} failed jobs` : ""}`}
						>
							{b.failed > 0 && (
								<div
									className="f"
									style={{ height: `${Math.min((b.failed / Math.max(b.mins, 1)) * 100, 100)}%` }}
								/>
							)}
						</div>
						{(b.today || i % skip === 0) && (
							<span className="lbl">{b.today ? "today" : b.label}</span>
						)}
					</div>
				))}
			</div>
		</Card>
	);
}
