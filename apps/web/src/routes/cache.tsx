import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { CacheEntry, Project } from "../lib/types";
import { Button, ErrorNote, PageHeader, Spinner } from "../components/ui";
import { bytes, relTime } from "../lib/format";

export function CachePage() {
	const qc = useQueryClient();
	const { data, isLoading, error, refetch } = useQuery({
		queryKey: ["cache"],
		queryFn: () => api.get<{ entries: CacheEntry[] }>("/cache"),
	});
	const { data: projects } = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	const remove = useMutation({
		mutationFn: (id: string) => api.del(`/cache/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["cache"] }),
	});

	if (isLoading) return <Spinner />;
	if (error) return <ErrorNote error={error} onRetry={() => refetch()} />;
	const entries = data?.entries ?? [];
	const totalBytes = entries.reduce((n, e) => n + e.size_bytes, 0);
	const largest = Math.max(...entries.map((e) => e.size_bytes), 1);
	const slugOf = new Map((projects?.projects ?? []).map((p) => [p.id, p.slug]));

	return (
		<>
			<PageHeader
				title="Cache"
				sub={`${entries.length} entr${entries.length === 1 ? "y" : "ies"} · ${bytes(totalBytes)} stored · entries unused for 7 days are evicted`}
			/>
			<div className="panel">
				<div className="phead">
					<span className="pt">Entries</span>
					<span className="r">newest first</span>
				</div>
				{entries.length === 0 ? (
					<div className="emptynote">
						No cache entries. Entries appear when a run uses actions/cache with a key and path.
					</div>
				) : (
					entries.map((e) => (
						<div key={e.id} className="flex items-center gap-3 border-b border-[var(--line)] px-4 py-2.5 last:border-0">
							<div className="min-w-0 flex-1">
								<div className="truncate font-mono text-xs text-[var(--ink)]">{e.key}</div>
								<div className="mt-0.5 truncate font-mono text-[11px] text-[var(--faint)]">
									{slugOf.get(e.project_id) ? (
										<Link to={`/projects/${slugOf.get(e.project_id)}`} className="lnk">
											{slugOf.get(e.project_id)}
										</Link>
									) : (
										e.project_id.slice(0, 8)
									)}
									<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
									{e.scope}
								</div>
							</div>
							<div className="hidden w-32 sm:block">
								<div className="meter">
									<i style={{ width: `${Math.max(2, (e.size_bytes / largest) * 100)}%` }} />
								</div>
							</div>
							<span className="w-20 shrink-0 text-right font-mono text-xs text-[var(--dim)]">
								{bytes(e.size_bytes)}
							</span>
							<span
								className="w-24 shrink-0 text-right text-[11px] text-[var(--faint)]"
								title={`Last used ${e.last_used_at}`}
							>
								{relTime(e.last_used_at)}
							</span>
							<Button
								size="sm"
								variant="ghost"
								title="Evict entry"
								disabled={remove.isPending}
								onClick={() => {
									if (confirm(`Evict cache ${e.key}?`)) remove.mutate(e.id);
								}}
							>
								<Trash2 size={13} />
							</Button>
						</div>
					))
				)}
			</div>
		</>
	);
}
