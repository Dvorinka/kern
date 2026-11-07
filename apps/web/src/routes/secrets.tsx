import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Pencil, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { Environment, Project, SecretSummary } from "../lib/types";
import { Button, Card, EmptyState, Input, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
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

	return (
		<>
			<PageHeader
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
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.secrets.length === 0 ? (
						<EmptyState
							title="No secrets"
							hint={
								slug
									? "Add one for this scope, or widen the scope filter to see inherited secrets."
									: "Org-level secrets here; pick a project or environment above to manage scoped ones."
							}
						/>
					) : (
						data.secrets.map((s) => {
							const scopeSlug = s.project_id ? slugByID.get(s.project_id) : undefined;
							return (
								<Row key={s.id}>
									<span className="font-mono min-w-0 flex-1 truncate text-[13px]">{s.name}</span>
									{!slug && (
										<span className="w-28 shrink-0 truncate text-right text-[11px] text-[var(--faint)]">
											{scopeSlug ? (
												<Link to={`/projects/${scopeSlug}`} className="lnk">
													{scopeSlug}
												</Link>
											) : (
												"org"
											)}
										</span>
									)}
									<span className="text-xs text-[var(--faint)]">
										updated {relTime(s.updated_at)}
									</span>
									<Button
										size="sm"
										variant="ghost"
										title="Rotate value"
										onClick={() => setRotating(s)}
									>
										<Pencil size={13} />
									</Button>
									<Button
										size="sm"
										variant="ghost"
										disabled={del.isPending}
										onClick={() => {
											if (confirm(`Delete secret ${s.name}?`))
												del.mutate({ name: s.name, qs: rowQs(s) });
										}}
									>
										<Trash2 size={13} />
									</Button>
								</Row>
							);
						})
					)}
				</Card>
			)}
		</>
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
