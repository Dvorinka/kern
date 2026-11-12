import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { CacheEntry, Project } from "../lib/types";
import { Button, PageHeader, Spinner } from "../components/ui";
import { bytes, projectColor, relTime } from "../lib/format";

export function CachePage() {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["cache"],
		// stats cover the whole store — the entry list is a bounded window
		queryFn: () =>
			api.get<{
				entries: CacheEntry[];
				stats: { count: number; bytes: number; largest: number; projects: number };
			}>("/cache"),
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
	const entries = data?.entries ?? [];
	const stats = data?.stats;
	const totalBytes = stats?.bytes ?? entries.reduce((n, e) => n + e.size_bytes, 0);
	const entryCount = stats?.count ?? entries.length;
	const largest = Math.max(...entries.map((e) => e.size_bytes), 1);
	const slugOf = new Map((projects?.projects ?? []).map((p) => [p.id, p.slug]));
	// entries unused for 7 days are evicted — count down honestly
	const EVICT = 7 * 86_400_000;
	const evictIn = (e: CacheEntry) =>
		Math.max(0, EVICT - (Date.now() - Date.parse(e.last_used_at)));

	return (
		<>
			<PageHeader mark="lint"
				title="Cache"
				sub={`${entryCount} entr${entryCount === 1 ? "y" : "ies"} · ${bytes(totalBytes)} stored · entries unused for 7 days are evicted`}
			/>
			{entries.length > 0 && (
				<div className="panel kpis mb-4">
					<div className="kpi">
						<div className="k">entries</div>
						<div className="v">{entryCount}</div>
						<div className="c">
							{stats?.projects ?? new Set(entries.map((e) => e.project_id)).size} project
							{(stats?.projects ?? 0) === 1 ? "" : "s"}
						</div>
					</div>
					<div className="kpi">
						<div className="k">stored</div>
						<div className="v">{bytes(totalBytes)}</div>
						<div className="c">across all keys</div>
					</div>
					<div className="kpi">
						<div className="k">largest</div>
						<div className="v">{bytes(stats?.largest ?? largest)}</div>
						<div className="c">single entry</div>
					</div>
					<div className="kpi">
						<div className="k">next eviction</div>
						<div className="v" style={{ fontSize: 24 }}>
							{evictIn(entries.reduce((a, b) => (Date.parse(a.last_used_at) < Date.parse(b.last_used_at) ? a : b))) < 86_400_000
								? "<1d"
								: `${Math.ceil(evictIn(entries.reduce((a, b) => (Date.parse(a.last_used_at) < Date.parse(b.last_used_at) ? a : b))) / 86_400_000)}d`}
						</div>
						<div className="c">oldest unused entry</div>
					</div>
				</div>
			)}
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
										<Link
											to={`/projects/${slugOf.get(e.project_id)}`}
											className="lnk"
											style={{ color: projectColor(slugOf.get(e.project_id)!) }}
										>
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
								title={`Last used ${e.last_used_at} — evicts after 7 unused days`}
							>
								{relTime(e.last_used_at)}
								<small className="block" style={{ color: evictIn(e) < 86_400_000 ? "var(--err)" : undefined }}>
									{evictIn(e) < 86_400_000 ? "evicts <1d" : `evicts ${Math.floor(evictIn(e) / 86_400_000)}d`}
								</small>
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
