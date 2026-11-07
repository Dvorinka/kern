import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { LocateFixed, Pause, Pencil, Plus, Trash2 } from "lucide-react";
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
	const [geoEdit, setGeoEdit] = useState<string | null>(null);
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
							action={
								<Button size="sm" variant="primary" onClick={() => setEnrolling(true)}>
									<Plus size={12} /> Enroll runner
								</Button>
							}
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
										<span className="w-36 min-w-0 shrink-0 truncate text-[11px] text-[var(--faint)]">
											{r.geo_city || r.geo_country
												? `${r.geo_city}${r.geo_city && r.geo_country ? ", " : ""}${r.geo_country}`
												: "—"}
										</span>
										<span className="text-[11px] text-[var(--faint)]">
											{r.draining
												? "draining"
												: busy
													? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}`
													: "idle"}
										</span>
										<span className="w-20 shrink-0 text-right font-mono text-[11px] text-[var(--faint)]">
											{relTime(r.last_heartbeat_at)}
										</span>
										<Button
											size="sm"
											variant="ghost"
											title={
												r.geo_source === "manual"
													? `manual location — ${r.geo_lat?.toFixed(2)}, ${r.geo_lng?.toFixed(2)}`
													: r.geo_source === "auto"
														? `auto location via ${r.geo_ip}`
														: "no location — edit or wait for a heartbeat"
											}
											onClick={() => setGeoEdit((g) => (g === r.id ? null : r.id))}
										>
											<Pencil size={13} />
										</Button>
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
										<Meter
											label="cpu"
											pct={cpu}
											sub={busy ? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}` : ""}
										/>
										<Meter
											label="mem"
											pct={mem}
											sub={r.memory_bytes > 0 ? bytes(r.memory_bytes) : ""}
										/>
										<Meter
											label="disk"
											pct={disk}
											sub={r.disk_free_bytes != null ? `${bytes(r.disk_free_bytes)} free` : ""}
										/>
									</div>
									{geoEdit === r.id && <GeoEditor runner={r} onDone={() => setGeoEdit(null)} />}
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
					<Input
						name="ttl"
						type="number"
						min={1}
						max={1440}
						placeholder="ttl minutes (60)"
						className="w-40"
					/>
					<Button type="submit" variant="primary" size="sm" disabled={mint.isPending}>
						Mint token
					</Button>
					{mint.error && <span className="text-xs text-[var(--err)]">{mint.error.message}</span>}
				</form>
			)}
		</Card>
	);
}

// GeoEditor places a runner on the fleet map by hand, or hands it back to
// egress-IP auto-detection.
function GeoEditor({ runner, onDone }: { runner: Runner; onDone: () => void }) {
	const qc = useQueryClient();
	const save = useMutation({
		mutationFn: (body: Record<string, unknown>) => api.patch(`/runners/${runner.id}/geo`, body),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["runners"] });
			qc.invalidateQueries({ queryKey: ["fleetmap"] });
			onDone();
		},
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const lat = parseFloat(String(fd.get("lat")));
		const lng = parseFloat(String(fd.get("lng")));
		if (Number.isNaN(lat) || Number.isNaN(lng)) return;
		save.mutate({
			lat,
			lng,
			city: String(fd.get("city")).trim(),
			country: String(fd.get("country")).trim(),
		});
	};

	return (
		<form onSubmit={submit} className="ml-[23px] mt-2 flex flex-wrap items-center gap-2">
			<Input
				name="lat"
				placeholder="lat"
				defaultValue={runner.geo_lat ?? ""}
				className="w-24"
				required
			/>
			<Input
				name="lng"
				placeholder="lng"
				defaultValue={runner.geo_lng ?? ""}
				className="w-24"
				required
			/>
			<Input name="city" placeholder="city" defaultValue={runner.geo_city} className="w-32" />
			<Input
				name="country"
				placeholder="country"
				defaultValue={runner.geo_country}
				className="w-28"
			/>
			<Button type="submit" variant="primary" size="sm" disabled={save.isPending}>
				Save
			</Button>
			<Button
				type="button"
				size="sm"
				title="re-resolve from the runner's egress IP"
				disabled={save.isPending}
				onClick={() => save.mutate({ auto: true })}
			>
				<LocateFixed size={13} /> Auto
			</Button>
			<Button type="button" size="sm" variant="ghost" onClick={onDone}>
				Cancel
			</Button>
			{save.error && <span className="text-xs text-[var(--err)]">{save.error.message}</span>}
			<span className="text-[10px] text-[var(--faint)]">
				{runner.geo_source === "auto" && runner.geo_ip
					? `auto via ${runner.geo_ip}`
					: "manual wins over auto"}
			</span>
		</form>
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
