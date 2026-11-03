import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pause, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { Runner } from "../lib/types";
import { Button, Card, EmptyState, PageHeader, Spinner } from "../components/ui";
import { StatusPill } from "../components/status";
import { Row } from "../components/layout";
import { bytes, relTime } from "../lib/format";

export function RunnersPage() {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
		refetchInterval: 5000,
	});
	const drain = useMutation({
		mutationFn: (id: string) => api.post(`/runners/${id}/drain`, { draining: true }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});
	const remove = useMutation({
		mutationFn: (id: string) => api.del(`/runners/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});

	return (
		<>
			<PageHeader title="Runners" sub="Registered execution agents" />
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.runners.length === 0 ? (
						<EmptyState
							title="No runners"
							hint="Start `just runner` (or the kern-runner binary) on any host with Docker to register one."
						/>
					) : (
						data.runners.map((r) => (
							<Row key={r.id}>
								<StatusPill status={r.status} className="w-20" />
								<span className="min-w-0 flex-1">
									<span className="font-medium">{r.name}</span>
									<span className="ml-2 text-xs text-[var(--text-faint)]">{r.hostname}</span>
								</span>
								<span className="w-36 truncate text-xs text-[var(--text-muted)]">
									{(r.labels ?? []).slice(0, 3).join(", ")}
									{(r.labels ?? []).length > 3 && ` +${r.labels.length - 3}`}
								</span>
								<span className="w-28 text-xs text-[var(--text-muted)]">
									{r.cpus} cpu · {bytes(r.memory_bytes)}
								</span>
								<span className="w-24 text-right text-xs text-[var(--text-faint)]">
									{relTime(r.last_heartbeat_at)}
								</span>
								<Button
									size="sm"
									variant="ghost"
									title="Drain (finish current work, take no new jobs)"
									disabled={r.draining || drain.isPending}
									onClick={() => drain.mutate(r.id)}
								>
									<Pause size={13} />
								</Button>
								<Button
									size="sm"
									variant="ghost"
									title="Delete runner"
									disabled={remove.isPending}
									onClick={() => {
										if (confirm(`Delete runner ${r.name}?`)) remove.mutate(r.id);
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
