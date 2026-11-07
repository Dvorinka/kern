import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { Download, Package } from "lucide-react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { Artifact } from "../lib/types";
import { EmptyState, PageHeader, Spinner } from "../components/ui";
import { bytes, relTime } from "../lib/format";

export function ArtifactsPage() {
	const { data, isLoading } = useQuery({
		queryKey: ["artifacts"],
		queryFn: () => api.get<{ artifacts: Artifact[] }>("/artifacts"),
	});

	const artifacts = useMemo(() => data?.artifacts ?? [], [data]);
	const totalBytes = artifacts.reduce((n, a) => n + a.size_bytes, 0);
	const largest = Math.max(...artifacts.map((a) => a.size_bytes), 1);

	// artifacts belong to runs — shelf them that way, newest run first
	const groups = useMemo(() => {
		const m = new Map<string, { runId: string; runNumber: number; project: string; items: Artifact[] }>();
		for (const a of artifacts) {
			const g =
				m.get(a.run_id) ??
				m.set(a.run_id, {
					runId: a.run_id,
					runNumber: a.run_number,
					project: a.project_slug,
					items: [],
				}).get(a.run_id)!;
			g.items.push(a);
		}
		return [...m.values()];
	}, [artifacts]);

	return (
		<>
			<PageHeader mark="web"
				title="Artifacts"
				sub="Files uploaded by actions/upload-artifact, stored as compressed archives"
			/>
			{isLoading ? (
				<Spinner />
			) : artifacts.length === 0 ? (
				<div className="panel">
					<EmptyState icon={<Package size={19} />}
						title="No artifacts"
						hint="Artifacts appear here when a run uploads files with actions/upload-artifact."
					/>
				</div>
			) : (
				<>
					<div className="panel kpis mb-4">
						<div className="kpi">
							<div className="k">artifacts</div>
							<div className="v">{artifacts.length}</div>
							<div className="c">across {groups.length} run{groups.length === 1 ? "" : "s"}</div>
						</div>
						<div className="kpi">
							<div className="k">stored</div>
							<div className="v">{bytes(totalBytes)}</div>
							<div className="c">compressed archives</div>
						</div>
						<div className="kpi">
							<div className="k">largest</div>
							<div className="v">{bytes(largest)}</div>
							<div className="c">single archive</div>
						</div>
					</div>
					<div className="panel">
						{groups.map((g) => (
							<div key={g.runId}>
								<div className="vgh">
									<span>
										<Link to={`/projects/${g.project}`} className="lnk">
											{g.project}
										</Link>{" "}
										·{" "}
										<Link to={`/runs/${g.runId}`} className="lnk">
											run #{g.runNumber}
										</Link>
									</span>
									<span>
										{g.items.length} file{g.items.length === 1 ? "" : "s"} ·{" "}
										{bytes(g.items.reduce((n, a) => n + a.size_bytes, 0))}
									</span>
								</div>
								{g.items.map((a) => (
									<div key={a.id} className="vrow">
										<span className="font-mono min-w-0 flex-1 truncate text-[13px]">
											{a.name}
										</span>
										<div className="hidden w-28 sm:block">
											<div className="meter">
												<i style={{ width: `${Math.max(2, (a.size_bytes / largest) * 100)}%` }} />
											</div>
										</div>
										<span className="w-16 shrink-0 text-right font-mono text-xs text-[var(--dim)]">
											{bytes(a.size_bytes)}
										</span>
										<span
											className="w-24 shrink-0 text-right text-[11px] text-[var(--faint)]"
											title={a.expires_at ? `Expires ${a.expires_at}` : "No expiry"}
										>
											{a.expires_at ? `exp ${relTime(a.expires_at)}` : relTime(a.created_at)}
										</span>
										<a
											href={`/api/v1/artifacts/${a.id}/download`}
											download
											className="ract grid shrink-0 place-items-center"
											title={`Download ${a.name}.tar.gz`}
										>
											<Download size={13} />
										</a>
									</div>
								))}
							</div>
						))}
					</div>
				</>
			)}
		</>
	);
}
