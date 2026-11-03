import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import type { OverviewResponse } from "../lib/types";
import { Card, EmptyState, PageHeader, Spinner } from "../components/ui";
import { StatusPill } from "../components/status";
import { Row } from "../components/layout";
import { duration, relTime, seconds } from "../lib/format";

export function OverviewPage() {
	const nav = useNavigate();
	const { data, isLoading } = useQuery({
		queryKey: ["overview"],
		queryFn: () => api.get<OverviewResponse>("/overview"),
		refetchInterval: 5000,
	});

	if (isLoading || !data) return <Spinner />;
	const o = data.overview;

	const stats = [
		{ label: "Active runs", value: o.runs_active },
		{ label: "Queued jobs", value: o.jobs_queued },
		{ label: "Running jobs", value: o.jobs_running },
		{ label: "Runners online", value: `${o.runners_online}/${o.runners_total}` },
		{ label: "Runs total", value: o.runs_total },
		{ label: "Success rate", value: o.runs_total ? `${Math.round((o.runs_success / o.runs_total) * 100)}%` : "—" },
		{ label: "Avg queue wait", value: seconds(o.avg_queue_seconds) },
		{ label: "P95 queue wait", value: seconds(o.p95_queue_seconds) },
		{ label: "Avg run duration", value: seconds(o.avg_run_seconds) },
		{ label: "Cache", value: `${data.cache.entries} entries` },
	];

	return (
		<>
			<PageHeader title="Overview" sub="Control plane activity across all projects" />
			<div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
				{stats.map((s) => (
					<Card key={s.label} className="p-3">
						<div className="text-xs text-[var(--text-faint)]">{s.label}</div>
						<div className="mt-1 text-lg font-semibold tabular-nums">{s.value}</div>
					</Card>
				))}
			</div>
			<h2 className="mb-2 text-[13px] font-semibold">Recent failures</h2>
			<Card>
				{data.failures.length === 0 ? (
					<EmptyState title="No failed runs" hint="Failures from the last window appear here." />
				) : (
					data.failures.map((r) => (
						<Row key={r.id} onClick={() => nav(`/runs/${r.id}`)}>
							<StatusPill status={r.status} conclusion={r.conclusion} />
							<span className="min-w-0 flex-1 truncate">
								<span className="font-medium">{r.workflow_name}</span>
								<span className="text-[var(--text-faint)]"> · {r.project_slug} · #{r.run_number}</span>
							</span>
							<span className="font-mono text-xs text-[var(--text-faint)]">{r.ref.replace("refs/heads/", "")}</span>
							<span className="w-16 text-right text-xs text-[var(--text-muted)]">
								{duration(r.started_at, r.completed_at)}
							</span>
							<span className="w-20 text-right text-xs text-[var(--text-faint)]">{relTime(r.created_at)}</span>
						</Row>
					))
				)}
			</Card>
		</>
	);
}
