import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api } from "../lib/api";
import type { Run } from "../lib/types";
import { Card, EmptyState, PageHeader, Spinner, cn } from "../components/ui";
import { StatusPill } from "../components/status";
import { Row } from "../components/layout";
import { duration, relTime, refName, shortSha } from "../lib/format";

const FILTERS = ["", "in_progress", "queued", "completed", "cancelled"] as const;

export function RunsPage() {
	const nav = useNavigate();
	const [params, setParams] = useSearchParams();
	const status = params.get("status") ?? "";
	const { data, isLoading } = useQuery({
		queryKey: ["runs", status],
		queryFn: () => api.get<{ runs: Run[]; total: number }>(`/runs?limit=100&status=${status}`),
		refetchInterval: 4000,
	});

	return (
		<>
			<PageHeader title="Runs" sub="Workflow executions across all projects" />
			<div className="mb-3 flex gap-1">
				{FILTERS.map((f) => (
					<button
						key={f || "all"}
						onClick={() => setParams(f ? { status: f } : {})}
						className={cn(
							"cursor-pointer rounded-[3px] px-2.5 py-1 text-xs font-medium",
							status === f
								? "bg-[var(--bg-subtle)] text-[var(--text)]"
								: "text-[var(--text-muted)] hover:text-[var(--text)]",
						)}
					>
						{f || "All"}
					</button>
				))}
			</div>
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.runs.length === 0 ? (
						<EmptyState title="No runs" hint="Trigger a workflow from a project page or a webhook." />
					) : (
						data.runs.map((r) => (
							<Row key={r.id} onClick={() => nav(`/runs/${r.id}`)}>
								<StatusPill status={r.status} conclusion={r.conclusion} className="w-24" />
								<span className="min-w-0 flex-1 truncate">
									<span className="font-medium">{r.workflow_name}</span>
									<span className="text-[var(--text-faint)]"> · {r.project_slug} · #{r.run_number}</span>
									{r.commit_message && (
										<span className="ml-2 text-xs text-[var(--text-muted)]">{r.commit_message}</span>
									)}
								</span>
								<span className="text-xs text-[var(--text-muted)]">{r.event}</span>
								<span className="font-mono w-24 truncate text-xs text-[var(--text-muted)]">{refName(r.ref)}</span>
								<span className="font-mono w-16 text-xs text-[var(--text-faint)]">{shortSha(r.sha)}</span>
								<span className="w-16 text-right text-xs text-[var(--text-muted)]">
									{duration(r.started_at, r.completed_at)}
								</span>
								<span className="w-20 text-right text-xs text-[var(--text-faint)]">{relTime(r.created_at)}</span>
							</Row>
						))
					)}
				</Card>
			)}
		</>
	);
}
