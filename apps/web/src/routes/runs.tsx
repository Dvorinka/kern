import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useMemo, useState, type ReactNode } from "react";
import { RotateCcw, X } from "lucide-react";
import { api } from "../lib/api";
import type { Job, OverviewResponse, Project, QueuedJob, Run, Runner } from "../lib/types";
import { PageHeader, RunnerIcon, Spinner, cn } from "../components/ui";
import { StatusPill, StatusSq } from "../components/status";
import { Fingerprint, jobHue, useRunDetails } from "../components/fingerprint";
import { bytes, duration, projectColor, refName, relTime, shortSha } from "../lib/format";

type Tab = "" | "running" | "failed" | "cancelled";

const PAGE_SIZE = 60;
// Job details fan out one request per row; cap it so deep pagination
// doesn't turn "load older" into hundreds of calls.
const DETAIL_CAP = 150;

// Tabs map to server-side filters so counts and pagination are real.
const TABS: { key: Tab; label: string; status?: string; conclusion?: string }[] = [
	{ key: "", label: "All runs" },
	{ key: "running", label: "Running", status: "queued,in_progress" },
	{ key: "failed", label: "Failed", conclusion: "failure" },
	{ key: "cancelled", label: "Cancelled", status: "cancelled" },
];

type RunsResponse = { runs: Run[]; total: number; counts?: Record<string, number> };

export function RunsPage() {
	const nav = useNavigate();
	const [params, setParams] = useSearchParams();
	const tab = (params.get("tab") ?? "") as Tab;
	const project = params.get("project") ?? "";
	const day = params.get("day") ?? "";
	const [branch, setBranch] = useState("");
	const active = TABS.find((t) => t.key === tab) ?? TABS[0]!;
	const filterQuery = new URLSearchParams({ limit: String(PAGE_SIZE), offset: "0" });
	if (active.status) filterQuery.set("status", active.status);
	if (active.conclusion) filterQuery.set("conclusion", active.conclusion);
	if (project) filterQuery.set("project", project);
	if (day) filterQuery.set("day", day);
	const { data, isLoading, fetchNextPage, hasNextPage, isFetchingNextPage } =
		useInfiniteQuery({
			queryKey: ["runs", "all", tab, project, day],
			queryFn: ({ pageParam }) => {
				const q = new URLSearchParams(filterQuery);
				q.set("offset", String(pageParam));
				return api.get<RunsResponse>(`/runs?${q}`);
			},
			initialPageParam: 0,
			getNextPageParam: (last, pages) => {
				const loaded = pages.reduce((n, p) => n + p.runs.length, 0);
				return loaded < last.total ? loaded : undefined;
			},
			refetchInterval: 4000,
		});
	const { data: overview } = useQuery({
		queryKey: ["overview"],
		queryFn: () => api.get<OverviewResponse>("/overview"),
		refetchInterval: 8000,
	});
	const { data: runners } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
		refetchInterval: 8000,
	});
	const { data: projectList } = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	// The real queue, server-side — not reconstructed from visible runs.
	const { data: queueData } = useQuery({
		queryKey: ["queue"],
		queryFn: () => api.get<{ jobs: QueuedJob[] }>("/jobs?status=queued"),
		refetchInterval: 4000,
	});

	const runs = useMemo(() => {
		const seen = new Set<string>();
		const out: Run[] = [];
		for (const p of data?.pages ?? []) {
			for (const r of p.runs) {
				if (!seen.has(r.id)) {
					seen.add(r.id);
					out.push(r);
				}
			}
		}
		return out;
	}, [data]);
	const total = data?.pages[0]?.total ?? 0;
	const counts = data?.pages[0]?.counts;
	const branches = useMemo(() => [...new Set(runs.map((r) => refName(r.ref)))].sort(), [runs]);
	const projects = useMemo(
		() => (projectList?.projects ?? []).map((p) => p.slug).sort(),
		[projectList],
	);
	// Status/project/day filters ran server-side; branch refines locally.
	const visible = runs.filter((r) => !branch || refName(r.ref) === branch);
	const detailRuns = useMemo(() => visible.slice(0, DETAIL_CAP), [visible]);
	const details = useRunDetails(detailRuns);
	const jobsByRun = useMemo(() => {
		const m = new Map<string, Job[]>();
		detailRuns.forEach((r, i) => {
			const jobs = details[i]?.data?.jobs;
			if (jobs) m.set(r.id, jobs);
		});
		return m;
	}, [detailRuns, details]);

	// day groups: runs land under dated headers like a git log
	const groups = useMemo(() => {
		const out: { key: string; label: string; runs: Run[] }[] = [];
		visible.forEach((r) => {
			const d = new Date(r.created_at);
			const key = `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
			const label = dayLabel(d);
			if (out[out.length - 1]?.key !== key) out.push({ key, label, runs: [] });
			out[out.length - 1]!.runs.push(r);
		});
		return out;
	}, [visible]);

	// jobs still parked — the real queue in server claim order
	const queuedJobs = queueData?.jobs ?? [];

	const qc = useQueryClient();
	const clearCancelled = useMutation({
		mutationFn: () =>
			api.post<{ cleared: number }>(
				`/runs/clear-cancelled${project ? `?project=${encodeURIComponent(project)}` : ""}`,
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runs"] }),
	});

	return (
		<>
			<PageHeader mark="build" title="Runs" sub="Every push runs on contact" />
			<div className="grid grid-cols-[1fr_300px] items-start gap-[26px] max-lg:grid-cols-1">
				<div className="min-w-0">
					<div className="tabs">
						{TABS.map((t) => (
							<button
								key={t.key || "all"}
								onClick={() => setParams(t.key ? { tab: t.key } : {})}
								className={cn(tab === t.key && "on")}
							>
								{t.label}
								<span className="n">
									{counts?.[t.key || "all"] ?? "…"}
								</span>
							</button>
						))}
						{tab === "cancelled" && (counts?.cancelled ?? 0) > 0 && (
							<button
								className="lnk ml-auto self-center"
								disabled={clearCancelled.isPending}
								onClick={() => clearCancelled.mutate()}
								title="Hide all cancelled runs from the lists (history is kept)"
							>
								{clearCancelled.isPending
									? "Clearing…"
									: `Clear ${counts?.cancelled} cancelled`}
							</button>
						)}
					</div>
					<div className="mb-0.5 mt-3.5 flex flex-wrap items-center gap-2 text-[11px] text-[var(--faint)]">
						<select className="fsel" value={branch} onChange={(e) => setBranch(e.target.value)}>
							<option value="">All branches</option>
							{branches.map((b) => (
								<option key={b} value={b}>
									{b}
								</option>
							))}
						</select>
						<select
							className="fsel"
							value={project}
							onChange={(e) =>
								setParams((p) => {
									const n = new URLSearchParams(p);
									if (e.target.value) n.set("project", e.target.value);
									else n.delete("project");
									return n;
								})
							}
						>
							<option value="">All projects</option>
							{projects.map((p) => (
								<option key={p} value={p}>
									{p}
								</option>
							))}
						</select>
						{day && (
							<button
								className="fsel"
								onClick={() =>
									setParams((p) => {
										const n = new URLSearchParams(p);
										n.delete("day");
										return n;
									})
								}
								title="Clear day filter"
							>
								{new Date(day).toLocaleDateString(undefined, {
									month: "short",
									day: "numeric",
								})}{" "}
								×
							</button>
						)}
						<span className="ml-auto">
							{visible.length} run{visible.length === 1 ? "" : "s"} · newest first
						</span>
					</div>

					{isLoading ? (
						<Spinner />
					) : visible.length === 0 ? (
						<div className="emptynote flush">
							Nothing here. A run appears the moment a push, webhook or manual trigger lands.
						</div>
					) : (
						<>
							{groups.map((g) => (
								<div key={g.key}>
									<div className="dayh">
										<span>{g.label}</span>
										<span className="dayn">
											{g.runs.length} run{g.runs.length === 1 ? "" : "s"}
										</span>
									</div>
									{g.runs.map((r) => {
										const live = r.status === "in_progress" || r.status === "queued";
										return (
											<RunRow
												key={r.id}
												r={r}
												jobs={jobsByRun.get(r.id)}
												live={live}
												onOpen={() => nav(`/runs/${r.id}`)}
											/>
										);
									})}
								</div>
							))}
							<div className="flex items-baseline gap-3.5 py-[11px] text-[11px] text-[var(--faint)]">
								<span>
									Showing {runs.length} of {total} run{total === 1 ? "" : "s"}
								</span>
								{hasNextPage && (
									<button
										className="lnk ml-auto"
										disabled={isFetchingNextPage}
										onClick={() => fetchNextPage()}
									>
										{isFetchingNextPage ? "Loading…" : "Load older runs →"}
									</button>
								)}
							</div>
						</>
					)}
				</div>

				<aside className="min-w-0">
					<SideBlock title="Queue" right={`${queuedJobs.length || overview?.overview.jobs_queued || 0} waiting`}>
						{queuedJobs.length === 0 && (
							<div className="emptynote flush">Queue is clear. A push picks a runner on contact.</div>
						)}
						{queuedJobs.slice(0, 5).map((j) => (
							<Link to={`/runs/${j.run_id}?job=${encodeURIComponent(j.job_key || j.id)}`} className="qrow rowlink" key={j.id}>
								<i className={`sw pat-${jobHue(j.job_key || j.name)}`} />
								<div className="min-w-0">
									<b className="block truncate">{j.name}</b>
									<small>
										{j.status === "waiting_approval"
											? "needs an approval"
											: j.needs.length
												? `waits for ${j.needs.join(", ")}`
												: "waiting for a runner"}
									</small>
								</div>
								<span className="wt">~{mmss(Date.now() - Date.parse(j.queued_at))}</span>
							</Link>
						))}
						{queuedJobs.length > 5 && (
							<div className="qrow dim">+{queuedJobs.length - 5} more waiting</div>
						)}
					</SideBlock>
					<SideBlock title="Fleet" right={`${runners?.runners.length ?? 0} runners`}>
						{(runners?.runners ?? []).map((r) => (
							<div
								key={r.id}
								className="fleetrow rowlink cursor-pointer"
								onClick={() => nav("/runners")}
							>
								<StatusSq status={r.active_jobs ? "in_progress" : r.status} />
								<RunnerIcon icon={r.icon} className="rricon rrsm" />
								<span className="min-w-0 flex-1 truncate text-[12.5px] font-medium">
									{r.name}
								</span>
								<span
									className={cn(
										"text-[11px]",
										r.active_jobs ? "font-semibold text-[var(--live)]" : "text-[var(--faint)]",
									)}
								>
									{r.active_jobs
										? `${r.active_jobs} job${r.active_jobs === 1 ? "" : "s"}`
										: `idle · ${relTime(r.last_heartbeat_at)}`}
								</span>
							</div>
						))}
						{!runners?.runners.length && (
							<div className="emptynote flush">No runners registered.</div>
						)}
					</SideBlock>
					<SideBlock title="Cache" right={`${overview?.cache.entries ?? 0} entries`}>
						<div className="kv" style={{ padding: "8px 0" }}>
							<span className="k">Stored</span>
							<span className="v">{bytes(overview?.cache.bytes)}</span>
						</div>
						<div className="meter" style={{ margin: "10px 0 4px" }}>
							<i style={{ width: "100%" }} />
						</div>
						<div className="kv" style={{ padding: "8px 0" }}>
							<span className="k">Runs total</span>
							<span className="v">{overview?.overview.runs_total ?? 0}</span>
						</div>
					</SideBlock>
				</aside>
			</div>
		</>
	);
}

const mmss = (ms: number) =>
	`${Math.floor(ms / 60000)}:${String(Math.floor((ms % 60000) / 1000)).padStart(2, "0")}`;

function dayLabel(d: Date): string {
	const day = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
	const today = new Date();
	today.setHours(0, 0, 0, 0);
	if (day === today.getTime()) return "Today";
	if (day === today.getTime() - 86_400_000) return "Yesterday";
	return d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
}

function RunRow({
	r,
	jobs,
	live,
	onOpen,
}: {
	r: Run;
	jobs?: Job[];
	live: boolean;
	onOpen: () => void;
}) {
	const qc = useQueryClient();
	const nav = useNavigate();
	const rerun = useMutation({
		mutationFn: () => api.post<{ run: { id: string } }>(`/runs/${r.id}/rerun`),
		onSuccess: (res) => {
			qc.invalidateQueries({ queryKey: ["runs"] });
			if (res.run?.id) nav(`/runs/${res.run.id}`);
		},
	});
	const cancel = useMutation({
		mutationFn: () => api.post(`/runs/${r.id}/cancel`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["runs"] }),
	});
	const [prev, setPrev] = useState(false);
	const [copied, setCopied] = useState(false);
	const copyLink = () => {
		void navigator.clipboard?.writeText(`${location.origin}/runs/${r.id}`);
		setCopied(true);
		setTimeout(() => setCopied(false), 1200);
	};
	const title = r.commit_message?.split("\n")[0] || `${r.workflow_name} #${r.run_number}`;
	return (
		<div
			className="runrow cursor-pointer"
			data-testid="run-row"
			role="link"
			tabIndex={0}
			onClick={onOpen}
			onKeyDown={(e) => {
				if (e.key === "Enter") onOpen();
			}}
			onMouseEnter={() => setPrev(true)}
			onMouseLeave={() => setPrev(false)}
		>
			<StatusSq status={r.status} conclusion={r.conclusion} />
			<div className="min-w-0">
				<div className="truncate text-[13px] font-medium text-[var(--ink)]">
					<span className="mr-2 font-mono font-normal text-[var(--faint)]">
						#{r.run_number}
					</span>
					{r.commit_message?.split("\n")[0] || r.workflow_name}
				</div>
				<div className="mt-[3px] truncate font-mono text-[11px] text-[var(--faint)]">
					<Link
						to={`/projects/${r.project_slug}`}
						onClick={(e) => e.stopPropagation()}
						className="lnk"
						style={{ color: projectColor(r.project_slug) }}
						title={`Open project ${r.project_name || r.project_slug}`}
					>
						{r.project_slug}
					</Link>
					<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
					{refName(r.ref)}
					<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
					{shortSha(r.sha)}
					<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
					{r.event}
					<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
					{r.actor}
					<i className="mx-1.5 not-italic text-[#3d3d3d]">·</i>
					{r.workflow_name}
				</div>
			</div>
			<Fingerprint run={r} jobs={jobs} small className="fp" />
			<div className="dur text-right font-mono text-xs text-[var(--dim)]">
				{duration(r.started_at, r.completed_at)}
				{live && <small className="block text-[11px] text-[var(--faint)]">elapsed</small>}
				{r.conclusion === "failure" && (
					<small className="block text-[11px] text-[var(--faint)]">failed</small>
				)}
				{r.status === "cancelled" && (
					<small className="block text-[11px] text-[var(--faint)]">cancelled</small>
				)}
			</div>
			<div className="when text-right text-[11px] text-[var(--faint)]">
				{live ? "now" : relTime(r.created_at)}
			</div>
			<span className="racts" onClick={(e) => e.stopPropagation()}>
				<button
					className="ract"
					title="Re-run this workflow"
					disabled={rerun.isPending}
					onClick={() => rerun.mutate()}
				>
					<RotateCcw size={12} />
				</button>
				{live && (
					<button
						className="ract err"
						title="Cancel this run"
						disabled={cancel.isPending}
						onClick={() => cancel.mutate()}
					>
						<X size={12} />
					</button>
				)}
			</span>
			{prev && (
				<div className="runprev" onClick={(e) => e.stopPropagation()}>
					<div className="rp-head">
						<StatusPill status={r.status} conclusion={r.conclusion} />
						<span className="rp-title">{title}</span>
					</div>
					<div className="rp-grid">
						<span className="k">workflow</span>
						<span className="v">
							{r.workflow_name} #{r.run_number}
						</span>
						<span className="k">project</span>
						<span className="v">{r.project_name || r.project_slug}</span>
						<span className="k">branch</span>
						<span className="v">{refName(r.ref)}</span>
						<span className="k">commit</span>
						<span className="v font-mono">{shortSha(r.sha)}</span>
						<span className="k">trigger</span>
						<span className="v">
							{r.event} · {r.actor}
						</span>
						<span className="k">queued</span>
						<span className="v">{relTime(r.created_at)}</span>
						<span className="k">took</span>
						<span className="v">{duration(r.started_at, r.completed_at) || "—"}</span>
						{jobs && (
							<>
								<span className="k">jobs</span>
								<span className="v">
									{jobs.length}
									{jobs.some((j) => j.status === "queued" || j.status === "waiting") &&
										` · ${jobs.filter((j) => j.status === "queued" || j.status === "waiting").length} waiting`}
								</span>
							</>
						)}
					</div>
					<Fingerprint run={r} jobs={jobs} />
					<div className="rp-acts">
						<button className="rpact" onClick={onOpen}>
							Open run
						</button>
						<button className="rpact" onClick={() => rerun.mutate()} disabled={rerun.isPending}>
							Re-run
						</button>
						{live && (
							<button className="rpact err" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
								Cancel
							</button>
						)}
						<button className="rpact ghost" onClick={copyLink}>
							{copied ? "Copied" : "Copy link"}
						</button>
					</div>
				</div>
			)}
		</div>
	);
}

function SideBlock({
	title,
	right,
	children,
}: {
	title: string;
	right?: string;
	children: ReactNode;
}) {
	return (
		<div className="mt-[26px] first:mt-5">
			<div className="flex items-baseline border-b border-[var(--line)] pb-[9px] text-xs font-medium text-[var(--dim)]">
				{title}
				{right && (
					<span className="ml-auto text-[11px] font-normal text-[var(--faint)]">{right}</span>
				)}
			</div>
			{children}
		</div>
	);
}
