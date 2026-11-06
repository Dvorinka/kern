import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import type { Notification } from "../lib/types";
import { relTime } from "../lib/format";

const KIND_COLOR: Record<Notification["kind"], string> = {
	run_approval: "var(--warn)",
	remediation: "var(--info)",
	run_failed: "var(--err)",
	runner_offline: "var(--faint)",
	webhook_failed: "var(--err)",
};

// Attention feed, fully derived server-side — a row vanishes the moment its
// cause is resolved. The bell badge counts only actionable kinds.
export function NotificationsBell() {
	const nav = useNavigate();
	const qc = useQueryClient();
	const [open, setOpen] = useState(false);
	const ref = useRef<HTMLDivElement>(null);

	const { data } = useQuery({
		queryKey: ["notifications"],
		queryFn: () => api.get<{ notifications: Notification[] }>("/notifications"),
		refetchInterval: 15000,
	});
	const approve = useMutation({
		mutationFn: (n: Notification) => api.post(`/jobs/${n.job_id}/remediate`, { step: n.step }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["notifications"] });
			qc.invalidateQueries({ queryKey: ["run"] });
		},
	});
	const dismiss = useMutation({
		mutationFn: (n: Notification) =>
			api.post(`/jobs/${n.job_id}/remediate-dismiss`, { step: n.step }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["notifications"] }),
	});

	useEffect(() => {
		if (!open) return;
		const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open]);

	const items = data?.notifications ?? [];
	const actionable = items.filter(
		(n) => n.kind === "run_approval" || n.kind === "remediation",
	).length;

	return (
		<>
			<button
				className="nbell"
				onClick={() => setOpen((o) => !o)}
				aria-label="Notifications"
				title="Notifications"
			>
				<Bell size={13} strokeWidth={1.8} />
				{actionable > 0 && <span className="dot">{actionable}</span>}
			</button>
			{open && (
				<div className="ndrop" onClick={() => setOpen(false)} ref={ref}>
					<div className="panel" onClick={(e) => e.stopPropagation()}>
						<div className="phead">
							<span className="pt">Notifications</span>
							<span className="r">{items.length ? `${items.length} open` : "clear"}</span>
						</div>
						{items.length === 0 && (
							<div className="nrow">
								<span className="ns">Nothing needs attention.</span>
							</div>
						)}
						{items.map((n) => (
							<div className="nrow" key={n.id}>
								<i className="ni" style={{ background: KIND_COLOR[n.kind] }} />
								<div className="min-w-0">
									<div className="nt">
										<a
											className="lnk"
											onClick={() => {
												setOpen(false);
												nav(n.href);
											}}
										>
											{n.title}
										</a>
									</div>
									{n.fix && n.fix.length > 0 && (
										<div className="mt-1 rounded border border-[var(--line)] bg-[var(--bg)] px-2 py-1 font-mono text-[10px] text-[var(--dim)]">
											{n.fix.map((f, i) => (
												<div key={i} className="truncate" title={f}>
													{f}
												</div>
											))}
										</div>
									)}
									<div className="ns">
										{n.sub}
										{n.sub ? " · " : ""}
										{relTime(n.created_at)}
									</div>
								</div>
								<div className="na">
									{n.kind === "remediation" && (
										<>
											<button
												className="lnk"
												disabled={approve.isPending}
												onClick={() => approve.mutate(n)}
											>
												Approve fix
											</button>
											<button
												className="lnk"
												disabled={dismiss.isPending}
												onClick={() => dismiss.mutate(n)}
											>
												Dismiss
											</button>
										</>
									)}
									{n.kind === "run_approval" && (
										<button
											className="lnk"
											onClick={() => {
												setOpen(false);
												nav(n.href);
											}}
										>
											Review
										</button>
									)}
								</div>
							</div>
						))}
					</div>
				</div>
			)}
		</>
	);
}
