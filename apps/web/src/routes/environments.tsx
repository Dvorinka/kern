import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import type { Deployment, Environment, Project } from "../lib/types";
import { Button, Card, EmptyState, ErrorNote, Input, PageHeader, SkeletonRows, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { StatusSq } from "../components/status";
import { refName, relTime, shortSha } from "../lib/format";

export function EnvironmentsPage() {
	const [params, setParams] = useSearchParams();
	const projects = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	const list = projects.data?.projects ?? [];
	const asked = params.get("project") ?? "";
	const slug = list.some((p) => p.slug === asked) ? asked : (list[0]?.slug ?? "");

	useEffect(() => {
		if (slug && slug !== asked) setParams({ project: slug }, { replace: true });
	}, [slug, asked, setParams]);

	const envs = useQuery({
		queryKey: ["environments", slug],
		enabled: slug !== "",
		queryFn: () =>
			api.get<{ environments: Environment[] }>(`/projects/${encodeURIComponent(slug)}/environments`),
	});
	const deps = useQuery({
		queryKey: ["deployments", slug],
		enabled: slug !== "",
		queryFn: () =>
			api.get<{ deployments: Deployment[] }>(`/deployments?project=${encodeURIComponent(slug)}`),
		refetchInterval: 5000,
	});

	const [adding, setAdding] = useState(false);

	if (projects.isLoading) return <Spinner />;

	return (
		<>
			<PageHeader
				title="Environments"
				sub="A protected environment can require reviewers, a wait, and a branch pattern"
				actions={
					list.length > 0 && (
						<>
							<select
								className="h-7 rounded-lg border border-[var(--ctl)] bg-transparent px-2 text-xs"
								value={slug}
								onChange={(e) => setParams({ project: e.target.value })}
								aria-label="Project"
							>
								{list.map((p) => (
									<option key={p.id} value={p.slug}>
										{p.slug}
									</option>
								))}
							</select>
							<Button size="sm" variant="primary" onClick={() => setAdding((a) => !a)}>
								Add environment
							</Button>
						</>
					)
				}
			/>
			{list.length === 0 ? (
				<Card>
					<EmptyState title="No projects" hint="Create a project before naming environments." />
				</Card>
			) : (
				<>
					{adding && slug && (
						<AddEnvironment
							slug={slug}
							onClose={() => setAdding(false)}
						/>
					)}
					<Card>
						{envs.isLoading ? (
							<SkeletonRows />
						) : envs.error ? (
							<ErrorNote error={envs.error} onRetry={() => envs.refetch()} />
						) : !envs.data || envs.data.environments.length === 0 ? (
							<EmptyState
								title="No environments"
								hint="A job's environment: key creates one on first use. Protect it here before that run."
							/>
						) : (
							envs.data.environments.map((env) => (
								<EnvRow key={env.id} slug={slug} env={env} />
							))
						)}
					</Card>
					<h2 className="mb-2 mt-6 text-[13px] font-semibold">Deployments</h2>
					<Card>
						{deps.isLoading ? (
							<SkeletonRows />
						) : deps.error ? (
							<ErrorNote error={deps.error} onRetry={() => deps.refetch()} />
						) : !deps.data || deps.data.deployments.length === 0 ? (
							<EmptyState title="No deployments" hint="They appear when a job names an environment." />
						) : (
							deps.data.deployments.map((d) => <DeploymentRow key={d.id} d={d} slug={slug} />)
						)}
					</Card>
				</>
			)}
		</>
	);
}

function AddEnvironment({ slug, onClose }: { slug: string; onClose: () => void }) {
	const qc = useQueryClient();
	const put = useMutation({
		mutationFn: (body: { name: string; protected: boolean; required_approvals: number }) =>
			api.put(`/projects/${encodeURIComponent(slug)}/environments/${encodeURIComponent(body.name)}`, {
				protected: body.protected,
				required_approvals: body.required_approvals,
			}),
		onSuccess: () => {
			onClose();
			qc.invalidateQueries({ queryKey: ["environments", slug] });
		},
	});
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const protected_ = fd.get("protected") === "on";
		let n = Number(fd.get("approvals") || 0);
		if (protected_ && n < 1) n = 1;
		put.mutate({
			name: String(fd.get("name")).trim(),
			protected: protected_,
			required_approvals: n,
		});
	};
	return (
		<Card className="mb-4 p-4">
			<form onSubmit={submit} className="flex flex-wrap items-center gap-2">
				<Input name="name" placeholder="name" required pattern="[^\s=]+" className="w-48" />
				<label className="flex items-center gap-1.5 text-xs text-[var(--dim)]">
					<input name="protected" type="checkbox" className="accent-[var(--ink)]" />
					protected
				</label>
				<Input
					name="approvals"
					type="number"
					min={0}
					max={6}
					defaultValue={1}
					className="w-16"
					aria-label="Required approvals"
				/>
				<Button type="submit" variant="primary" size="sm" disabled={put.isPending}>
					Save
				</Button>
			</form>
			{put.error && <p className="mt-2 text-xs text-[var(--err)]">{put.error.message}</p>}
		</Card>
	);
}

type EnvRule = {
	protected: boolean;
	required_approvals: number;
	reviewer_emails: string[];
	wait_minutes: number;
	branch_policy: "all" | "selected";
	branch_patterns: string[];
};

function splitList(raw: string) {
	return raw
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean);
}

function EnvRow({ slug, env }: { slug: string; env: Environment }) {
	const qc = useQueryClient();
	const put = useMutation({
		mutationFn: (body: EnvRule) =>
			api.put(`/projects/${encodeURIComponent(slug)}/environments/${encodeURIComponent(env.name)}`, body),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["environments", slug] }),
	});
	const del = useMutation({
		mutationFn: () =>
			api.del(`/projects/${encodeURIComponent(slug)}/environments/${encodeURIComponent(env.name)}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["environments", slug] }),
	});
	const [emails, setEmails] = useState((env.reviewer_emails ?? []).join(", "));
	const [patterns, setPatterns] = useState((env.branch_patterns ?? []).join(", "));
	const [wait, setWait] = useState(String(env.wait_minutes ?? 0));
	const [policy, setPolicy] = useState<EnvRule["branch_policy"]>(env.branch_policy || "all");
	useEffect(() => {
		setEmails((env.reviewer_emails ?? []).join(", "));
		setPatterns((env.branch_patterns ?? []).join(", "));
		setWait(String(env.wait_minutes ?? 0));
		setPolicy(env.branch_policy || "all");
	}, [env]);

	const rule = (patch: Partial<EnvRule>): EnvRule => {
		const next: EnvRule = {
			protected: env.protected,
			required_approvals: env.required_approvals,
			reviewer_emails: splitList(emails),
			wait_minutes: Math.max(0, Number(wait) || 0),
			branch_policy: policy,
			branch_patterns: splitList(patterns),
			...patch,
		};
		if (next.protected && next.required_approvals < 1 && next.wait_minutes < 1 && next.branch_policy !== "selected") {
			next.required_approvals = 1;
		}
		return next;
	};
	return (
		<Row className="flex-wrap">
			<span className="font-mono min-w-24 flex-1 truncate text-[13px]">{env.name}</span>
			<span className="text-xs text-[var(--faint)]">{env.protected ? "protected" : "open"}</span>
			<label className="flex items-center gap-1.5 text-xs text-[var(--dim)]">
				approvals
				<select
					className="h-7 rounded-lg border border-[var(--ctl)] bg-transparent px-1.5 text-xs"
					value={env.required_approvals}
					disabled={put.isPending}
					aria-label={`Approvals for ${env.name}`}
					onChange={(e) => put.mutate(rule({ required_approvals: Number(e.target.value) }))}
				>
					{[0, 1, 2, 3, 4, 5, 6].map((n) => (
						<option key={n} value={n}>
							{n}
						</option>
					))}
				</select>
			</label>
			<Button size="sm" disabled={put.isPending} onClick={() => put.mutate(rule({ protected: !env.protected }))}>
				{env.protected ? "Open" : "Protect"}
			</Button>
			<Button
				size="sm"
				variant="ghost"
				disabled={del.isPending}
				onClick={() => {
					if (confirm(`Delete environment ${env.name}? Deployment history for it goes with it.`)) {
						del.mutate();
					}
				}}
			>
				<Trash2 size={13} />
			</Button>
			{env.protected && (
				<div className="flex basis-full flex-wrap items-center gap-2 pt-1">
					<label className="flex items-center gap-1.5 text-xs text-[var(--dim)]">
						wait
						<Input
							type="number"
							min={0}
							max={43200}
							value={wait}
							aria-label={`Wait minutes for ${env.name}`}
							className="w-20"
							onChange={(e) => setWait(e.target.value)}
							onBlur={() => put.mutate(rule({}))}
						/>
						<span className="text-[var(--faint)]">min</span>
					</label>
					<label className="flex min-w-40 flex-1 items-center gap-1.5 text-xs text-[var(--dim)]">
						reviewers
						<Input
							value={emails}
							placeholder="any member"
							aria-label={`Reviewers for ${env.name}`}
							className="min-w-0 flex-1"
							onChange={(e) => setEmails(e.target.value)}
							onBlur={() => put.mutate(rule({}))}
						/>
					</label>
					<label className="flex items-center gap-1.5 text-xs text-[var(--dim)]">
						branches
						<select
							className="h-7 rounded-lg border border-[var(--ctl)] bg-transparent px-1.5 text-xs"
							value={policy}
							aria-label={`Branch policy for ${env.name}`}
							onChange={(e) => {
								const branch_policy = e.target.value as EnvRule["branch_policy"];
								setPolicy(branch_policy);
								put.mutate(rule({ branch_policy }));
							}}
						>
							<option value="all">all</option>
							<option value="selected">selected</option>
						</select>
					</label>
					{policy === "selected" && (
						<Input
							value={patterns}
							placeholder="main, release/*"
							aria-label={`Branch patterns for ${env.name}`}
							className="w-44"
							onChange={(e) => setPatterns(e.target.value)}
							onBlur={() => put.mutate(rule({}))}
						/>
					)}
				</div>
			)}
			{(put.error || del.error) && (
				<span className="basis-full text-xs text-[var(--err)]">{(put.error ?? del.error)?.message}</span>
			)}
		</Row>
	);
}

function reviewerCanAct(emails: string[] | undefined, email: string, role: string) {
	const list = emails ?? [];
	if (role === "owner" || role === "admin") return true;
	if (list.length === 0) return true;
	return list.includes(email);
}

function waitLeft(until: string | null) {
	if (!until) return "";
	const ms = new Date(until).getTime() - Date.now();
	if (ms <= 0) return "";
	return `wait ${Math.max(1, Math.round(ms / 60000))}m`;
}

function DeploymentRow({ d, slug }: { d: Deployment; slug: string }) {
	const { user } = useAuth();
	const qc = useQueryClient();
	const approvals = d.approvals ?? [];
	const waitingOnPeople = approvals.length < d.required_approvals;
	const timer = waitLeft(d.wait_until);
	const waiting = d.status === "queued" && (waitingOnPeople || timer !== "");
	const allowed = reviewerCanAct(d.reviewer_emails, user?.email ?? "", user?.role ?? "");
	const act = useMutation({
		mutationFn: (op: "approve" | "reject") => api.post(`/jobs/${d.job_id}/${op}`),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["deployments", slug] });
			qc.invalidateQueries({ queryKey: ["run", d.run_id] });
		},
	});
	return (
		<Row className="flex-wrap">
			<StatusSq status={d.status} />
			<span className="font-mono w-28 truncate text-[13px]">{d.environment_name}</span>
			<span className="min-w-24 flex-1 truncate text-xs text-[var(--dim)]">
				{d.job_name}
				<span className="text-[var(--faint)]">
					{" "}
					· {refName(d.ref)} {shortSha(d.sha)} · {relTime(d.created_at)}
				</span>
			</span>
			<Link to={`/runs/${d.run_id}`} className="lnk font-mono text-xs">
				#{d.run_number}
			</Link>
			<span className="font-mono text-xs text-[var(--faint)]">
				{approvals.length}/{d.required_approvals}
				{timer ? ` · ${timer}` : ""}
			</span>
			{waiting && allowed && (
				<>
					{waitingOnPeople && (
						<Button size="sm" variant="primary" disabled={act.isPending} onClick={() => act.mutate("approve")}>
							Approve
						</Button>
					)}
					<Button size="sm" variant="danger" disabled={act.isPending} onClick={() => act.mutate("reject")}>
						Reject
					</Button>
				</>
			)}
			{waiting && !allowed && <span className="text-xs text-[var(--faint)]">reviewers only</span>}
			{act.error && <span className="text-xs text-[var(--err)]">{act.error.message}</span>}
		</Row>
	);
}
