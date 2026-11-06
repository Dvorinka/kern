import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useMemo, useState } from "react";
import { api } from "../lib/api";
import type { UsageResponse } from "../lib/types";
import { Card, CardHeader, EmptyState, ErrorNote, PageHeader, SkeletonRows } from "../components/ui";
import { seconds } from "../lib/format";

const WINDOWS = [7, 30, 90];
// GitHub's free tier for private repos on the Free plan — the honest
// comparison unit, not a quota KERN enforces.
const FREE_TIER_MIN = 2000;

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
	const { data, isLoading, error, refetch } = useQuery({
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
					k: "Billable minutes",
					v: t.billable_minutes.toLocaleString(),
					c: `per-job ceiling · GitHub Linux 2-core @ ${usd(data!.rate_usd_per_min)}/min`,
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

			{isLoading ? (
				<Card>
					<SkeletonRows rows={6} />
				</Card>
			) : error ? (
				<Card>
					<ErrorNote error={error} onRetry={() => refetch()} />
				</Card>
			) : !u ? null : (
				<>
					<SavingsCard u={u} res={data!} days={days} />
					<div className="panel kpis mt-4">
						{kpis.map((k) => (
							<div className="kpi" key={k.k}>
								<div className="k">{k.k}</div>
								<div className="v">{k.v}</div>
								<div className="c fl">{k.c}</div>
							</div>
						))}
					</div>

					<MinutesChart u={u} days={days} />

					{u.failures.length > 0 && (
						<Card className="mt-4">
							<CardHeader
								title="Failure causes"
								sub="failed steps by diagnosis kind — which workflow line keeps breaking"
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
							<CardHeader title="By project" sub="runner time and hosted equivalent" />
							{u.projects.length === 0 ? (
								<EmptyState title="No completed jobs in this window." />
							) : (
								<table className="tbl">
									<thead>
										<tr>
											<th>Project</th>
											<th className="num">Runs</th>
											<th className="num">Time</th>
											<th className="num">≈ hosted</th>
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
												<td
													className="num"
													title={`${p.billable_minutes.toLocaleString()} billable min`}
												>
													{usd(p.billable_minutes * data!.rate_usd_per_min)}
												</td>
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

// SavingsCard reads instantly — one big honest number: what GitHub-hosted
// runners would have billed for the same jobs, plus where the window sits
// against the 2,000-minute free tier. An equivalence, never an invoice.
function SavingsCard({
	u,
	res,
	days,
}: {
	u: UsageResponse["usage"];
	res: UsageResponse;
	days: number;
}) {
	const billable = u.totals.billable_minutes;
	const tiers = billable / FREE_TIER_MIN;
	const tierText =
		billable === 0
			? "no billable minutes this window"
			: tiers >= 1
				? `${tiers.toFixed(1)}× the GitHub free tier`
				: `${Math.round(tiers * 100)}% of the GitHub free tier`;
	return (
		<Card>
			<div className="save">
				<div className="big">
					≈ {usd(res.hosted_equiv_usd)}
					<small> hosted-equivalent · {days}d</small>
				</div>
				<div className="sub">
					<b>{billable.toLocaleString()} billable min</b> — {tierText} ({FREE_TIER_MIN.toLocaleString()}{" "}
					min/mo on Linux 2-core @ {usd(res.rate_usd_per_min)}/min)
					{billable > FREE_TIER_MIN && (
						<>
							{" · "}
							<b>+{(billable - FREE_TIER_MIN).toLocaleString()} min</b> over a free month
						</>
					)}
				</div>
				<div className="foot">
					Equivalence, not an invoice — the fleet runs on your own hardware; GitHub would have billed
					this for the same jobs. Billable minutes round each job up, exactly as GitHub meters.
				</div>
			</div>
		</Card>
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
