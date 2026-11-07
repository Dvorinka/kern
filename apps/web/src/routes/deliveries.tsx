import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Webhook } from "lucide-react";
import { api } from "../lib/api";
import type { WebhookDelivery } from "../lib/types";
import { EmptyState, PageHeader, Spinner, cn } from "../components/ui";
import { StatusSq } from "../components/status";
import { CopyButton } from "./settings";
import { relTime } from "../lib/format";

export function DeliveriesPage() {
	const [statusF, setStatusF] = useState("");
	const [eventF, setEventF] = useState("");
	const { data, isLoading } = useQuery({
		queryKey: ["deliveries"],
		queryFn: () => api.get<{ deliveries: WebhookDelivery[] }>("/webhooks/deliveries"),
		refetchInterval: 5000,
	});

	const deliveries = data?.deliveries ?? [];
	const stats = useMemo(() => {
		const byEvent = new Map<string, number>();
		let failed = 0;
		for (const d of deliveries) {
			byEvent.set(d.event, (byEvent.get(d.event) ?? 0) + 1);
			if (d.status === "failed") failed++;
		}
		return { failed, byEvent };
	}, [deliveries]);
	const events = [...stats.byEvent.entries()].sort((a, b) => b[1] - a[1]);
	const visible = deliveries.filter(
		(d) => (!statusF || d.status === statusF) && (!eventF || d.event === eventF),
	);

	return (
		<>
			<PageHeader mark="build" title="Webhooks" sub="Provider deliveries received by the control plane" />
			{isLoading ? (
				<Spinner />
			) : (
				<>
					{deliveries.length > 0 && (
						<div className="panel kpis mb-4">
							<div className="kpi">
								<div className="k">deliveries</div>
								<div className="v">{deliveries.length}</div>
								<div className="c">last received {relTime(deliveries[0]?.received_at)}</div>
							</div>
							<div className="kpi">
								<div className="k">failed</div>
								<div className="v">{stats.failed}</div>
								<div className="c">
									{deliveries.length
										? `${Math.round(((deliveries.length - stats.failed) / deliveries.length) * 100)}% processed`
										: "—"}
								</div>
							</div>
							<div className="kpi">
								<div className="k">top event</div>
								<div className="v" style={{ fontSize: 22 }}>{events[0]?.[0] ?? "—"}</div>
								<div className="c">{events[0] ? `${events[0][1]} of ${deliveries.length}` : ""}</div>
							</div>
						</div>
					)}
					<div className="panel">
						{deliveries.length === 0 ? (
							<EmptyState icon={<Webhook size={19} />}
								title="No deliveries"
								hint="Configure a GitHub webhook or App to point at /api/v1/webhooks/github."
							/>
						) : (
							<>
								<div className="flex flex-wrap items-center gap-2 border-b border-[var(--line)] px-4 py-2.5">
									{["", "processed", "failed", "received"].map((s) => {
										const n = s ? deliveries.filter((d) => d.status === s).length : deliveries.length;
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
								{visible.length === 0 && (
									<div className="emptynote flush">No deliveries match this filter.</div>
								)}
								{visible.map((d) => (
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
