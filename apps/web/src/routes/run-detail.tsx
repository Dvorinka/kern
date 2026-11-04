import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router-dom";
import { useState } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { Ban, ChevronDown, ChevronRight, Download, RotateCw, Wrench } from "lucide-react";
import { api } from "../lib/api";
import type { Artifact, Job, RunDetail, Step } from "../lib/types";
import { Button, Card, KV, PageHeader, Spinner, cn } from "../components/ui";
import { StatusPill } from "../components/status";
import { LogView } from "../components/log-view";
import { JobDAG } from "../components/dag";
import { Row } from "../components/layout";
import { bytes, duration, relTime, refName, shortSha } from "../lib/format";

export function RunDetailPage() {
	const { id } = useParams<{ id: string }>();
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["run", id],
		queryFn: () => api.get<RunDetail>(`/runs/${id}`),
		refetchInterval: (q) => {
			const s = q.state.data?.run.status;
			return s === "completed" || s === "cancelled" ? false : 2000;
		},
	});
	const cancel = useMutation({
		mutationFn: () => api.post(`/runs/${id}/cancel`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run", id] }),
	});
	const rerun = useMutation({
		mutationFn: () => api.post(`/runs/${id}/rerun`),
	});

	if (isLoading || !data) return <Spinner />;
	const { run, jobs } = data;
	const live = run.status === "in_progress" || run.status === "queued";

	return (
		<>
			<PageHeader
				title={`${run.workflow_name} #${run.run_number}`}
				sub={`${run.project_slug} · ${run.event} · ${run.actor}`}
				actions={
					<>
						{live && (
							<Button variant="danger" size="sm" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
								<Ban size={13} /> Cancel
							</Button>
						)}
						<Button size="sm" onClick={() => rerun.mutate()} disabled={rerun.isPending}>
							<RotateCw size={13} /> Re-run
						</Button>
					</>
				}
			/>
			<Card className="mb-4 p-4">
				<div className="grid grid-cols-2 gap-x-8 md:grid-cols-3">
					<KV k="Status" v={<StatusPill status={run.status} conclusion={run.conclusion} />} />
					<KV k="Ref" v={refName(run.ref)} mono />
					<KV k="Commit" v={shortSha(run.sha)} mono />
					<KV k="Trust" v={run.trust_level} />
					<KV k="Duration" v={duration(run.started_at, run.completed_at)} />
					<KV k="Created" v={relTime(run.created_at)} />
				</div>
			</Card>
			{jobs.length > 1 && (
				<Card className="mb-4 overflow-x-auto p-4">
					<JobDAG jobs={jobs} />
				</Card>
			)}
			<div className="space-y-3">
				{jobs.map((j) => (
					<JobCard key={j.id} job={j} />
				))}
			</div>
			<RunArtifacts runId={run.id} />
		</>
	);
}

function RunArtifacts({ runId }: { runId: string }) {
	const { data } = useQuery({
		queryKey: ["artifacts", runId],
		queryFn: () => api.get<{ artifacts: Artifact[] }>(`/artifacts?run_id=${runId}`),
	});
	if (!data || data.artifacts.length === 0) return null;
	return (
		<Card className="mt-4">
			<div className="border-b border-[var(--border)] px-4 py-2.5">
				<span className="klabel">Artifacts</span>
			</div>
			{data.artifacts.map((a) => (
				<Row key={a.id}>
					<span className="min-w-0 flex-1 truncate font-mono text-[13px]">{a.name}</span>
					<span className="w-16 shrink-0 text-right text-xs text-[var(--text-faint)]">{bytes(a.size_bytes)}</span>
					<a
						href={`/api/v1/artifacts/${a.id}/download`}
						download
						className="shrink-0 rounded p-1.5 text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]"
						title={`Download ${a.name}.tar.gz`}
					>
						<Download size={14} />
					</a>
				</Row>
			))}
		</Card>
	);
}

function JobCard({ job }: { job: Job }) {
	const [open, setOpen] = useState<Record<number, boolean>>({});
	return (
		<Card>
			<div className="flex items-center gap-3 px-4 py-2.5">
				<StatusPill status={job.status} conclusion={job.conclusion} className="w-24" />
				<span className="min-w-0 flex-1 truncate font-medium">{job.name}</span>
				{job.matrix && Object.keys(job.matrix).length > 0 && (
					<span className="font-mono text-xs text-[var(--text-faint)]">
						{Object.entries(job.matrix)
							.map(([k, v]) => `${k}=${v}`)
							.join(" ")}
					</span>
				)}
				<span className="font-mono text-xs text-[var(--text-faint)]">{job.image}</span>
				<span className="text-xs text-[var(--text-muted)]">{duration(job.started_at, job.completed_at)}</span>
			</div>
			{job.error && <div className="border-t border-[var(--border)] px-4 py-2 text-xs text-[var(--err)]">{job.error}</div>}
			{(job.steps ?? []).map((s) => (
				<StepRow key={s.id} jobId={job.id} step={s} open={!!open[s.number]} onToggle={() => setOpen((o) => ({ ...o, [s.number]: !o[s.number] }))} />
			))}
		</Card>
	);
}

function StepRow({ jobId, step, open, onToggle }: { jobId: string; step: Step; open: boolean; onToggle: () => void }) {
	const qc = useQueryClient();
	const live = step.status === "running";
	const approve = useMutation({
		mutationFn: () => api.post(`/jobs/${jobId}/remediate`, { step: step.number }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run"] }),
	});
	const fixPending =
		!!step.diagnosis?.fixable &&
		!!step.diagnosis?.remediation &&
		!step.diagnosis.remediation.approved &&
		!step.diagnosis.remediation.applied;
	return (
		<div className={cn("border-t border-[var(--border)]", open && "bg-[var(--bg-subtle)]/50")}>
			<button
				onClick={onToggle}
				className="flex w-full cursor-pointer items-center gap-3 px-4 py-2 text-left hover:bg-[var(--bg-subtle)]"
			>
				{open ? <ChevronDown size={13} className="text-[var(--text-faint)]" /> : <ChevronRight size={13} className="text-[var(--text-faint)]" />}
				<span className="w-8 text-xs text-[var(--text-faint)]">#{step.number}</span>
				<span className="min-w-0 flex-1 truncate text-[13px]">{step.name}</span>
				<StatusPill status={step.status} conclusion={step.conclusion} />
				<span className="w-16 text-right text-xs text-[var(--text-muted)]">{duration(step.started_at, step.completed_at)}</span>
			</button>
			{step.diagnosis && (
				<div className="flex items-center gap-2 border-t border-[var(--border)] bg-[var(--err)]/5 px-4 py-2 text-[11px] text-[var(--err)]">
					<span className="min-w-0 flex-1">
						{step.diagnosis.kind}: {step.diagnosis.summary} — {step.diagnosis.suggestion}
						{step.diagnosis.remediation && ` (${step.diagnosis.remediation.result || "proposed"})`}
					</span>
					{fixPending && (
						<Button size="sm" disabled={approve.isPending} onClick={() => approve.mutate()}>
							<Wrench size={12} /> Approve fix
						</Button>
					)}
					{approve.error && <span className="text-[var(--err)]">{approve.error.message}</span>}
				</div>
			)}
			{open && (
				<div className="space-y-3 px-4 pb-3">
					{step.has_summary && <StepSummary stepId={step.id} />}
					<LogView stepId={step.id} active={live} />
				</div>
			)}
		</div>
	);
}

// StepSummary renders a step's GITHUB_STEP_SUMMARY markdown, sanitized before it
// reaches the DOM.
function StepSummary({ stepId }: { stepId: string }) {
	const { data } = useQuery({
		queryKey: ["step-summary", stepId],
		queryFn: () => api.get<{ text: string }>(`/steps/${stepId}/summary`),
	});
	if (!data?.text) return null;
	return (
		<div className="rounded-[3px] border border-[var(--border)] bg-[var(--bg-raised)] p-3">
			<div className="mb-2 text-[10px] font-medium uppercase tracking-wide text-[var(--text-faint)]">
				Step summary
			</div>
			<div
				className="prose-kern text-[13px]"
				dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(marked.parse(data.text, { async: false })) }}
			/>
		</div>
	);
}
