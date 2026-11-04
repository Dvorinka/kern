import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Pause, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { Runner } from "../lib/types";
import { Button, Card, EmptyState, Input, PageHeader, Spinner } from "../components/ui";
import { StatusSq } from "../components/status";
import { CopyButton } from "./settings";
import { Row } from "../components/layout";
import { bytes, relTime } from "../lib/format";

export function RunnersPage() {
	const qc = useQueryClient();
	const [enrolling, setEnrolling] = useState(false);
	const { data, isLoading } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
		refetchInterval: 5000,
	});
	const { data: settings } = useQuery({
		queryKey: ["settings"],
		queryFn: () => api.get<{ self_heal: string }>("/settings"),
	});
	const setHeal = useMutation({
		mutationFn: (v: string) => api.put("/settings", { self_heal: v }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
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
			<PageHeader
				title="Runners"
				sub="Registered execution agents"
				actions={
					<Button size="sm" variant="primary" onClick={() => setEnrolling((e) => !e)}>
						<Plus size={13} /> Enroll runner
					</Button>
				}
			/>
			{enrolling && <EnrollCard onDone={() => setEnrolling(false)} />}
			<Row>
				<span className="text-xs text-[var(--dim)]">Self-heal (org):</span>
				<select
					className="fsel"
					value={settings?.self_heal ?? "off"}
					disabled={setHeal.isPending}
					onChange={(e) => setHeal.mutate(e.target.value)}
				>
					<option value="off">off</option>
					<option value="auto">auto</option>
					<option value="approve">approve</option>
				</select>
				<span className="text-[10px] text-[var(--faint)]">
					allow-listed fixes for fixable step failures; audit-logged
				</span>
			</Row>
			{isLoading ? (
				<Spinner />
			) : (
				<div className="panel">
					<div className="phead">
						<span className="pt">Fleet</span>
						<span className="r">{data?.runners.length ?? 0} registered</span>
					</div>
					{!data || data.runners.length === 0 ? (
						<EmptyState
							title="No runners"
							hint="Start `just runner` (or the kern-runner binary) on any host with Docker to register one."
						/>
					) : (
						data.runners.map((r) => {
							const busy = (r.active_jobs ?? 0) > 0;
							const cpu = r.cpu_percent != null ? Math.round(r.cpu_percent) : null;
							const mem =
								r.mem_used_bytes != null && r.memory_bytes > 0
									? Math.round((r.mem_used_bytes / r.memory_bytes) * 100)
									: null;
							const disk =
								r.disk_free_bytes != null && r.disk_bytes > 0
									? Math.max(0, Math.round(100 - (r.disk_free_bytes / r.disk_bytes) * 100))
									: null;
							return (
								<div key={r.id} className="border-b border-[var(--line)] px-4 py-3 last:border-0">
									<div className="flex items-center gap-3">
										<StatusSq status={busy ? "in_progress" : r.status} />
										<span className="w-36 min-w-0 truncate font-mono text-xs text-[var(--ink)]">
											{r.name}
										</span>
										<span className="w-28 min-w-0 truncate text-[11px] text-[var(--faint)]">
											{r.hostname}
										</span>
										<span
											className="min-w-0 flex-1 truncate font-mono text-[11px] text-[var(--faint)]"
											title={(r.labels ?? []).join(", ")}
										>
											{(r.labels ?? []).join(", ")}
										</span>
										<span className="text-[11px] text-[var(--faint)]">
											{r.draining ? "draining" : busy ? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}` : "idle"}
										</span>
										<span className="w-20 shrink-0 text-right font-mono text-[11px] text-[var(--faint)]">
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
									</div>
									<div className="ml-[23px] mt-2 flex flex-wrap items-center gap-x-8 gap-y-1">
										<Meter label="cpu" pct={cpu} sub={busy ? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}` : ""} />
										<Meter label="mem" pct={mem} sub={r.memory_bytes > 0 ? bytes(r.memory_bytes) : ""} />
										<Meter
											label="disk"
											pct={disk}
											sub={r.disk_free_bytes != null ? `${bytes(r.disk_free_bytes)} free` : ""}
										/>
									</div>
								</div>
							);
						})
					)}
				</div>
			)}
		</>
	);
}

// EnrollCard mints a one-time enrollment token and prints the join command —
// the plaintext token is shown once, only its hash is stored.
function EnrollCard({ onDone }: { onDone: () => void }) {
	const [minted, setMinted] = useState<{ token: string; name: string } | null>(null);
	const mint = useMutation({
		mutationFn: (body: { name: string; ttl_minutes: number }) =>
			api.post<{ token: string }>("/enroll-tokens", body),
		onSuccess: (res, body) => setMinted({ token: res.token, name: body.name }),
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		mint.mutate({
			name: String(fd.get("name")).trim() || "runner",
			ttl_minutes: parseInt(String(fd.get("ttl")), 10) || 60,
		});
	};

	const joinCmd = minted
		? `kern-runner join --control ${window.location.origin} --name ${minted.name} --token ${minted.token}`
		: "";

	return (
		<Card className="mb-4 p-4">
			{minted ? (
				<div>
					<div className="mb-2 text-xs text-[var(--ok)]">
						Enroll token minted — run this on the runner host before it expires:
					</div>
					<div className="flex items-center gap-2 rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2">
						<code className="min-w-0 flex-1 truncate font-mono text-xs">{joinCmd}</code>
						<CopyButton text={joinCmd} />
						<Button size="sm" variant="ghost" onClick={onDone}>
							Done
						</Button>
					</div>
				</div>
			) : (
				<form onSubmit={submit} className="flex flex-wrap items-center gap-3">
					<Input name="name" placeholder="runner name" required className="w-48" />
					<Input name="ttl" type="number" min={1} max={1440} placeholder="ttl minutes (60)" className="w-40" />
					<Button type="submit" variant="primary" size="sm" disabled={mint.isPending}>
						Mint token
					</Button>
					{mint.error && <span className="text-xs text-[var(--err)]">{mint.error.message}</span>}
				</form>
			)}
		</Card>
	);
}

// Meter is a thin utilization readout: label, bar, secondary detail.
// null pct means the runner has not reported metrics yet. Grey until hot —
// colour is data, not decoration.
function Meter({ label, pct, sub }: { label: string; pct: number | null; sub: string }) {
	return (
		<span className="flex items-center gap-2 text-[10px] leading-4 text-[var(--faint)]">
			<span className="w-7">{label}</span>
			<span className="h-1.5 w-24 rounded-[3px] bg-[var(--raise2)]">
				{pct != null && (
					<span
						className="block h-1.5 rounded-[3px]"
						style={{
							width: `${Math.min(100, Math.max(2, pct))}%`,
							background: pct > 85 ? "var(--err)" : "#3a3a3a",
						}}
					/>
				)}
			</span>
			<span className="w-20 truncate font-mono">
				{pct != null ? `${Math.round(pct)}%` : "-"} {sub}
			</span>
		</span>
	);
}
