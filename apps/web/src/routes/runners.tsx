import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Cpu, Image as ImageIcon, LocateFixed, Pause, Pencil, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { Runner } from "../lib/types";
import { Button, Card, EmptyState, Input, RunnerIcon, Spinner } from "../components/ui";
import { StatusSq } from "../components/status";
import { CopyButton } from "./settings";
import { Row } from "../components/layout";
import { GeoPicker } from "../components/fleetmap";
import { bytes, relTime } from "../lib/format";

export function RunnersPage() {
	const qc = useQueryClient();
	const [enrolling, setEnrolling] = useState(false);
	const [geoEdit, setGeoEdit] = useState<string | null>(null);
	const [iconEdit, setIconEdit] = useState<string | null>(null);
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
	const [staleDismissed, setStaleDismissed] = useState(false);

	// Offline runners whose last heartbeat is older than a week are almost
	// certainly decommissioned leftovers (replaced VMs, retired agents).
	// Surface them for removal instead of letting rows accumulate forever.
	const stale = (data?.runners ?? []).filter(
		(r) =>
			r.status === "offline" &&
			r.last_heartbeat_at != null &&
			Date.now() - new Date(r.last_heartbeat_at).getTime() > 7 * 24 * 3600 * 1000,
	);

	return (
		<div className="page">
			<div className="mb-6 flex flex-wrap items-end gap-4">
				<div>
					<h1 className="big x">
						{data?.runners.length
							? `${data.runners.filter((r) => r.status === "online").length} of ${data.runners.length} runners are online.`
							: "No runners yet."}
					</h1>
					<p className="lead">
						Each square is a job slot — filled with the pattern of the job running in
						it. Empty squares are free.
					</p>
				</div>
				<div className="ml-auto">
					<button className="pill solid" onClick={() => setEnrolling((e) => !e)}>
						<Plus size={13} /> Enroll runner
					</button>
				</div>
			</div>
			{enrolling && <EnrollCard onDone={() => setEnrolling(false)} />}
			{stale.length > 0 && !staleDismissed && (
				<Card className="mb-4 flex flex-wrap items-center gap-2 p-3">
					<span className="text-xs text-[var(--dim)]">
						{stale.length} runner{stale.length === 1 ? "" : "s"} offline for over 7 days —
						probably safe to remove:
					</span>
					{stale.map((r) => (
						<button
							key={r.id}
							className="pill"
							title={`last seen ${relTime(r.last_heartbeat_at)}`}
							onClick={() => {
								if (confirm(`Delete runner ${r.name}?`)) remove.mutate(r.id);
							}}
						>
							<Trash2 size={11} /> {r.name}
						</button>
					))}
					<Button size="sm" variant="ghost" className="ml-auto" onClick={() => setStaleDismissed(true)}>
						Dismiss
					</Button>
				</Card>
			)}
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
				<div className="fl">
					{!data || data.runners.length === 0 ? (
						<EmptyState icon={<Cpu size={19} />}
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
								<div key={r.id} className="frow">
									<div className="fn">
										<b>
											{r.icon && (
												<span className="mr-2 inline-flex align-middle">
													<RunnerIcon icon={r.icon} className="rricon rrname" />
												</span>
											)}
											{r.name}
										</b>
										<small>
											{r.cpus} cores · {r.arch}
											{r.hostname ? ` · ${r.hostname}` : ""}
											{r.geo_city ? ` · ${r.geo_city}` : ""}
										</small>
									</div>
									<div className={`slots ${r.status === "offline" ? "off" : ""}`}>
										{Array.from({ length: Math.min(8, Math.max(4, r.cpus)) }, (_, i) => (
											<i key={i} className={busy && i < (r.active_jobs ?? 0) ? "j jr" : busy ? "j" : ""} />
										))}
									</div>
									<div className="ld">
										<div className="meter">
											<i style={{ width: `${Math.max(cpu ?? 0, busy ? 4 : 0)}%` }} />
										</div>
										<b>{cpu != null ? `${cpu}%` : "—"}</b>
									</div>
									<span className="stt">
										<StatusSq status={busy ? "in_progress" : r.status} />
										{r.draining
											? "draining"
											: busy
												? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}`
												: r.status}
										<span className="font-mono text-[11px] text-[var(--faint)]">
											{relTime(r.last_heartbeat_at)}
										</span>
									</span>
									<div className="fx">
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
										<span className="acts2">
											<button
												className="pill"
												title="Runner icon — an emoji or an image URL/path"
												onClick={() => setIconEdit((g) => (g === r.id ? null : r.id))}
											>
												<ImageIcon size={12} />
											</button>
											<button
												className="pill"
												title={
													r.geo_source === "manual"
														? `manual location — ${r.geo_lat?.toFixed(2)}, ${r.geo_lng?.toFixed(2)}`
														: r.geo_source === "auto"
															? `auto location via ${r.geo_ip}`
															: "no location — edit or wait for a heartbeat"
												}
												onClick={() => setGeoEdit((g) => (g === r.id ? null : r.id))}
											>
												<Pencil size={12} />
											</button>
											<button
												className="pill"
												title="Drain (finish current work, take no new jobs)"
												disabled={r.draining || drain.isPending}
												onClick={() => drain.mutate(r.id)}
											>
												<Pause size={12} />
											</button>
											<button
												className="pill"
												title="Delete runner"
												disabled={remove.isPending}
												onClick={() => {
													if (confirm(`Delete runner ${r.name}?`)) remove.mutate(r.id);
												}}
											>
												<Trash2 size={12} />
											</button>
										</span>
									</div>
									{geoEdit === r.id && <GeoEditor runner={r} onDone={() => setGeoEdit(null)} />}
									{iconEdit === r.id && (
										<div className="fgeo">
											<IconEditor runner={r} onDone={() => setIconEdit(null)} />
										</div>
									)}
								</div>
							);
						})
					)}
				</div>
			)}
		</div>
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

// GeoEditor places a runner on the fleet map — click the map for coordinates
// (city/country reverse-geocoded via Nominatim, still editable), type them by
// hand, or hand location back to egress-IP auto-detection.
function GeoEditor({ runner, onDone }: { runner: Runner; onDone: () => void }) {
	const qc = useQueryClient();
	const [lat, setLat] = useState(runner.geo_lat?.toString() ?? "");
	const [lng, setLng] = useState(runner.geo_lng?.toString() ?? "");
	const [city, setCity] = useState(runner.geo_city ?? "");
	const [country, setCountry] = useState(runner.geo_country ?? "");
	const revTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

	const geoLat = parseFloat(lat);
	const geoLng = parseFloat(lng);

	const onPick = (la: number, ln: number) => {
		setLat(la.toString());
		setLng(ln.toString());
		if (revTimer.current) clearTimeout(revTimer.current);
		revTimer.current = setTimeout(() => {
			void (async () => {
				try {
					const r = await fetch(
						`https://nominatim.openstreetmap.org/reverse?format=jsonv2&zoom=10&lat=${la}&lon=${ln}`,
						{ headers: { Accept: "application/json" } },
					);
					if (!r.ok) return;
					const j = (await r.json()) as { address?: Record<string, string> };
					const a = j.address ?? {};
					setCity(a.city ?? a.town ?? a.village ?? a.municipality ?? a.county ?? a.state ?? "");
					setCountry(a.country ?? "");
				} catch {
					// offline or rate-limited — coordinates still save
				}
			})();
		}, 400);
	};

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
		if (Number.isNaN(geoLat) || Number.isNaN(geoLng)) return;
		save.mutate({ lat: geoLat, lng: geoLng, city: city.trim(), country: country.trim() });
	};

	return (
		<div className="fgeo geopanel">
			<GeoPicker
				lat={Number.isNaN(geoLat) ? null : geoLat}
				lng={Number.isNaN(geoLng) ? null : geoLng}
				onPick={onPick}
			/>
			<form onSubmit={submit} className="mt-2 flex flex-wrap items-center gap-2">
				<Input
					name="lat"
					placeholder="lat"
					value={lat}
					onChange={(e) => setLat(e.target.value)}
					className="w-24"
					required
				/>
				<Input
					name="lng"
					placeholder="lng"
					value={lng}
					onChange={(e) => setLng(e.target.value)}
					className="w-24"
					required
				/>
				<Input
					name="city"
					placeholder="city"
					value={city}
					onChange={(e) => setCity(e.target.value)}
					className="w-32"
				/>
				<Input
					name="country"
					placeholder="country"
					value={country}
					onChange={(e) => setCountry(e.target.value)}
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
						: "click the map or type coordinates — city/country fill in automatically"}
				</span>
			</form>
		</div>
	);
}

// IconEditor sets the runner's glyph: an emoji, a short label, or an image
// URL/path. Empty restores the default marker.
function IconEditor({ runner, onDone }: { runner: Runner; onDone: () => void }) {
	const qc = useQueryClient();
	const save = useMutation({
		mutationFn: (icon: string) => api.patch(`/runners/${runner.id}`, { icon }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["runners"] });
			onDone();
		},
	});
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		save.mutate(String(new FormData(e.currentTarget).get("icon")).trim());
	};
	return (
		<form onSubmit={submit} className="ml-[23px] mt-2 flex flex-wrap items-center gap-2">
			<span className="text-[11px] text-[var(--faint)]">icon</span>
			<RunnerIcon icon={runner.icon} className="rricon rrname" />
			<Input
				name="icon"
				placeholder="emoji like 🚀 — or an image URL/path"
				defaultValue={runner.icon}
				className="w-64"
			/>
			<Button type="submit" variant="primary" size="sm" disabled={save.isPending}>
				Save
			</Button>
			<Button
				type="button"
				size="sm"
				variant="ghost"
				disabled={save.isPending || !runner.icon}
				onClick={() => save.mutate("")}
			>
				Clear
			</Button>
			<Button type="button" size="sm" variant="ghost" onClick={onDone}>
				Cancel
			</Button>
			{save.error && <span className="text-xs text-[var(--err)]">{save.error.message}</span>}
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
