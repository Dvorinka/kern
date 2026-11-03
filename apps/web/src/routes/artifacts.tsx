import { useQuery } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { Artifact } from "../lib/types";
import { Card, EmptyState, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { bytes, relTime } from "../lib/format";

export function ArtifactsPage() {
	const { data, isLoading } = useQuery({
		queryKey: ["artifacts"],
		queryFn: () => api.get<{ artifacts: Artifact[] }>("/artifacts"),
	});

	return (
		<>
			<PageHeader
				title="Artifacts"
				sub="Files uploaded by actions/upload-artifact, stored as compressed archives"
			/>
			{isLoading ? (
				<Spinner />
			) : (
				<Card>
					{!data || data.artifacts.length === 0 ? (
						<EmptyState
							title="No artifacts"
							hint="Artifacts appear here when a run uploads files with actions/upload-artifact."
						/>
					) : (
						data.artifacts.map((a) => (
							<Row key={a.id}>
								<span className="font-mono min-w-0 flex-1 truncate text-[13px]">{a.name}</span>
								<Link
									to={`/runs/${a.run_id}`}
									className="shrink-0 text-xs text-[var(--text-muted)] hover:text-[var(--text)]"
								>
									{a.project_slug}#{a.run_number}
								</Link>
								<span className="w-16 shrink-0 text-right text-xs text-[var(--text-faint)]">
									{bytes(a.size_bytes)}
								</span>
								<span className="w-20 shrink-0 text-right text-xs text-[var(--text-faint)]">
									{relTime(a.created_at)}
								</span>
								<a
									href={`/api/v1/artifacts/${a.id}/download`}
									download
									className="shrink-0 rounded p-1.5 text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]"
									title={`Download ${a.name}.tar.gz`}
								>
									<Download size={14} />
								</a>
							</Row>
						))
					)}
				</Card>
			)}
		</>
	);
}
