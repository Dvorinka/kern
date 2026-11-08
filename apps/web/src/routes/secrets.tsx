import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Code2, Copy, KeyRound, Pencil, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { Environment, Project, ProjectVar, SecretSummary } from "../lib/types";
import { Button, Card, EmptyState, Input, PageHeader, Spinner } from "../components/ui";
import { relTime } from "../lib/format";

// Secrets are scoped org → project → environment. The API filters by
// ?project=<slug>&environment=<name>; an unscoped read returns org and
// project secrets (environment-scoped rows need both params).
export function SecretsPage() {
	const qc = useQueryClient();
	const [params, setParams] = useSearchParams();
	const project = params.get("project") ?? "";
	const environment = params.get("environment") ?? "";

	const { data: projects } = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	const projectList = projects?.projects ?? [];
	const slug = projectList.some((p) => p.slug === project) ? project : "";

	const envs = useQuery({
		queryKey: ["environments", slug],
		enabled: slug !== "",
		queryFn: () =>
			api.get<{ environments: Environment[] }>(
				`/projects/${encodeURIComponent(slug)}/environments`,
			),
	});
	const envNames = (envs.data?.environments ?? []).map((e) => e.name);
	const env = envNames.includes(environment) ? environment : "";

	const scopeQs = (() => {
		const p = new URLSearchParams();
		if (slug) p.set("project", slug);
		if (env) p.set("environment", env);
		const s = p.toString();
		return s ? `?${s}` : "";
	})();

	const { data, isLoading } = useQuery({
		queryKey: ["secrets", scopeQs],
		queryFn: () => api.get<{ secrets: SecretSummary[] }>(`/secrets${scopeQs}`),
	});

	const [adding, setAdding] = useState(false);
	const [rotating, setRotating] = useState<SecretSummary | null>(null);
	const [q, setQ] = useState("");

	const put = useMutation({
		mutationFn: ({ name, value, qs }: { name: string; value: string; qs: string }) =>
			api.put(`/secrets/${encodeURIComponent(name)}${qs}`, { value }),
		onSuccess: () => {
			setAdding(false);
			setRotating(null);
			qc.invalidateQueries({ queryKey: ["secrets"] });
		},
	});
	const del = useMutation({
		mutationFn: ({ name, qs }: { name: string; qs: string }) =>
			api.del(`/secrets/${encodeURIComponent(name)}${qs}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["secrets"] }),
	});

	// A row shown in the unscoped list may still be project-scoped — writes
	// must carry that scope or the upsert lands at org level / the delete
	// hits the wrong same-named row.
	const slugByID = new Map(projectList.map((p) => [p.id, p.slug]));
	const rowQs = (s: SecretSummary) => {
		// Environment-scoped rows only appear under the matching env scope;
		// the summary carries the env id, not its name, so keep the page scope.
		if (s.environment_id) return scopeQs;
		const slugFor = s.project_id ? slugByID.get(s.project_id) : undefined;
		if (!slugFor) return scopeQs;
		const p = new URLSearchParams({ project: slugFor });
		return `?${p.toString()}`;
	};

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		put.mutate({ name: String(fd.get("name")), value: String(fd.get("value")), qs: scopeQs });
	};

	const scopeLabel = slug ? (env ? `${slug} / ${env}` : slug) : "organization + projects";

	// group by scope: organization first, then each project alphabetically
	const STALE = 90 * 86_400_000;
	const groups = useMemo(() => {
		const out = new Map<string, { label: string; slug?: string; items: SecretSummary[] }>();
		const needle = q.trim().toLowerCase();
		for (const s of data?.secrets ?? []) {
			if (needle && !s.name.toLowerCase().includes(needle)) continue;
			const scopeSlug = s.project_id ? slugByID.get(s.project_id) : undefined;
			const envLabel = s.environment_id ? env : undefined;
			const key = envLabel ? `${scopeSlug ?? slug}/${envLabel}` : scopeSlug ?? "_org";
			if (!out.has(key)) {
				out.set(key, {
					label: envLabel ? `${scopeSlug ?? slug} / ${envLabel}` : scopeSlug ?? "organization",
					slug: scopeSlug,
					items: [],
				});
			}
			out.get(key)!.items.push(s);
		}
		return [...out.values()].sort((a, b) =>
			a.label === "organization" ? -1 : b.label === "organization" ? 1 : a.label.localeCompare(b.label),
		);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [data, q]);

	const allSecrets = data?.secrets ?? [];
	const stale = allSecrets.filter((s) => Date.now() - Date.parse(s.updated_at) > STALE).length;
	const fresh = allSecrets.filter((s) => Date.now() - Date.parse(s.updated_at) < 7 * 86_400_000).length;

	return (
		<>
			<PageHeader mark="ok"
				title="Secrets"
				sub={`Encrypted at rest; delivered only to trusted runs and masked in logs — showing ${scopeLabel}`}
				actions={
					<div className="flex items-center gap-2">
						<select
							className="fsel"
							value={slug}
							onChange={(e) => setParams(e.target.value ? { project: e.target.value } : {})}
							aria-label="Project scope"
						>
							<option value="">All scopes</option>
							{projectList.map((p) => (
								<option key={p.id} value={p.slug}>
									{p.slug}
								</option>
							))}
						</select>
						{slug && (
							<select
								className="fsel"
								value={env}
								onChange={(e) =>
									setParams(
										e.target.value
											? { project: slug, environment: e.target.value }
											: { project: slug },
									)
								}
								aria-label="Environment scope"
							>
								<option value="">project-wide</option>
								{envNames.map((n) => (
									<option key={n} value={n}>
										{n}
									</option>
								))}
							</select>
						)}
						<Button size="sm" variant="primary" onClick={() => setAdding((a) => !a)}>
							Add secret
						</Button>
					</div>
				}
			/>
			{allSecrets.length > 0 && (
				<div className="panel kpis mb-4">
					<div className="kpi">
						<div className="k">secrets</div>
						<div className="v">{allSecrets.length}</div>
						<div className="c">in {scopeLabel}</div>
					</div>
					<div className="kpi">
						<div className="k">scopes</div>
						<div className="v">{groups.length}</div>
						<div className="c">org + projects</div>
					</div>
					<div className="kpi">
						<div className="k">stale</div>
						<div className="v" style={stale ? { color: "var(--err)" } : undefined}>{stale}</div>
						<div className="c">older than 90d — rotate</div>
					</div>
					<div className="kpi">
						<div className="k">rotated 7d</div>
						<div className="v">{fresh}</div>
						<div className="c">fresh values</div>
					</div>
				</div>
			)}
			{adding && (
				<Card className="mb-4 p-4">
					<form onSubmit={submit} className="flex gap-2">
						<Input
							name="name"
							placeholder="NAME (no spaces)"
							required
							pattern="[^\s=]+"
							className="w-56"
						/>
						<Input name="value" type="password" placeholder="value" required className="flex-1" />
						<Button type="submit" variant="primary" size="sm" disabled={put.isPending}>
							Save{slug ? ` to ${scopeLabel}` : ""}
						</Button>
					</form>
					{put.error && <p className="mt-2 text-xs text-[var(--err)]">{put.error.message}</p>}
				</Card>
			)}
			{rotating && (
				<Card className="mb-4 p-4">
					<RotateForm
						name={rotating.name}
						busy={put.isPending}
						error={put.error?.message}
						onSubmit={(value) => put.mutate({ name: rotating.name, value, qs: rowQs(rotating) })}
						onCancel={() => setRotating(null)}
					/>
				</Card>
			)}
			{!isLoading && allSecrets.length > 0 && (
				<input
					className="fsel mb-3 w-64"
					placeholder="Filter by name…"
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
			)}
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.secrets.length === 0 ? (
						<EmptyState icon={<KeyRound size={19} />}
							title="No secrets"
							hint={
								slug
									? "Add one for this scope, or widen the scope filter to see inherited secrets."
									: "Org-level secrets here; pick a project or environment above to manage scoped ones."
							}
						/>
					) : (
						groups.map((g) => (
							<div key={g.label}>
								{groups.length > 1 && (
									<div className="vgh">
										{g.slug ? (
											<Link to={`/projects/${g.slug}`} className="lnk">
												{g.label}
											</Link>
										) : (
											g.label
										)}
										<span>{g.items.length} secret{g.items.length === 1 ? "" : "s"}</span>
									</div>
								)}
								{g.items.map((s) => {
									const isStale = Date.now() - Date.parse(s.updated_at) > STALE;
									return (
									<div className="vrow" key={s.id}>
										<i className={isStale ? "vkey stale" : "vkey"} />
										<span className="font-mono min-w-0 truncate text-[13px]">{s.name}</span>
										{isStale && <span className="vstale">stale</span>}
										<span className="vdots" aria-hidden="true">••••••••••</span>
										<span className="vacts">
											<button
												className="ract"
												title={`Copy name ${s.name}`}
												onClick={() => navigator.clipboard.writeText(s.name)}
											>
												<Copy size={12} />
											</button>
											<button
												className="ract"
												title={`Copy workflow snippet  secrets.${s.name}`}
												onClick={() =>
													navigator.clipboard.writeText(`\${{ secrets.${s.name} }}`)
												}
											>
												<Code2 size={12} />
											</button>
											<button
												className="ract"
												title="Rotate value"
												onClick={() => setRotating(s)}
											>
												<Pencil size={12} />
											</button>
											<button
												className="ract err"
												title="Delete secret"
												disabled={del.isPending}
												onClick={() => {
													if (confirm(`Delete secret ${s.name}?`))
														del.mutate({ name: s.name, qs: rowQs(s) });
												}}
											>
												<Trash2 size={12} />
											</button>
										</span>
										<span className="vwhen">updated {relTime(s.updated_at)}</span>
									</div>
									);
								})}
							</div>
						))
					)}
				</Card>
			)}
			{slug && <VarsCard slug={slug} />}
		</>
	);
}

// VarsCard manages a project's configuration variables — the GitHub `vars`
// expression context. Unlike secrets, values are plaintext configuration:
// visible here, delivered to every run including untrusted pull_request runs,
// and never masked in logs.
function VarsCard({ slug }: { slug: string }) {
	const qc = useQueryClient();
	const qs = `?project=${encodeURIComponent(slug)}`;
	const { data, isLoading } = useQuery({
		queryKey: ["vars", slug],
		queryFn: () => api.get<{ vars: ProjectVar[] }>(`/vars${qs}`),
	});
	const [adding, setAdding] = useState(false);
	const [q, setQ] = useState("");

	const put = useMutation({
		mutationFn: ({ name, value }: { name: string; value: string }) =>
			api.put(`/vars/${encodeURIComponent(name)}${qs}`, { value }),
		onSuccess: () => {
			setAdding(false);
			qc.invalidateQueries({ queryKey: ["vars", slug] });
		},
	});
	const del = useMutation({
		mutationFn: (name: string) => api.del(`/vars/${encodeURIComponent(name)}${qs}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["vars", slug] }),
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		put.mutate({ name: String(fd.get("name")), value: String(fd.get("value")) });
	};

	const vars = (data?.vars ?? []).filter(
		(v) => !q.trim() || v.name.toLowerCase().includes(q.trim().toLowerCase()),
	);

	return (
		<div className="mt-6">
			<PageHeader mark="ok"
				title="Variables"
				sub={`Project configuration for the vars context — plaintext, delivered to all runs — ${slug}`}
				actions={
					<Button size="sm" variant="primary" onClick={() => setAdding((a) => !a)}>
						Add variable
					</Button>
				}
			/>
			{adding && (
				<Card className="mb-4 p-4">
					<form onSubmit={submit} className="flex gap-2">
						<Input
							name="name"
							placeholder="NAME (no spaces)"
							required
							pattern="[^\s=]+"
							className="w-56"
						/>
						<Input name="value" placeholder="value" required className="flex-1" />
						<Button type="submit" variant="primary" size="sm" disabled={put.isPending}>
							Save
						</Button>
					</form>
					{put.error && <p className="mt-2 text-xs text-[var(--err)]">{put.error.message}</p>}
				</Card>
			)}
			{!isLoading && (data?.vars.length ?? 0) > 0 && (
				<input
					className="fsel mb-3 w-64"
					placeholder="Filter by name…"
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
			)}
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{vars.length === 0 ? (
						<EmptyState icon={<Code2 size={19} />}
							title="No variables"
							hint={`Add one, or use kern vars set NAME VALUE --project ${slug}.`}
						/>
					) : (
						vars.map((v) => (
							<div className="vrow" key={v.id}>
								<i className="vkey" />
								<span className="font-mono min-w-0 truncate text-[13px]">{v.name}</span>
								<span className="vdots font-mono">{v.value}</span>
								<span className="vacts">
									<button
										className="ract"
										title={`Copy name ${v.name}`}
										onClick={() => navigator.clipboard.writeText(v.name)}
									>
										<Copy size={12} />
									</button>
									<button
										className="ract"
										title={`Copy workflow snippet  vars.${v.name}`}
										onClick={() =>
											navigator.clipboard.writeText(`\${{ vars.${v.name} }}`)
										}
									>
										<Code2 size={12} />
									</button>
									<button
										className="ract"
										title="Edit variable"
										onClick={() => {
											const value = prompt(`New value for ${v.name}`, v.value);
											if (value !== null) put.mutate({ name: v.name, value });
										}}
									>
										<Pencil size={12} />
									</button>
									<button
										className="ract err"
										title="Delete variable"
										disabled={del.isPending}
										onClick={() => {
											if (confirm(`Delete variable ${v.name}?`)) del.mutate(v.name);
										}}
									>
										<Trash2 size={12} />
									</button>
								</span>
								<span className="vwhen">updated {relTime(v.updated_at)}</span>
							</div>
						))
					)}
				</Card>
			)}
		</div>
	);
}

function RotateForm({
	name,
	busy,
	error,
	onSubmit,
	onCancel,
}: {
	name: string;
	busy: boolean;
	error?: string;
	onSubmit: (value: string) => void;
	onCancel: () => void;
}) {
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		onSubmit(String(fd.get("value")));
	};
	return (
		<form onSubmit={submit} className="flex items-center gap-2">
			<span className="font-mono text-xs text-[var(--dim)]">{name}</span>
			<Input
				name="value"
				type="password"
				placeholder="new value"
				required
				className="flex-1"
				autoFocus
			/>
			<Button type="submit" variant="primary" size="sm" disabled={busy}>
				Rotate
			</Button>
			<Button type="button" size="sm" variant="ghost" onClick={onCancel}>
				Cancel
			</Button>
			{error && <span className="text-xs text-[var(--err)]">{error}</span>}
		</form>
	);
}
