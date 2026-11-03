import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { CacheEntry } from "../lib/types";
import { Button, Card, EmptyState, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { bytes, relTime } from "../lib/format";

export function CachePage() {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["cache"],
		queryFn: () => api.get<{ entries: CacheEntry[] }>("/cache"),
	});
	const remove = useMutation({
		mutationFn: (id: string) => api.del(`/cache/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["cache"] }),
	});

	return (
		<>
			<PageHeader
				title="Cache"
				sub="Dependency archives saved by actions/cache; entries unused for 7 days are evicted"
			/>
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.entries.length === 0 ? (
						<EmptyState
							title="No cache entries"
							hint="Entries appear when a run uses actions/cache with a key and path."
						/>
					) : (
						data.entries.map((e) => (
							<Row key={e.id}>
								<span className="min-w-0 flex-1 truncate font-mono text-[13px]">{e.key}</span>
								<span className="w-24 shrink-0 text-xs text-[var(--text-muted)]">{e.scope}</span>
								<span className="w-16 shrink-0 text-right text-xs text-[var(--text-faint)]">
									{bytes(e.size_bytes)}
								</span>
								<span
									className="w-20 shrink-0 text-right text-xs text-[var(--text-faint)]"
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
							</Row>
						))
					)}
				</Card>
			)}
		</>
	);
}
