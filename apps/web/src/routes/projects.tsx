import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { Play, RefreshCw } from "lucide-react";
import { api } from "../lib/api";
import type { Project, Workflow } from "../lib/types";
import { Button, Card, EmptyState, ErrorNote, Input, KV, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { relTime } from "../lib/format";

export function ProjectsPage() {
	const nav = useNavigate();
	const qc = useQueryClient();
	const { data, isLoading, error, refetch } = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	const [creating, setCreating] = useState(false);
	const create = useMutation({
		mutationFn: (body: Record<string, string>) => api.post("/projects", body),
		onSuccess: () => {
			setCreating(false);
			qc.invalidateQueries({ queryKey: ["projects"] });
		},
	});
	// GitHub's app-manifest flow must leave the browser as a form POST — fetch
	// the manifest, then submit it to github.com so the app is created under
	// the user's own account.
	const connect = useMutation({
		mutationFn: () => api.get<{ url: string; manifest: Record<string, unknown> }>("/github-app/manifest"),
		onSuccess: (data) => {
			const form = document.createElement("form");
			form.method = "post";
			form.action = data.url;
			const input = document.createElement("input");
			input.type = "hidden";
			input.name = "manifest";
			input.value = JSON.stringify(data.manifest);
			form.appendChild(input);
			document.body.appendChild(form);
			form.submit();
		},
	});

	return (
		<>
			<PageHeader
				title="Projects"
				sub="Repositories wired into KERN"
				actions={
					<div className="flex gap-2">
						<Button size="sm" onClick={() => connect.mutate()} disabled={connect.isPending}>
							Connect GitHub
						</Button>
						<Button size="sm" variant="primary" onClick={() => setCreating((c) => !c)}>New project</Button>
					</div>
				}
			/>
			{creating && (
				<Card className="mb-4 p-4">
					<CreateProjectForm
						busy={create.isPending}
						onSubmit={(body) => create.mutate(body)}
						error={create.error?.message}
					/>
				</Card>
			)}
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{error ? (
						<ErrorNote error={error} onRetry={() => refetch()} />
					) : !data || data.projects.length === 0 ? (
						<EmptyState
							title="No projects"
							hint="Create a project with a local_path or repo_url, then sync its .github/workflows."
						/>
					) : (
						data.projects.map((p) => (
							<Row key={p.id} onClick={() => nav(`/projects/${p.slug}`)}>
								<span className="min-w-0 flex-1 truncate font-medium">{p.name}</span>
								<span className="text-xs text-[var(--dim)]">{p.slug}</span>
								<span className="font-mono w-64 truncate text-xs text-[var(--faint)]">
									{p.repo_full_name || p.local_path || p.repo_url}
								</span>
							</Row>
						))
					)}
				</Card>
			)}
		</>
	);
}

function CreateProjectForm({
	busy,
	onSubmit,
	error,
}: {
	busy: boolean;
	onSubmit: (body: Record<string, string>) => void;
	error?: string;
}) {
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const body: Record<string, string> = {};
		for (const [k, v] of fd.entries()) {
			const s = String(v).trim();
			if (s) body[k] = s;
		}
		onSubmit(body);
	};
	return (
		<form onSubmit={submit} className="grid grid-cols-2 gap-3">
			<Input name="slug" placeholder="slug (required)" required />
			<Input name="name" placeholder="display name" />
			<Input name="local_path" placeholder="local path (/srv/repo)" />
			<Input name="repo_url" placeholder="repo url (https://…)" />
			<Input name="repo_full_name" placeholder="owner/repo" />
			<Input name="default_branch" placeholder="default branch (main)" />
			{error && <p className="col-span-2 text-xs text-[var(--err)]">{error}</p>}
			<div className="col-span-2">
				<Button type="submit" variant="primary" size="sm" disabled={busy}>
					{busy ? "Creating…" : "Create"}
				</Button>
			</div>
		</form>
	);
}

export function ProjectDetailPage() {
	const { slug } = useParams<{ slug: string }>();
	const nav = useNavigate();
	const qc = useQueryClient();
	const { data, isLoading, error, refetch } = useQuery({
		queryKey: ["project", slug],
		queryFn: () => api.get<{ project: Project; workflows: Workflow[] }>(`/projects/${slug}`),
	});
	const { data: psettings } = useQuery({
		queryKey: ["project-settings", slug],
		queryFn: () => api.get<{ self_heal: string; fork_gate: string }>(`/projects/${slug}/settings`),
	});
	const setHeal = useMutation({
		mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { self_heal: v }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
	});
	const setGate = useMutation({
		mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { fork_gate: v }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
	});
	const sync = useMutation({
		mutationFn: () => api.post(`/projects/${slug}/sync`),
		onSuccess: () => {
			refetch();
			qc.invalidateQueries({ queryKey: ["projects"] });
		},
	});
	const trigger = useMutation({
		mutationFn: (wfID: string) =>
			api.post<{ run: { id: string } }>(`/projects/${slug}/workflows/${wfID}/runs`, {
				event: "workflow_dispatch",
			}),
		onSuccess: (res) => nav(`/runs/${res.run.id}`),
	});

	if (isLoading || !data) {
		return error ? <ErrorNote error={error} onRetry={() => refetch()} /> : <Spinner />;
	}
	const { project, workflows } = data;
	return (
		<>
			<PageHeader
				title={project.name}
				sub={project.repo_full_name || project.local_path || project.repo_url}
				actions={
					<>
						<Button size="sm" onClick={() => nav(`/environments?project=${slug}`)}>
							Environments
						</Button>
						<Button size="sm" onClick={() => sync.mutate()} disabled={sync.isPending}>
							<RefreshCw size={13} className={sync.isPending ? "animate-spin" : ""} /> Sync workflows
						</Button>
					</>
				}
			/>
			<Card className="mb-4 p-4">
				<div className="grid grid-cols-2 gap-x-8 md:grid-cols-3">
					<KV k="Slug" v={project.slug} mono />
					<KV k="Provider" v={project.provider} />
					<KV k="Default branch" v={project.default_branch} mono />
					<KV k="Repo" v={project.repo_url || "—"} mono />
					<KV k="Local path" v={project.local_path || "—"} mono />
					<KV k="Created" v={relTime(project.created_at)} />
					<KV
						k="Self-heal"
						v={
							<select
								className="rounded border border-[var(--line)] bg-transparent px-2 py-0.5 text-xs"
								value={psettings?.self_heal ?? "inherit"}
								disabled={setHeal.isPending}
								onChange={(e) => setHeal.mutate(e.target.value)}
							>
								<option value="inherit">inherit</option>
								<option value="off">off</option>
								<option value="auto">auto</option>
								<option value="approve">approve</option>
							</select>
						}
					/>
					<KV
						k="Fork gate"
						v={
							<select
								className="rounded border border-[var(--line)] bg-transparent px-2 py-0.5 text-xs"
								value={psettings?.fork_gate ?? "off"}
								disabled={setGate.isPending}
								onChange={(e) => setGate.mutate(e.target.value)}
							>
								<option value="off">off</option>
								<option value="all">all</option>
								<option value="first-time">first-time</option>
							</select>
						}
					/>
				</div>
			</Card>
			{sync.data != null && (
				<SyncResult result={sync.data as { sha: string; workflows: { path: string; valid: boolean; diagnostics: { level: string; message: string }[] | null }[] }} />
			)}
			{sync.error && <p className="mb-3 text-xs text-[var(--err)]">{sync.error.message}</p>}
			<h2 className="mb-2 text-[13px] font-semibold">Workflows</h2>
			<Card>
				{workflows.length === 0 ? (
					<EmptyState title="No workflows" hint="Run sync to discover .github/workflows in the project source." />
				) : (
					workflows.map((w) => (
						<Row key={w.id}>
							<span className="min-w-0 flex-1 truncate font-medium">{w.name}</span>
							<span className="font-mono flex-1 truncate text-xs text-[var(--faint)]">{w.path}</span>
							<Button
								size="sm"
								onClick={() => trigger.mutate(w.id)}
								disabled={trigger.isPending}
							>
								<Play size={12} /> Run
							</Button>
						</Row>
					))
				)}
			</Card>
		</>
	);
}

function SyncResult({
	result,
}: {
	result: { sha: string; workflows: { path: string; valid: boolean; diagnostics: { level: string; message: string }[] | null }[] };
}) {
	return (
		<Card className="mb-4 p-4">
			<div className="mb-2 text-xs text-[var(--faint)]">
				Synced at <span className="font-mono">{result.sha.slice(0, 8)}</span>
			</div>
			{result.workflows.map((w) => (
				<div key={w.path} className="text-xs">
					<span className={w.valid ? "text-[var(--ok)]" : "text-[var(--err)]"}>{w.valid ? "valid" : "invalid"}</span>
					<span className="font-mono ml-2 text-[var(--dim)]">{w.path}</span>
					{(w.diagnostics ?? []).map((d, i) => (
						<div key={i} className="ml-4 text-[var(--warn)]">
							{d.level}: {d.message}
						</div>
					))}
				</div>
			))}
		</Card>
	);
}
