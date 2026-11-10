import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { useEffect, useState } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { Download, Wrench } from "lucide-react";
import { api } from "../lib/api";
import type { Annotation, Artifact, Deployment, Job, RunDetail, Runner, Step } from "../lib/types";
import { Button, Spinner, cn } from "../components/ui";

import { useAuth } from "../lib/auth";
import { LogView } from "../components/log-view";
import { RunGantt } from "../components/gantt";
import { bytes, duration, refName, relTime, shortSha } from "../lib/format";

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
	const { data: settings } = useQuery({
		queryKey: ["settings"],
		queryFn: () => api.get<{ public_ci: string }>("/settings"),
		staleTime: 60_000,
	});
	const cancel = useMutation({
		mutationFn: () => api.post(`/runs/${id}/cancel`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run", id] }),
	});
	const rerun = useMutation({
		mutationFn: () => api.post(`/runs/${id}/rerun`),
	});
	const approve = useMutation({
		mutationFn: () => api.post(`/runs/${id}/approve`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["run", id] }),
	});

	const [jobId, setJobId] = useState<string | null>(null);
	const [stepN, setStepN] = useState<number | null>(null);
	// ?job=<key|id> deep links — trace lanes and queue rows land on a
	// specific job's log.
	const [params] = useSearchParams();
	const wantJob = params.get("job");
	useEffect(() => {
		if (!wantJob || !data) return;
		const j = data.jobs.find((j) => j.job_key === wantJob || j.id === wantJob || j.name === wantJob);
		if (j) setJobId(j.id);
	}, [wantJob, data]);

	const runnerNames = new Map((runners?.runners ?? []).map((r) => [r.id, r.name]));

	if (isLoading || !data) return <Spinner />;
	const { run, jobs } = data;
	const live =
		run.status === "in_progress" || run.status === "queued" || run.status === "awaiting_approval";
	const title = run.commit_message?.split("\n")[0] || `${run.workflow_name} #${run.run_number}`;
	const job =
		jobs.find((j) => j.id === jobId) ?? jobs.find((j) => j.status === "in_progress") ?? jobs[0];

	return (
		<div className="page">
			<Link to="/runs" className="back">
				← All runs
			</Link>
			<div className="rtag">
				<b>
					{run.workflow_name} #{run.run_number}
				</b>
				<span>{refName(run.ref)}</span>
				<code>{shortSha(run.sha)}</code>
				<span>
					{run.event} by {run.actor}
				</span>
				<Link to={`/projects/${run.project_slug}`} className="lnk">
					{run.project_name || run.project_slug}
				</Link>
				<Link to={`/runs?project=${run.project_slug}`} className="lnk">
					project runs
				</Link>
				{settings?.public_ci === "on" && (
					<Link to={`/ci/${run.id}`} className="lnk" title="Open the public run page">
						public
					</Link>
				)}
			</div>
			<h2 className="rhead-h">{title}</h2>
			<div
				className={`clock ${live ? "live" : run.conclusion === "failure" || run.status === "cancelled" ? "fail" : ""}`}
			>
				<b>{duration(run.started_at ?? run.created_at, run.completed_at)}</b>
				<span className="cst">
					<i className={`sq ${live ? "live" : run.conclusion === "failure" || run.status === "cancelled" ? "bad" : "ok"}`} />
					{live
						? "running"
						: run.conclusion === "failure"
							? "failed"
							: run.status === "cancelled"
								? "cancelled"
								: "passed"}
					<span className="acts">
						{run.status === "awaiting_approval" && (
							<button className="pill solid" onClick={() => approve.mutate()} disabled={approve.isPending}>
								Approve
							</button>
						)}
						{live && (
							<button className="pill" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
								Cancel
							</button>
						)}
						<button className="pill solid" onClick={() => rerun.mutate()} disabled={rerun.isPending}>
							Re-run
						</button>
					</span>
				</span>
			</div>
			<dl className="rkv">
				<div>
					<dt>Trigger</dt>
					<dd>{run.event}</dd>
				</div>
				<div>
					<dt>Branch</dt>
					<dd>{refName(run.ref)}</dd>
				</div>
				<div>
					<dt>Runner</dt>
					<dd>
						{[...new Set(jobs.map((j) => j.runner_id && runnerNames.get(j.runner_id)).filter(Boolean))].join(
							", ",
						) || "—"}
					</dd>
				</div>
				<div>
					<dt>Image</dt>
					<dd>{jobs.find((j) => j.image)?.image ?? "—"}</dd>
				</div>
				<div>
					<dt>Started</dt>
					<dd>{relTime(run.started_at ?? run.created_at)}</dd>
				</div>
				<div>
					<dt>Jobs</dt>
					<dd>{jobs.length}</dd>
				</div>
			</dl>
			<ApprovalGate
				deployments={data.deployments ?? []}
				jobs={jobs}
				onDone={() => qc.invalidateQueries({ queryKey: ["run", id] })}
			/>
			<RunGantt
				run={run}
				jobs={jobs}
				runners={runnerNames}
				onPickJob={(jid) => {
					setJobId(jid);
					setStepN(null);
				}}
			/>
			{job && (
				<StepsPanel
					jobs={jobs}
					job={job}
					runners={runnerNames}
					onPickJob={setJobId}
					stepN={stepN}
					onPickStep={setStepN}
					live={live}
				/>
			)}
			<RunArtifacts runId={run.id} />
		</div>
	);
}

function StepsPanel({
	jobs,
	job,
	runners,
	onPickJob,
	stepN,
	onPickStep,
	live,
}: {
	jobs: Job[];
	job: Job;
	runners: Map<string, string>;
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
		<div className="tsec gap">
			<div className="sec2">
				<h3>Log</h3>
				<span>
					{job.name}
					{jobs.length > 1 &&
						` · job ${jobs.findIndex((j) => j.id === job.id) + 1} of ${jobs.length}`}
					{job.runner_id && runners.get(job.runner_id) && ` · on ${runners.get(job.runner_id)}`}
					{job.environment && ` · ${job.environment}`}
					{job.classification && ` · ${job.classification}`}
				</span>
				{jobs.length > 1 && (
					<span className="jpick">
						{jobs.map((j) => (
							<button
								key={j.id}
								onClick={() => {
									onPickJob(j.id);
									onPickStep(null);
								}}
								className={j.id === job.id ? "on" : ""}
							>
								{j.name}
							</button>
						))}
					</span>
				)}
				<span className="r">
					{sel ? `step ${sel.number} of ${steps.length}` : `${steps.length} steps`}
					{job.started_at && ` · ${duration(job.started_at, job.completed_at)}`}
				</span>
			</div>
			<div className="lg">
				<div className="steps">
					{steps.length === 0 && (
						<div className="px-4 py-3 text-[11px] text-[var(--faint)]">
							{job.queue_reason
								? `Queued: ${job.queue_reason} (runs-on: ${job.runs_on.join(", ")}).`
								: job.status === "waiting_approval"
									? "Waiting for deployment approval."
									: "No steps yet."}
						</div>
					)}
					{job.queue_reason && steps.length > 0 && (
						<div className="px-4 py-2 text-[11px] text-[var(--warn)]">
							Re-queued: {job.queue_reason} (runs-on: {job.runs_on.join(", ")}).
						</div>
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
					<div className="logwait">
						{job.status === "waiting_approval"
							? `Deployment to ${job.environment || "this environment"} is waiting for approval.`
							: "Waiting for the first step."}
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
				? "r"
				: step.status === "completed"
					? "done"
					: step.status === "skipped" || step.status === "cancelled"
						? "skip"
						: "q";
	return (
		<button className={cn("st", cls, sel && "sel")} onClick={onClick}>
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
			{(() => {
				const anns = step.annotations ?? [];
				const bad = anns.filter((a) => a.level === "error" || a.level === "warning").length;
				return bad > 0 ? (
					<span
						className="shrink-0 font-mono text-[10px]"
						style={{ color: anns.some((a) => a.level === "error") ? "var(--err)" : "var(--warn)" }}
						title={`${bad} annotation${bad === 1 ? "" : "s"}`}
					>
						{bad}
					</span>
				) : null;
			})()}
			<span className="tm">{duration(step.started_at, step.completed_at)}</span>
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
		!step.diagnosis.remediation.applied &&
		!step.diagnosis.remediation.dismissed;
	return (
		<div className="flex min-w-0 flex-col">
			{step.diagnosis && (
				<div className="flex items-center gap-2 border-b border-[var(--line)] px-3.5 py-2 text-[11px] text-[var(--err)]">
					<span className="min-w-0 flex-1">
						{step.diagnosis.kind}: {step.diagnosis.summary} — {step.diagnosis.suggestion}
						{step.diagnosis.remediation && ` (${step.diagnosis.remediation.result || "proposed"})`}
						{fixPending && (step.diagnosis.remediation?.fix?.length ?? 0) > 0 && (
							<span className="mt-1 block rounded border border-[var(--line)] bg-[var(--bg)] px-2 py-1 font-mono text-[10px] text-[var(--dim)]">
								runs once as root in the retried job's container:{" "}
								{step.diagnosis.remediation!.fix!.join(" && ")}
							</span>
						)}
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
			{step.annotations && step.annotations.length > 0 && <AnnotationList annotations={step.annotations} />}
			<LogView stepId={step.id} active={live} header={step.name} />
		</div>
	);
}

// AnnotationList renders the ::error::/::warning::/::notice:: workflow
// commands a step emitted, above its log. Color carries level only.
function AnnotationList({ annotations }: { annotations: Annotation[] }) {
	const color = (l: string) =>
		l === "error" ? "var(--err)" : l === "warning" ? "var(--warn)" : "var(--faint)";
	return (
		<div className="max-h-48 overflow-y-auto border-b border-[var(--line)]">
			{annotations.map((a, i) => (
				<div key={i} className="flex items-baseline gap-2 px-3.5 py-1.5 text-[11px]">
					<span
						className="w-14 shrink-0 font-mono text-[10px] uppercase"
						style={{ color: color(a.level) }}
					>
						{a.level}
					</span>
					<span className="min-w-0 flex-1 whitespace-pre-wrap">
						{a.title && <span className="font-medium">{a.title}: </span>}
						{a.file && (
							<span className="font-mono text-[10px] text-[var(--faint)]">
								{a.file}
								{a.line ? `:${a.line}` : ""}
								{a.col ? `:${a.col}` : ""}
								{" — "}
							</span>
						)}
						{a.message}
					</span>
				</div>
			))}
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
		<div className="tsec gap">
			<div className="sec2">
				<h3>Artifacts</h3>
			</div>
			<div className="plate" style={{ padding: "6px 22px" }}>
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

function ApprovalGate({
	deployments,
	jobs,
	onDone,
}: {
	deployments: Deployment[];
	jobs: Job[];
	onDone: () => void;
}) {
	const { user } = useAuth();
	const pending = deployments.filter((d) =>
		jobs.some((j) => j.id === d.job_id && j.status === "waiting_approval"),
	);
	if (pending.length === 0) return null;
	return (
		<div className="plate" style={{ padding: "10px 22px", marginBottom: 30 }}>
			<div className="sec2" style={{ marginTop: 6 }}>
				<h3>Deployment approval</h3>
				<span>{pending.length} waiting</span>
			</div>
			{pending.map((d) => (
				<ApprovalRow
					key={d.id}
					d={d}
					email={user?.email ?? ""}
					role={user?.role ?? ""}
					onDone={onDone}
				/>
			))}
		</div>
	);
}

function ApprovalRow({
	d,
	email,
	role,
	onDone,
}: {
	d: Deployment;
	email: string;
	role: string;
	onDone: () => void;
}) {
	const qc = useQueryClient();
	const approvals = d.approvals ?? [];
	const mine = approvals.some((a) => a.email === email);
	const list = d.reviewer_emails ?? [];
	const allowed = role === "owner" || role === "admin" || list.length === 0 || list.includes(email);
	const timer =
		d.wait_until && new Date(d.wait_until).getTime() > Date.now()
			? `wait ${Math.max(1, Math.round((new Date(d.wait_until).getTime() - Date.now()) / 60000))}m`
			: "";
	const act = useMutation({
		mutationFn: (op: "approve" | "reject") => api.post(`/jobs/${d.job_id}/${op}`),
		onSuccess: () => {
			onDone();
			qc.invalidateQueries({ queryKey: ["deployments"] });
		},
	});
	return (
		<div className="flex flex-wrap items-center gap-3 border-b border-[var(--line)] px-4 py-2.5 last:border-0">
			<span className="font-mono text-[13px]">{d.environment_name}</span>
			<span className="text-xs text-[var(--dim)]">{d.job_name}</span>
			<span className="font-mono text-xs text-[var(--faint)]">
				{approvals.length}/{d.required_approvals}
				{timer ? ` · ${timer}` : ""}
				{approvals.length > 0 ? ` · ${approvals.map((a) => a.email).join(", ")}` : ""}
			</span>
			<span className="ml-auto flex items-center gap-2">
				{allowed ? (
					<>
						<Button
							size="sm"
							variant="primary"
							disabled={act.isPending || mine}
							onClick={() => act.mutate("approve")}
						>
							{mine ? "Approved" : "Approve"}
						</Button>
						<Button
							size="sm"
							variant="danger"
							disabled={act.isPending}
							onClick={() => act.mutate("reject")}
						>
							Reject
						</Button>
					</>
				) : (
					<span className="text-xs text-[var(--faint)]">reviewers only</span>
				)}
			</span>
			{act.error && <span className="w-full text-xs text-[var(--err)]">{act.error.message}</span>}
		</div>
	);
}
