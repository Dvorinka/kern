import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { useMemo, useState, type ReactNode } from "react";
import { api } from "../lib/api";
import type { OverviewResponse, Run, Runner } from "../lib/types";
import { PageHeader, Spinner, cn } from "../components/ui";
import { StatusSq } from "../components/status";
import { Fingerprint, useRunDetails } from "../components/fingerprint";
import { bytes, duration, relTime, refName, shortSha } from "../lib/format";

type Tab = "" | "running" | "failed" | "cancelled";

const TABS: { key: Tab; label: string; match: (r: Run) => boolean }[] = [
	{ key: "", label: "All runs", match: () => true },
	{
		key: "running",
		label: "Running",
		match: (r) => r.status === "in_progress" || r.status === "queued",
	},
	{ key: "failed", label: "Failed", match: (r) => r.conclusion === "failure" },
	{
		key: "cancelled",
		label: "Cancelled",
		match: (r) => r.status === "cancelled" || r.conclusion === "cancelled",
	},
];

export function RunsPage() {
	const [params, setParams] = useSearchParams();
	const tab = (params.get("tab") ?? "") as Tab;
	const [branch, setBranch] = useState("");
	const [older, setOlder] = useState(0);
	const { data, isLoading } = useQuery({
		queryKey: ["runs", "all", older],
		queryFn: () => api.get<{ runs: Run[]; total: number }>(`/runs?limit=60&offset=${older * 60}`),
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

	const runs = useMemo(() => data?.runs ?? [], [data]);
	const branches = useMemo(() => [...new Set(runs.map((r) => refName(r.ref)))].sort(), [runs]);
	const counts = useMemo(() => TABS.map((t) => runs.filter(t.match).length), [runs]);
	const match = TABS.find((t) => t.key === tab)?.match ?? TABS[0]!.match;
	const visible = runs.filter((r) => match(r) && (!branch || refName(r.ref) === branch));
	const details = useRunDetails(visible);

	return (
		<>
			<PageHeader title="Runs" sub="Every push runs on contact" />
			<div className="grid grid-cols-[1fr_300px] items-start gap-[26px] max-lg:grid-cols-1">
				<div className="min-w-0">
					<div className="tabs">
						{TABS.map((t, i) => (
							<button
								key={t.key || "all"}
								onClick={() => setParams(t.key ? { tab: t.key } : {})}
								className={cn(tab === t.key && "on")}
							>
								{t.label}
								<span className="n">{counts[i]}</span>
							</button>
						))}
					</div>
					<div className="mb-0.5 mt-3.5 flex items-center text-[11px] text-[var(--faint)]">
						<select className="fsel" value={branch} onChange={(e) => setBranch(e.target.value)}>
							<option value="">All branches</option>
							{branches.map((b) => (
								<option key={b} value={b}>
									{b}
								</option>
							))}
						</select>
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
							{visible.map((r, i) => {
								const live = r.status === "in_progress" || r.status === "queued";
								return (
									<Link key={r.id} to={`/runs/${r.id}`} className="runrow" data-testid="run-row">
										<StatusSq status={r.status} conclusion={r.conclusion} />
										<div className="min-w-0">
											<div className="truncate text-[13px] font-medium text-[var(--ink)]">
												<span className="mr-2 font-mono font-normal text-[var(--faint)]">
													#{r.run_number}
												</span>
												{r.commit_message?.split("\n")[0] || r.workflow_name}
											</div>
											<div className="mt-[3px] truncate font-mono text-[11px] text-[var(--faint)]">
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
										<Fingerprint run={r} jobs={details[i]?.data?.jobs} small className="fp" />
										<div className="dur text-right font-mono text-xs text-[var(--dim)]">
											{duration(r.started_at, r.completed_at)}
											{live && (
												<small className="block text-[11px] text-[var(--faint)]">elapsed</small>
											)}
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
									</Link>
								);
							})}
							<div className="runscale">
								<div className="labs">
									<span>0</span>
									<span>2m</span>
									<span>4m</span>
									<span>6m</span>
									<span>8m</span>
								</div>
							</div>
							<div className="flex items-baseline gap-3.5 py-[11px] text-[11px] text-[var(--faint)]">
								<span>
									{visible.length} of {data?.total ?? visible.length} runs
								</span>
								{older > 0 && (
									<button className="lnk" onClick={() => setOlder((o) => o - 1)}>
										← Newer
									</button>
								)}
								{(data?.total ?? 0) > (older + 1) * 60 && (
									<button className="lnk ml-auto" onClick={() => setOlder((o) => o + 1)}>
										Older →
									</button>
								)}
							</div>
						</>
					)}
				</div>

				<aside className="min-w-0">
					<SideBlock title="Queue" right={`${overview?.overview.jobs_queued ?? 0} waiting`}>
						<div className="emptynote flush">
							{overview?.overview.jobs_queued
								? `${overview.overview.jobs_queued} job${overview.overview.jobs_queued === 1 ? "" : "s"} waiting for a runner.`
								: "Queue is clear. A push picks a runner on contact."}
						</div>
					</SideBlock>
					<SideBlock title="Fleet" right={`${runners?.runners.length ?? 0} runners`}>
						{(runners?.runners ?? []).map((r) => (
							<div key={r.id} className="fr" style={{ padding: "9px 0" }}>
								<StatusSq status={r.active_jobs ? "in_progress" : r.status} />
								<span className="rn" style={{ width: "auto" }}>
									{r.name}
								</span>
								<span className={cn("rs", r.active_jobs ? "busy" : "")}>
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
