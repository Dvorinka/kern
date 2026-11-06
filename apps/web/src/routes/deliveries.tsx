import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api } from "../lib/api";
import type { WebhookDelivery } from "../lib/types";
import { Card, EmptyState, ErrorNote, PageHeader, Spinner, cn } from "../components/ui";
import { StatusSq } from "../components/status";
import { relTime } from "../lib/format";

export function DeliveriesPage() {
	const { data, isLoading, error, refetch } = useQuery({
		queryKey: ["deliveries"],
		queryFn: () => api.get<{ deliveries: WebhookDelivery[] }>("/webhooks/deliveries"),
		refetchInterval: 5000,
	});

	return (
		<>
			<PageHeader title="Webhooks" sub="Provider deliveries received by the control plane" />
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{error ? (
						<ErrorNote error={error} onRetry={() => refetch()} />
					) : !data || data.deliveries.length === 0 ? (
						<EmptyState
							title="No deliveries"
							hint="Configure a GitHub webhook or App to point at /api/v1/webhooks/github."
						/>
					) : (
						data.deliveries.map((d) => <DeliveryRow key={d.id} d={d} />)
					)}
				</Card>
			)}
		</>
	);
}

function DeliveryRow({ d }: { d: WebhookDelivery }) {
	const [open, setOpen] = useState(false);
	const { data: detail } = useQuery({
		queryKey: ["delivery", d.id],
		queryFn: () => api.get<{ delivery: WebhookDelivery }>(`/webhooks/deliveries/${d.id}`),
		enabled: open,
	});
	return (
		<div className={cn("border-b border-[var(--line)] last:border-0", open && "bg-[var(--raise2)]/50")}>
			<button
				onClick={() => setOpen((o) => !o)}
				className="flex w-full cursor-pointer items-center gap-3 px-4 py-2 text-left hover:bg-[var(--raise2)]"
			>
				{open ? (
					<ChevronDown size={13} className="shrink-0 text-[var(--faint)]" />
				) : (
					<ChevronRight size={13} className="shrink-0 text-[var(--faint)]" />
				)}
				<span className="flex w-24 shrink-0 items-center gap-2 font-mono text-xs text-[var(--dim)]">
					<StatusSq status={d.status} />
					{d.status}
				</span>
				<span className="w-28 shrink-0 text-[13px] font-medium">{d.event}</span>
				<span className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--faint)]">
					{d.delivery_id}
				</span>
				{d.error && (
					<span
						className="max-w-64 truncate text-xs"
						style={{ color: d.status === "failed" ? "var(--err)" : "var(--faint)" }}
					>
						{d.error}
					</span>
				)}
				<span className="w-20 shrink-0 text-right text-xs text-[var(--faint)]">
					{relTime(d.received_at)}
				</span>
			</button>
			{open && (
				<pre className="max-h-96 overflow-auto border-t border-[var(--line)] px-4 py-3 font-mono text-[11px] leading-relaxed text-[var(--dim)]">
					{detail?.delivery.payload
						? JSON.stringify(detail.delivery.payload, null, 2)
						: "Loading payload…"}
				</pre>
			)}
		</div>
	);
}
