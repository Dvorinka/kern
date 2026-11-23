import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState, type FormEvent } from "react";
import { AlertTriangle, Cpu, Image as ImageIcon, LocateFixed, Pause, Pencil, Plus, RefreshCw, Terminal, Trash2, Zap } from "lucide-react";
import { api } from "../lib/api";
import type { Runner } from "../lib/types";
import { Button, Card, EmptyState, Input, RunnerIcon, Spinner, cn } from "../components/ui";
import { StatusSq } from "../components/status";
import { CopyButton } from "./settings";
import { GeoPicker } from "../components/fleetmap";
import { bytes, relTime } from "../lib/format";

type FleetFilter = "all" | "online" | "busy" | "issues";

export function RunnersPage() {
	const qc = useQueryClient();
	const [enrolling, setEnrolling] = useState(false);
	const [geoEdit, setGeoEdit] = useState<string | null>(null);
	const [iconEdit, setIconEdit] = useState<string | null>(null);
	const [envEdit, setEnvEdit] = useState<string | null>(null);
	const [filter, setFilter] = useState<FleetFilter>("all");
	const [search, setSearch] = useState("");
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
	const setUpdate = useMutation({
		mutationFn: (v: { id: string; on: boolean }) =>
			api.post(`/runners/${v.id}/auto-update`, { enabled: v.on }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});
	const setWake = useMutation({
		mutationFn: (v: { id: string; on: boolean }) =>
			api.patch(`/runners/${v.id}`, { wake_on_demand: v.on }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});
	const drain = useMutation({
		mutationFn: (v: { id: string; draining: boolean }) =>
			api.post(`/runners/${v.id}/drain`, { draining: v.draining }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});
	const remove = useMutation({
		mutationFn: (id: string) => api.del(`/runners/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runners"] }),
	});
	const [staleDismissed, setStaleDismissed] = useState(false);
	const [confirmDel, setConfirmDel] = useState<string | null>(null);
	// Bulk selection — drain/undrain/delete the checked runners with the
	// same per-runner mutations, fanned out in parallel.
	const [sel, setSel] = useState<Set<string>>(new Set());
	const toggleSel = (id: string) =>
		setSel((s) => {
			const n = new Set(s);
			if (n.has(id)) n.delete(id); else n.add(id);
			return n;
		});
	const bulk = useMutation({
		mutationFn: async (v: { ids: string[]; op: "drain" | "undrain" | "delete" }) => {
			await Promise.all(
				v.ids.map((id) =>
					v.op === "delete"
						? api.del(`/runners/${id}`)
						: api.post(`/runners/${id}/drain`, { draining: v.op === "drain" }),
				),
			);
		},
		onSuccess: () => {
			setSel(new Set());
			qc.invalidateQueries({ queryKey: ["runners"] });
		},
	});

	const allRunners = useMemo(() => data?.runners ?? [], [data]);
	const fleet = useMemo(() => {
		let online = 0;
		let draining = 0;
		let activeJobs = 0;
		let totalSlots = 0;
		let paused = 0;
		let drift = 0;
		const versions = new Set<string>();
		for (const r of allRunners) {
			if (r.status === "online" || r.status === "draining") {
				online++;
				totalSlots += Math.max(1, r.concurrency || 1);
				if (r.version) versions.add(r.version);
			}
			if (r.draining || r.status === "draining") draining++;
			activeJobs += r.active_jobs ?? 0;
			if (r.claims_paused || r.disk_alerted) paused++;
			if ((r.missing_tools?.length ?? 0) > 0) drift++;
		}
		const versionSkew = versions.size > 1;
		return { online, draining, activeJobs, totalSlots, paused, drift, versionSkew };
	}, [allRunners]);

	const hasIssue = (r: Runner) =>
		r.status === "offline" ||
		r.draining ||
		Boolean(r.claims_paused) ||
		Boolean(r.disk_alerted) ||
		(r.missing_tools?.length ?? 0) > 0;

	const visible = useMemo(() => {
		const needle = search.trim().toLowerCase();
		return allRunners.filter((r) => {
			if (filter === "online" && r.status !== "online") return false;
			if (filter === "busy" && (r.active_jobs ?? 0) === 0) return false;
			if (filter === "issues" && !hasIssue(r)) return false;
			if (needle) {
				const hay = [r.name, r.hostname, r.geo_city, r.arch, r.executor, ...(r.labels ?? [])]
					.filter(Boolean)
					.join(" ")
					.toLowerCase();
				if (!hay.includes(needle)) return false;
			}
			return true;
		});
	}, [allRunners, filter, search]);

	// Offline runners whose last heartbeat is older than a week are almost
	// certainly decommissioned leftovers (replaced VMs, retired agents).
	const stale = allRunners.filter(
		(r) =>
			r.status === "offline" &&
			r.last_heartbeat_at != null &&
			Date.now() - new Date(r.last_heartbeat_at).getTime() > 7 * 24 * 3600 * 1000,
	);

	const utilPct = fleet.totalSlots > 0 ? Math.round((fleet.activeJobs / fleet.totalSlots) * 100) : 0;

	return (
		<div className="page">
			<div className="mb-5 flex flex-wrap items-end gap-4">
				<div>
					<h1 className="big x">
						{allRunners.length
							? `${fleet.online} of ${allRunners.length} runners are online.`
							: "No runners yet."}
					</h1>
					<p className="lead">
						Each square is a concurrent job slot — filled while executing, open when ready to claim.
					</p>
				</div>
				<div className="ml-auto">
					<button className="pill solid" onClick={() => setEnrolling((e) => !e)}>
						<Plus size={13} /> Enroll runner
					</button>
				</div>
			</div>
			{enrolling && <EnrollCard onDone={() => setEnrolling(false)} />}

			{allRunners.length > 0 && (
				<div className="panel kpis mb-4">
					<div className="kpi">
						<div className="k">online</div>
						<div className="v">
							{fleet.online}
							<small>/{allRunners.length}</small>
						</div>
						<div className="c">
							{fleet.draining > 0 ? `${fleet.draining} draining` : "fleet ready"}
						</div>
					</div>
					<div className="kpi">
						<div className="k">active slots</div>
						<div className="v">
							{fleet.activeJobs}
							<small>/{fleet.totalSlots}</small>
						</div>
						<div className="c">{utilPct}% slot utilization</div>
					</div>
					<div className="kpi">
						<div className="k">health gates</div>
						<div className="v" style={fleet.paused > 0 ? { color: "var(--err)" } : undefined}>
							{fleet.paused}
						</div>
						<div className="c">
							{fleet.paused > 0 ? "claims paused or low disk" : "no gates tripped"}
						</div>
					</div>
					<div className="kpi">
						<div className="k">tool &amp; version drift</div>
						<div
							className="v"
							style={fleet.drift > 0 || fleet.versionSkew ? { color: "var(--warn)" } : undefined}
						>
							{fleet.drift > 0 ? `${fleet.drift} host${fleet.drift === 1 ? "" : "s"}` : fleet.versionSkew ? "skew" : "aligned"}
						</div>
						<div className="c">
							{fleet.drift > 0
								? "missing declared tools"
								: fleet.versionSkew
									? "mixed runner versions"
									: "all online hosts match"}
						</div>
					</div>
				</div>
			)}

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
								if (confirmDel === r.id) {
									remove.mutate(r.id);
									setConfirmDel(null);
								} else setConfirmDel(r.id);
							}}
						>
							<Trash2 size={11} /> {confirmDel === r.id ? "confirm?" : r.name}
						</button>
					))}
					<Button size="sm" variant="ghost" className="ml-auto" onClick={() => setStaleDismissed(true)}>
						Dismiss
					</Button>
				</Card>
			)}

			<div className="mb-3 flex flex-wrap items-center gap-2">
				{(
					[
						["all", `All · ${allRunners.length}`],
						["online", `Online · ${allRunners.filter((r) => r.status === "online").length}`],
						["busy", `Busy · ${allRunners.filter((r) => (r.active_jobs ?? 0) > 0).length}`],
						["issues", `Issues · ${allRunners.filter(hasIssue).length}`],
					] as [FleetFilter, string][]
				).map(([key, label]) => (
					<button
						key={key}
						className={cn("jpill", filter === key && "on")}
						onClick={() => setFilter(key)}
					>
						{label}
					</button>
				))}
				<input
					className="fsel w-52"
					placeholder="Filter name, label, host…"
					value={search}
					onChange={(e) => setSearch(e.target.value)}
				/>
				<div className="ml-auto flex items-center gap-2">
					<span className="whitespace-nowrap text-xs text-[var(--dim)]">Self-heal:</span>
					<select
						className="fsel"
						aria-label="Org self-heal mode"
						value={settings?.self_heal ?? "off"}
						disabled={setHeal.isPending}
						onChange={(e) => setHeal.mutate(e.target.value)}
					>
						<option value="off">off</option>
						<option value="auto">auto</option>
						<option value="approve">approve</option>
					</select>
				</div>
			</div>

			{isLoading ? (
				<Spinner />
			) : (
				<div className="fl">
					{allRunners.length === 0 ? (
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
						<>
						{sel.size > 0 && (
							<div className="panel flex flex-wrap items-center gap-2 px-4 py-2.5">
								<span className="text-xs font-semibold text-[var(--ink)]">{sel.size} selected</span>
								<span className="ml-auto flex gap-1.5">
									<Button size="sm" variant="ghost" disabled={bulk.isPending}
										onClick={() => bulk.mutate({ ids: [...sel], op: "drain" })}>
										Drain
									</Button>
									<Button size="sm" variant="ghost" disabled={bulk.isPending}
										onClick={() => bulk.mutate({ ids: [...sel], op: "undrain" })}>
										Undrain
									</Button>
									<Button size="sm" variant="danger" disabled={bulk.isPending}
										onClick={() => bulk.mutate({ ids: [...sel], op: "delete" })}>
										Delete
									</Button>
									<Button size="sm" variant="ghost" onClick={() => setSel(new Set())}>
										Clear
									</Button>
								</span>
							</div>
						)}
						{visible.length === 0 && (
							<div className="panel">
								<div className="emptynote">No runners match this filter.</div>
							</div>
						)}
						{visible.map((r) => {
							const busy = (r.active_jobs ?? 0) > 0;
							const slots = Math.max(1, r.concurrency || 1, r.active_jobs ?? 0);
							const cpu = r.cpu_percent != null ? Math.round(r.cpu_percent) : null;
							const mem =
								r.mem_used_bytes != null && r.memory_bytes > 0
									? Math.round((r.mem_used_bytes / r.memory_bytes) * 100)
									: null;
							const disk =
								r.disk_free_bytes != null && r.disk_bytes > 0
									? Math.max(0, Math.round(100 - (r.disk_free_bytes / r.disk_bytes) * 100))
									: null;
							const statusClass =
								r.status === "offline"
									? "err"
									: r.draining || r.claims_paused || r.disk_alerted
										? "warn"
										: busy
											? "live"
											: "ok";
							const statusText = r.draining
								? "draining"
								: r.claims_paused
									? `paused · ${r.claims_paused}`
									: busy
										? `${r.active_jobs} active`
										: r.status;
							const envCount = Object.keys(r.env ?? {}).length;
							const labels = r.labels ?? [];
							const missing = r.missing_tools ?? [];
							return (
								<div key={r.id} className="frow">
									<div className="rc-top">
										<input
											type="checkbox"
											aria-label={`Select ${r.name}`}
											checked={sel.has(r.id)}
											onChange={() => toggleSel(r.id)}
										/>
										<div className="rc-id">
											{r.icon && <RunnerIcon icon={r.icon} className="rricon rrname" />}
											<div className="min-w-0">
												<div className="flex flex-wrap items-center gap-2">
													<b className="truncate">{r.name}</b>
													{r.executor && (
														<span className="rpill">{r.executor}</span>
													)}
													<span className={`rpill ${statusClass}`}>
														<StatusSq status={busy ? "in_progress" : r.status} />
														{statusText}
													</span>
												</div>
												<small>
													{r.cpus} cores · {r.arch}
													{r.hostname ? ` · ${r.hostname}` : ""}
													{r.geo_city ? ` · ${r.geo_city}` : ""}
													{` · ${r.version || "dev"}`}
													{(r.egress_24h_bytes ?? 0) > 0 &&
														` · 24h egress ${bytes(r.egress_24h_bytes)}`}
													{` · seen ${relTime(r.last_heartbeat_at)}`}
												</small>
											</div>
										</div>
										<div className="rc-acts">
											<button
												className={cn("rc-btn", iconEdit === r.id && "on")}
												title="Runner icon"
												onClick={() => setIconEdit((g) => (g === r.id ? null : r.id))}
											>
												<ImageIcon size={13} />
											</button>
											<button
												className={cn("rc-btn", geoEdit === r.id && "on")}
												title={
													r.geo_source === "manual"
														? `Manual location (${r.geo_lat?.toFixed(2)}, ${r.geo_lng?.toFixed(2)})`
														: r.geo_source === "auto"
															? `Auto location via ${r.geo_ip}`
															: "Set location"
												}
												onClick={() => setGeoEdit((g) => (g === r.id ? null : r.id))}
											>
												<Pencil size={13} />
											</button>
											<button
												className={cn("rc-btn", r.auto_update && "on")}
												title={r.auto_update
													? "Self-update on — accepts signed runner image offers"
													: "Self-update off — stays on current image"}
												disabled={setUpdate.isPending}
												onClick={() => setUpdate.mutate({ id: r.id, on: !r.auto_update })}
											>
												<RefreshCw size={13} />
											</button>
											<button
												className={cn("rc-btn", (envEdit === r.id || envCount > 0) && "on")}
												title={
													envCount > 0
														? `Env overrides (${envCount}): ${Object.keys(r.env).join(", ")}`
														: "Env overrides for every job on this runner"
												}
												onClick={() => setEnvEdit((g) => (g === r.id ? null : r.id))}
											>
												<Terminal size={13} />
											</button>
											<button
												className={cn("rc-btn", r.wake_on_demand && "on")}
												title={r.wake_on_demand
													? "Wake on demand on — scaler may power this host for queued work"
													: "Wake on demand off"}
												disabled={setWake.isPending}
												onClick={() => setWake.mutate({ id: r.id, on: !r.wake_on_demand })}
											>
												<Zap size={13} />
											</button>
											<button
												className={cn("rc-btn", r.draining && "on")}
												title={r.draining ? "Undrain (resume taking new jobs)" : "Drain (finish current work, take no new jobs)"}
												disabled={drain.isPending}
												onClick={() => drain.mutate({ id: r.id, draining: !r.draining })}
											>
												<Pause size={13} />
											</button>
											<button
												className={cn("rc-btn danger", confirmDel === r.id && "confirm")}
												title="Delete runner"
												disabled={remove.isPending}
												onClick={() => {
													if (confirmDel === r.id) {
														remove.mutate(r.id);
														setConfirmDel(null);
													} else setConfirmDel(r.id);
												}}
											>
												{confirmDel === r.id ? "confirm?" : <Trash2 size={13} />}
											</button>
										</div>
									</div>

									<div className="rc-mid">
										<div className="flex items-center gap-2.5">
											<div className={`slots ${r.status === "offline" ? "off" : ""}`}>
												{Array.from({ length: Math.min(12, slots) }, (_, i) => (
													<i key={i} className={i < (r.active_jobs ?? 0) ? "j jr" : ""} />
												))}
											</div>
											<span className="font-mono text-[11px] text-[var(--dim)]">
												{r.active_jobs ?? 0}/{r.concurrency || 1} slots
											</span>
										</div>
										<div className="rc-gauges">
											<Meter label="cpu" pct={cpu} sub={cpu != null ? "" : "idle"} />
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
									</div>

									{(labels.length > 0 || envCount > 0 || missing.length > 0 || r.disk_alerted) && (
										<div className="rc-foot">
											{labels.map((l) => (
												<span key={l} className="rpill">
													{l}
												</span>
											))}
											{envCount > 0 && (
												<span className="rpill ok">
													{envCount} env override{envCount === 1 ? "" : "s"}
												</span>
											)}
											{r.disk_alerted && (
												<span className="rpill err">
													<AlertTriangle size={11} /> low disk alert
												</span>
											)}
											{missing.length > 0 && (
												<span className="rpill warn">
													<AlertTriangle size={11} /> missing: {missing.join(", ")}
												</span>
											)}
										</div>
									)}

									{geoEdit === r.id && <GeoEditor runner={r} onDone={() => setGeoEdit(null)} />}
									{envEdit === r.id && <EnvEditor runner={r} onDone={() => setEnvEdit(null)} />}
									{iconEdit === r.id && (
										<div className="fgeo">
											<IconEditor runner={r} onDone={() => setIconEdit(null)} />
										</div>
									)}
								</div>
							);
						})}
						</>
					)}
				</div>
			)}
		</div>
	);
}

// EnrollCard mints a one-time enrollment token and prints the join command —
// the plaintext token is shown once, only its hash is stored.
function EnrollCard({ onDone }: { onDone: () => void }) {
	const [minted, setMinted] = useState<{
		token: string;
		name: string;
		autoUpdate: boolean;
	} | null>(null);
	const [autoUpdate, setAutoUpdate] = useState(true);
	const [envDraft, setEnvDraft] = useState("");
	const mint = useMutation({
		mutationFn: (body: {
			name: string;
			ttl_minutes: number;
			config?: { auto_update?: boolean; env?: Record<string, string> };
		}) => api.post<{ token: string }>("/enroll-tokens", body),
		onSuccess: (res, body) =>
			setMinted({ token: res.token, name: body.name, autoUpdate: !!body.config?.auto_update }),
	});

	const parseEnv = (raw: string): Record<string, string> => {
		const env: Record<string, string> = {};
		for (const line of raw.split("\n")) {
			const t = line.trim();
			if (!t || t.startsWith("#")) continue;
			const eq = t.indexOf("=");
			if (eq > 0) env[t.slice(0, eq).trim()] = t.slice(eq + 1);
		}
		return env;
	};

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const env = parseEnv(envDraft);
		mint.mutate({
			name: String(fd.get("name")).trim() || "runner",
			ttl_minutes: parseInt(String(fd.get("ttl")), 10) || 60,
			config: {
				auto_update: autoUpdate,
				...(Object.keys(env).length > 0 ? { env } : {}),
			},
		});
	};

	const joinCmd = minted
		? `kern-runner join --control ${window.location.origin} --name ${minted.name} --token ${minted.token}`
		: "";
	const envFlags = envDraft
		.trim()
		.split("\n")
		.filter((l) => l.trim() && !l.trim().startsWith("#"))
		.map((l) => {
			const eq = l.indexOf("=");
			return eq > 0 ? `-e ${l.slice(0, eq).trim()}=${l.slice(eq + 1)}` : "";
		})
		.filter(Boolean)
		.join(" ");
	const dockerCmd = minted
		? `docker run -d --name kern-runner --restart always -e KERN_CONTROL_URL=${window.location.origin} -e KERN_RUNNER_JOIN_TOKEN=${minted.token} ${minted.autoUpdate ? "-e KERN_RUNNER_UPDATE=1 " : ""}-e KERN_WORK_ROOT=/opt/kern/work -e HOME=/root ${envFlags} -v /var/run/docker.sock:/var/run/docker.sock -v /opt/kern/work:/opt/kern/work ghcr.io/dvorinka/kern-runner`
		: "";

	return (
		<Card className="mb-4 p-4">
			{minted ? (
				<div>
					<div className="mb-2 text-xs text-[var(--ok)]">
						Enroll token minted — run this on the runner host before it expires:
					</div>
					<div className="mb-2 text-xs text-[var(--faint)]">Binary install:</div>
					<div className="mb-3 flex items-center gap-2 rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2">
						<code className="min-w-0 flex-1 truncate font-mono text-xs">{joinCmd}</code>
						<CopyButton text={joinCmd} />
					</div>
					<div className="mb-2 text-xs text-[var(--faint)]">One container — registers, claims, self-updates:</div>
					<div className="flex items-center gap-2 rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2">
						<code className="min-w-0 flex-1 truncate font-mono text-xs">{dockerCmd}</code>
						<CopyButton text={dockerCmd} />
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
					<label className="flex items-center gap-1.5 text-xs text-[var(--dim)]">
						<input
							type="checkbox"
							checked={autoUpdate}
							onChange={(e) => setAutoUpdate(e.target.checked)}
						/>
						auto-update
					</label>
					<Button type="submit" variant="primary" size="sm" disabled={mint.isPending}>
						Mint token
					</Button>
					{mint.error && <span className="text-xs text-[var(--err)]">{mint.error.message}</span>}
					<div className="w-full">
						<label className="mb-1 block text-[11px] text-[var(--faint)]">
							initial env overrides (KEY=VALUE per line — applied to every job on this runner)
						</label>
						<textarea
							className="w-full rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2 font-mono text-xs"
							rows={3}
							placeholder={"# e.g.\nNODE_ENV=production"}
							value={envDraft}
							onChange={(e) => setEnvDraft(e.target.value)}
						/>
					</div>
				</form>
			)}
		</Card>
	);
}

// EnvEditor manages the operator env override map — KEY=VALUE lines merged
// into every job's environment on this runner (app values win over
// container/env-file defaults; GITHUB_*/RUNNER_* contract keys are protected
// server-side).
function EnvEditor({ runner, onDone }: { runner: Runner; onDone: () => void }) {
	const qc = useQueryClient();
	const [draft, setDraft] = useState(() =>
		Object.entries(runner.env ?? {})
			.map(([k, v]) => `${k}=${v}`)
			.join("\n"),
	);
	const save = useMutation({
		mutationFn: (env: Record<string, string>) => api.patch(`/runners/${runner.id}`, { env }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["runners"] });
			onDone();
		},
	});
	const [formErr, setFormErr] = useState("");
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const env: Record<string, string> = {};
		let n = 0;
		for (const line of draft.split("\n")) {
			n++;
			const t = line.trim();
			if (!t || t.startsWith("#")) continue;
			const eq = t.indexOf("=");
			if (eq <= 0) {
				setFormErr(`line ${n}: expected KEY=value`);
				return;
			}
			env[t.slice(0, eq).trim()] = t.slice(eq + 1);
		}
		setFormErr("");
		save.mutate(env);
	};
	return (
		<div className="ml-[23px] mt-2">
			<form onSubmit={submit}>
				<textarea
					className="w-full rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2 font-mono text-xs"
					rows={Math.min(8, Math.max(3, draft.split("\n").length + 1))}
					placeholder={"KEY=VALUE — one per line\n# comments and blank lines ignored"}
					value={draft}
					onChange={(e) => setDraft(e.target.value)}
				/>
				<div className="mt-2 flex items-center gap-2">
					<Button type="submit" variant="primary" size="sm" disabled={save.isPending}>
						Save env
					</Button>
					<Button type="button" variant="ghost" size="sm" onClick={onDone}>
						Cancel
					</Button>
					{formErr && <span className="text-xs text-[var(--err)]">{formErr}</span>}
					{save.error && <span className="text-xs text-[var(--err)]">{save.error.message}</span>}
				</div>
			</form>
		</div>
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
						`/api/v1/geo/reverse?lat=${la}&lon=${ln}`,
						{ credentials: "same-origin" },
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

// Sensible runner glyphs: platform, substrate, and status marks.
const ICON_PRESETS = [
	"🐧", "🪟", "🍎", "🐳", "☁️", "🖥️", "💻", "⚙️",
	"🔧", "🏗️", "📦", "🚀", "🤖", "🦾", "☸️", "🏠",
	"🌐", "⚡", "🔥", "🍓", "📟", "🏭", "🧊", "🐋",
];

const RECENT_KEY = "kern.runner-icons";

function recentIcons(): string[] {
	try {
		const v = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
		return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 8) : [];
	} catch {
		return [];
	}
}

function pushRecentIcon(icon: string) {
	if (!icon || ICON_PRESETS.includes(icon)) return;
	const next = [icon, ...recentIcons().filter((x) => x !== icon)].slice(0, 8);
	try {
		localStorage.setItem(RECENT_KEY, JSON.stringify(next));
	} catch {
		// storage may be unavailable — recents are a nicety, not a feature
	}
}

// Light client-side check; the server enforces the real bound (500 chars,
// no control chars). Anything that looks like a URL must be http(s).
function iconInputError(v: string): string | null {
	if (v.includes("://") && !/^https?:\/\//i.test(v)) return "image URLs must be http(s)";
	if (v.length > 500) return "max 500 chars";
	return null;
}

// IconEditor sets the runner's glyph: a preset or recent emoji, a short
// label, or an image URL/path. Empty restores the default marker.
function IconEditor({ runner, onDone }: { runner: Runner; onDone: () => void }) {
	const qc = useQueryClient();
	const [draft, setDraft] = useState(runner.icon);
	const [inputErr, setInputErr] = useState<string | null>(null);
	const recents = recentIcons().filter((x) => !ICON_PRESETS.includes(x));
	const save = useMutation({
		mutationFn: (icon: string) => api.patch(`/runners/${runner.id}`, { icon }),
		onSuccess: (_res, icon) => {
			pushRecentIcon(icon);
			qc.invalidateQueries({ queryKey: ["runners"] });
			onDone();
		},
	});
	const apply = (icon: string) => {
		const err = iconInputError(icon);
		setInputErr(err);
		if (!err) save.mutate(icon);
	};
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		apply(draft.trim());
	};
	return (
		<div className="ml-[23px] mt-2">
			<div className="flex flex-wrap items-center gap-1.5">
				<span className="text-[11px] text-[var(--faint)]">pick</span>
				{[...recents, ...ICON_PRESETS].map((g, i) => (
					<button
						key={`${g}-${i}`}
						type="button"
						className={draft === g ? "pill solid" : "pill"}
						title={i < recents.length ? "recent" : undefined}
						onClick={() => setDraft(g)}
					>
						<span className="text-sm leading-none">{g}</span>
					</button>
				))}
			</div>
			<form onSubmit={submit} className="mt-2 flex flex-wrap items-center gap-2">
				<span className="text-[11px] text-[var(--faint)]">preview</span>
				<RunnerIcon icon={draft} className="rricon rrname" />
				<b className="text-xs">{runner.name}</b>
				<Input
					name="icon"
					placeholder="emoji like 🚀 — or an image URL/path"
					value={draft}
					onChange={(e) => {
						setDraft(e.target.value);
						setInputErr(iconInputError(e.target.value.trim()));
					}}
					className="w-64"
				/>
				<Button type="submit" variant="primary" size="sm" disabled={save.isPending || !!inputErr}>
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
				{(inputErr || save.error) && (
					<span className="text-xs text-[var(--err)]">{inputErr ?? save.error?.message}</span>
				)}
			</form>
		</div>
	);
}

// Meter is a thin utilization readout: label, bar, secondary detail.
// null pct means the runner has not reported metrics yet. Grey until hot —
// colour is data, not decoration.
function Meter({ label, pct, sub }: { label: string; pct: number | null; sub: string }) {
	const barColor =
		pct == null ? "transparent" : pct > 85 ? "var(--err)" : pct > 70 ? "var(--warn)" : "var(--ink)";
	return (
		<span className="flex items-center gap-2 text-[11px] leading-4 text-[var(--dim)]">
			<span className="w-7 text-[10px] uppercase tracking-wider text-[var(--faint)]">{label}</span>
			<span className="h-1.5 w-20 overflow-hidden rounded-full bg-[var(--line)]">
				{pct != null && (
					<span
						className="block h-1.5 rounded-full transition-[width] duration-200"
						style={{
							width: `${Math.min(100, Math.max(3, pct))}%`,
							background: barColor,
						}}
					/>
				)}
			</span>
			<span className="min-w-[68px] truncate font-mono text-[10.5px]">
				{pct != null ? `${Math.round(pct)}%` : "—"}
				{sub ? <span className="text-[var(--faint)]"> {sub}</span> : ""}
			</span>
		</span>
	);
}
