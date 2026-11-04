import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router-dom";
import { useState } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { Download, Wrench } from "lucide-react";
import { api } from "../lib/api";
import type { Artifact, Job, RunDetail, Runner, Step } from "../lib/types";
import { Button, PageHeader, Spinner, cn } from "../components/ui";
import { StatusPill } from "../components/status";
import { LogView } from "../components/log-view";
import { RunGantt } from "../components/gantt";
import { CopyButton } from "./settings";
import { bytes, duration, refName, shortSha } from "../lib/format";

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
	const { data: runners } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
	});
	const cancel = useMutation({
		mutationFn: () => api.post(`/runs/${id}/cancel`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run", id] }),
	});
	const rerun = useMutation({
		mutationFn: () => api.post(`/runs/${id}/rerun`),
	});

	const [jobId, setJobId] = useState<string | null>(null);
	const [stepN, setStepN] = useState<number | null>(null);

	const runnerNames = new Map((runners?.runners ?? []).map((r) => [r.id, r.name]));

	if (isLoading || !data) return <Spinner />;
	const { run, jobs } = data;
	const live = run.status === "in_progress" || run.status === "queued";
	const title = run.commit_message?.split("\n")[0] || `${run.workflow_name} #${run.run_number}`;
	const job =
		jobs.find((j) => j.id === jobId) ?? jobs.find((j) => j.status === "in_progress") ?? jobs[0];

	return (
		<>
			<PageHeader
				title={title}
				sub={
					<div className="flex flex-wrap items-baseline gap-x-3.5">
						<span>
							{run.workflow_name} ·{" "}
							<b className="font-medium text-[var(--ink)]">#{run.run_number}</b>
						</span>
						<span className="font-mono">{refName(run.ref)}</span>
						<span className="flex items-center gap-1 font-mono">
							{shortSha(run.sha)}
							<CopyButton text={run.sha} />
						</span>
						<span>
							{run.event} by {run.actor}
						</span>
						<span>started {new Date(run.started_at ?? run.created_at).toLocaleTimeString()}</span>
					</div>
				}
				actions={
					<>
						<StatusPill status={run.status} conclusion={run.conclusion} className="normal-case" />
						{live && (
							<Button
								variant="danger"
								size="sm"
								onClick={() => cancel.mutate()}
								disabled={cancel.isPending}
							>
								Cancel
							</Button>
						)}
						<Button
							variant="primary"
							size="sm"
							onClick={() => rerun.mutate()}
							disabled={rerun.isPending}
						>
							Re-run
						</Button>
					</>
				}
			/>
			<RunGantt run={run} jobs={jobs} runners={runnerNames} />
			{job && (
				<StepsPanel
					jobs={jobs}
					job={job}
					onPickJob={setJobId}
					stepN={stepN}
					onPickStep={setStepN}
					live={live}
				/>
			)}
			<RunArtifacts runId={run.id} />
		</>
	);
}

function StepsPanel({
	jobs,
	job,
	onPickJob,
	stepN,
	onPickStep,
	live,
}: {
	jobs: Job[];
	job: Job;
	onPickJob: (id: string) => void;
	stepN: number | null;
	onPickStep: (n: number | null) => void;
	live: boolean;
}) {
	const steps = job.steps ?? [];
	const sel =
		steps.find((s) => s.number === stepN) ??
		steps.find((s) => s.status === "running") ??
		steps[steps.length - 1];
	return (
		<div className="panel mt-4">
			<div className="phead">
				<span className="pt">Steps · {job.name}</span>
				{jobs.length > 1 && (
					<span className="flex gap-3">
						{jobs.map((j) => (
							<button
								key={j.id}
								onClick={() => {
									onPickJob(j.id);
									onPickStep(null);
								}}
								className={cn(
									"cursor-pointer border-0 bg-transparent p-0 text-[11px]",
									j.id === job.id
										? "text-[var(--ink)]"
										: "text-[var(--faint)] hover:text-[var(--dim)]",
								)}
							>
								{j.name}
							</button>
						))}
					</span>
				)}
				<span className="r">
					{sel ? `step ${sel.number} of ${steps.length}` : `${steps.length} steps`}
					{job.runner_id && <span>·</span>}
					{duration(job.started_at, job.completed_at)}
				</span>
			</div>
			<div className="sbody">
				<div className="slist">
					{steps.length === 0 && (
						<div className="px-4 py-3 text-[11px] text-[var(--faint)]">No steps yet.</div>
					)}
					{steps.map((s) => (
						<StepButton
							key={s.id}
							step={s}
							sel={sel?.id === s.id}
							onClick={() => onPickStep(s.number)}
						/>
					))}
				</div>
				{sel ? (
					<StepLog key={sel.id} job={job} step={sel} live={live && sel.status === "running"} />
				) : (
					<div className="px-4 py-8 text-[11px] text-[var(--faint)]">
						Waiting for the first step.
					</div>
				)}
			</div>
		</div>
	);
}

function StepButton({ step, sel, onClick }: { step: Step; sel: boolean; onClick: () => void }) {
	const cls =
		step.conclusion === "failure"
			? "err"
			: step.status === "running"
				? "live"
				: step.status === "completed"
					? "done"
					: step.status === "skipped" || step.status === "cancelled"
						? "skip"
						: "pend";
	return (
		<button className={cn("step", cls, sel && "sel")} onClick={onClick}>
			<span className="no">{String(step.number).padStart(2, "0")}</span>
			<span className="g">
				{cls === "done" && (
					<svg viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.8">
						<path d="M1.5 5.5l2.4 2.4 4.6-5" />
					</svg>
				)}
				{cls === "err" && (
					<svg viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.8">
						<path d="M2 2l6 6M8 2l-6 6" />
					</svg>
				)}
			</span>
			<span className="min-w-0 flex-1 truncate">{step.name}</span>
			<span className="t">{duration(step.started_at, step.completed_at)}</span>
		</button>
	);
}

// StepLog renders the selected step's stream plus its diagnosis row when the
// doctor flagged it — same wiring as before, restyled.
function StepLog({ job, step, live }: { job: Job; step: Step; live: boolean }) {
	const qc = useQueryClient();
	const approve = useMutation({
		mutationFn: () => api.post(`/jobs/${job.id}/remediate`, { step: step.number }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run"] }),
	});
	const fixPending =
		!!step.diagnosis?.fixable &&
		!!step.diagnosis?.remediation &&
		!step.diagnosis.remediation.approved &&
		!step.diagnosis.remediation.applied;
	return (
		<div className="flex min-w-0 flex-col">
			{step.diagnosis && (
				<div className="flex items-center gap-2 border-b border-[var(--line)] px-3.5 py-2 text-[11px] text-[var(--err)]">
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
			{step.has_summary && <StepSummary stepId={step.id} />}
			<LogView stepId={step.id} active={live} header={step.name} />
		</div>
	);
}

function RunArtifacts({ runId }: { runId: string }) {
	const { data } = useQuery({
		queryKey: ["artifacts", runId],
		queryFn: () => api.get<{ artifacts: Artifact[] }>(`/artifacts?run_id=${runId}`),
	});
	if (!data || data.artifacts.length === 0) return null;
	return (
		<div className="panel mt-4">
			<div className="phead">
				<span className="pt">Artifacts</span>
			</div>
			{data.artifacts.map((a) => (
				<div
					key={a.id}
					className="flex items-center gap-3 border-b border-[var(--line)] px-4 py-2.5 last:border-0"
				>
					<span className="min-w-0 flex-1 truncate font-mono text-xs">{a.name}</span>
					<span className="w-16 shrink-0 text-right text-xs text-[var(--faint)]">
						{bytes(a.size_bytes)}
					</span>
					<a
						href={`/api/v1/artifacts/${a.id}/download`}
						download
						className="shrink-0 rounded p-1.5 text-[var(--dim)] hover:bg-[var(--raise2)] hover:text-[var(--ink)]"
						title={`Download ${a.name}.tar.gz`}
					>
						<Download size={14} />
					</a>
				</div>
			))}
		</div>
	);
}

// StepSummary renders a step's GITHUB_STEP_SUMMARY markdown, sanitized before
// it reaches the DOM.
function StepSummary({ stepId }: { stepId: string }) {
	const { data } = useQuery({
		queryKey: ["step-summary", stepId],
		queryFn: () => api.get<{ text: string }>(`/steps/${stepId}/summary`),
	});
	if (!data?.text) return null;
	return (
		<div className="border-b border-[var(--line)] p-3">
			<div className="mb-2 text-[11px] text-[var(--faint)]">Step summary</div>
			<div
				className="prose-kern text-[13px]"
				dangerouslySetInnerHTML={{
					__html: DOMPurify.sanitize(marked.parse(data.text, { async: false }), {
						USE_PROFILES: { html: true },
					}),
				}}
			/>
		</div>
	);
}
