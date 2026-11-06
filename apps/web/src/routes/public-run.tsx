import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { ApiError, api } from "../lib/api";
import { PageHeader, Spinner } from "../components/ui";
import { StatusPill, StatusSq } from "../components/status";
import { duration, refName, shortSha } from "../lib/format";

interface PublicStep {
	number: number;
	name: string;
	status: string;
	conclusion: string;
	started_at: string | null;
	completed_at: string | null;
	diagnosis?: { kind: string; summary: string; suggestion: string };
}

interface PublicJob {
	name: string;
	status: string;
	conclusion: string;
	started_at: string | null;
	completed_at: string | null;
	steps: PublicStep[];
}

interface PublicPayload {
	run: {
		id: string;
		run_number: number;
		workflow_name: string;
		event: string;
		ref: string;
		sha: string;
		commit_message: string;
		actor?: string;
		status: string;
		conclusion: string;
		project_slug: string;
		started_at: string | null;
		completed_at: string | null;
	};
	jobs: PublicJob[];
}

export function PublicRunPage() {
	const { id } = useParams<{ id: string }>();
	const { data, isLoading, error } = useQuery({
		queryKey: ["public-run", id],
		queryFn: () => api.get<PublicPayload>(`/public/runs/${id}`),
		retry: false,
		refetchInterval: (q) => {
			if (q.state.status === "error") return false;
			const s = q.state.data?.run.status;
			return s === "completed" || s === "cancelled" ? false : 2000;
		},
	});

	const missing = error instanceof ApiError && (error.status === 404 || error.status === 400);

	return (
		<div className="console-bg min-h-full">
			<header className="flex items-center gap-2.5 border-b border-[var(--line)] px-5 py-3">
				<img src="/logo.svg" alt="" className="size-5 rounded-[4px]" />
				<span className="text-[13px] font-semibold tracking-[0.22em]">KERN</span>
				<span className="text-xs text-[var(--faint)]">Public run</span>
				<Link to="/login" className="ml-auto text-xs text-[var(--dim)] hover:text-[var(--ink)]">
					Sign in
				</Link>
			</header>
			<main className="mx-auto max-w-3xl px-5 py-6">
				{isLoading && <Spinner />}
				{!isLoading && error && (
					<p className="text-sm text-[var(--dim)]">
						{missing ? "Run not found." : "Could not load this run."}
					</p>
				)}
				{data && <PublicBody data={data} />}
			</main>
		</div>
	);
}

function PublicBody({ data }: { data: PublicPayload }) {
	const { run, jobs } = data;
	const title = run.commit_message || `${run.workflow_name} #${run.run_number}`;
	return (
		<>
			<PageHeader
				title={title}
				sub={
					<div className="flex flex-wrap items-baseline gap-x-3.5">
						<span>
							{run.workflow_name} · <b className="font-medium text-[var(--ink)]">#{run.run_number}</b>
						</span>
						<span className="font-mono">{refName(run.ref)}</span>
						<span className="font-mono">{shortSha(run.sha)}</span>
						<span>{run.event}{run.actor ? ` by ${run.actor}` : ""}</span>
					</div>
				}
				actions={
					<>
						<StatusPill status={run.status} conclusion={run.conclusion} />
						<Link to={`/runs/${run.id}`} className="text-xs text-[var(--dim)] hover:text-[var(--ink)]">
							Full run
						</Link>
					</>
				}
			/>
			<div className="panel">
				<div className="phead">
					<span className="pt">Jobs</span>
					<span className="r">{jobs.length}</span>
				</div>
				{jobs.length === 0 ? (
					<div className="px-4 py-3 text-[11px] text-[var(--faint)]">No jobs yet.</div>
				) : (
					jobs.map((job, i) => (
						<div key={`${job.name}-${i}`} className="border-b border-[var(--line)] last:border-0">
							<div className="flex items-center gap-3 px-4 py-2.5">
								<StatusSq status={job.status} conclusion={job.conclusion} />
								<span className="min-w-0 flex-1 truncate font-medium">{job.name}</span>
								<span className="tabular-nums text-xs text-[var(--faint)]">
									{duration(job.started_at, job.completed_at)}
								</span>
							</div>
							{job.steps.length > 0 && (
								<ul className="pb-2">
									{job.steps.map((step) => (
										<li key={step.number} className="flex items-center gap-3 px-4 py-1 pl-9 text-xs">
											<StatusSq status={step.status} conclusion={step.conclusion} />
											<span className="min-w-0 flex-1 text-[var(--dim)]">
												<span className="truncate">{step.name}</span>
												{step.diagnosis && (
													<span className="block text-[var(--faint)]">
														{step.diagnosis.summary} — {step.diagnosis.suggestion}
													</span>
												)}
											</span>
											<span className="tabular-nums text-[var(--faint)]">
												{duration(step.started_at, step.completed_at)}
											</span>
										</li>
									))}
								</ul>
							)}
						</div>
					))
				)}
			</div>
			<p className="mt-3 text-[11px] text-[var(--faint)]">
				Logs stay on the signed-in run page.
			</p>
		</>
	);
}
