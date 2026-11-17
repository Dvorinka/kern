import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ChevronDown, ChevronRight, Webhook } from "lucide-react";
import { api } from "../lib/api";
import type { WebhookDelivery } from "../lib/types";
import { EmptyState, PageHeader, Spinner, cn } from "../components/ui";
import { StatusSq } from "../components/status";
import { CopyButton } from "./settings";
import { catColor, relTime } from "../lib/format";

export function DeliveriesPage() {
	const [statusF, setStatusF] = useState("");
	const [eventF, setEventF] = useState("");
	// filters run server-side; total/counts/events count the whole table
	// so the KPIs and pills stay true beyond the fetched window.
	const qs = new URLSearchParams();
	if (statusF) qs.set("status", statusF);
	if (eventF) qs.set("event", eventF);
	const { data, isLoading } = useQuery({
		queryKey: ["deliveries", statusF, eventF],
		queryFn: () =>
			api.get<{
				deliveries: WebhookDelivery[];
				total: number;
				counts: Record<string, number>;
				events: Record<string, number>;
			}>(`/webhooks/deliveries?${qs}`),
		refetchInterval: 5000,
	});

	const deliveries = data?.deliveries ?? [];
	const total = data?.total ?? 0;
	const counts = data?.counts ?? {};
	const events = Object.entries(data?.events ?? {}).sort((a, b) => b[1] - a[1]);

	return (
		<>
			<PageHeader mark="build" title="Webhooks" sub="Provider deliveries received by the control plane" />
			{isLoading ? (
				<Spinner />
			) : (
				<>
					{total > 0 && (
						<div className="panel kpis mb-4">
							<div className="kpi">
								<div className="k">deliveries</div>
								<div className="v">{total}</div>
								<div className="c">last received {relTime(deliveries[0]?.received_at)}</div>
							</div>
							<div className="kpi">
								<div className="k">failed</div>
								<div className="v">{counts.failed ?? 0}</div>
								<div className="c">
									{total ? `${Math.round(((total - (counts.failed ?? 0)) / total) * 100)}% processed` : "—"}
								</div>
							</div>
							<div className="kpi">
								<div className="k">top event</div>
								<div
									className="v"
									style={{ fontSize: 22, color: events[0] ? catColor(events[0][0]) : undefined }}
								>
									{events[0]?.[0] ?? "—"}
								</div>
								<div className="c">{events[0] ? `${events[0][1]} of ${total}` : ""}</div>
							</div>
						</div>
					)}
					<div className="panel">
						{total === 0 ? (
							<EmptyState icon={<Webhook size={19} />}
								title="No deliveries"
								hint="Configure a GitHub webhook or App to point at /api/v1/webhooks/github."
							/>
						) : (
							<>
								<div className="flex flex-wrap items-center gap-2 border-b border-[var(--line)] px-4 py-2.5">
									{["", "processed", "failed", "received"].map((s) => {
										const n = s ? (counts[s] ?? 0) : total;
										return (
											<button
												key={s || "all"}
												className={cn("jpill", statusF === s && "on")}
												onClick={() => setStatusF(s)}
											>
												{s || "all"} · {n}
											</button>
										);
									})}
									<select
										className="fsel ml-auto"
										value={eventF}
										onChange={(e) => setEventF(e.target.value)}
									>
										<option value="">All events</option>
										{events.map(([e, n]) => (
											<option key={e} value={e}>
												{e} ({n})
											</option>
										))}
									</select>
								</div>
								{deliveries.length === 0 && (
									<div className="emptynote flush">No deliveries match this filter.</div>
								)}
								{deliveries.map((d) => (
									<DeliveryRow key={d.id} d={d} />
								))}
							</>
						)}
					</div>
				</>
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
		<div
			className={cn("border-b border-[var(--line)] last:border-0", open && "bg-[var(--raise2)]/50")}
		>
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
				<span
					className="w-28 shrink-0 text-[13px] font-medium"
					style={{ color: catColor(d.event) }}
				>
					{d.event}
				</span>
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
				<div className="border-t border-[var(--line)]">
					<div className="flex items-center gap-3 px-4 py-1.5 text-[11px] text-[var(--faint)]">
						<span>
							{d.processed_at ? `processed ${relTime(d.processed_at)}` : "not yet processed"}
							{d.provider ? ` · ${d.provider}` : ""}
						</span>
						{detail?.delivery.payload && (
							<span className="ml-auto flex items-center gap-1">
								Copy JSON
								<CopyButton text={JSON.stringify(detail.delivery.payload, null, 2)} />
							</span>
						)}
					</div>
					<pre className="max-h-96 overflow-auto px-4 pb-3 font-mono text-[11px] leading-relaxed text-[var(--dim)]">
						{detail?.delivery.payload
							? JSON.stringify(detail.delivery.payload, null, 2)
							: "Loading payload…"}
					</pre>
				</div>
			)}
		</div>
	);
}
