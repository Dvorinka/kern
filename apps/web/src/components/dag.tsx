import type { Job } from "../lib/types";

// Layered SVG dependency graph: jobs grouped by topological depth, edges from
// each need to its dependents. Console style: sharp nodes, signal-dot status,
// accent edges flowing left to right.
const STATUS_COLOR: Record<string, string> = {
	success: "var(--ok)",
	failure: "var(--err)",
	cancelled: "var(--text-faint)",
	skipped: "var(--text-faint)",
};

export function JobDAG({ jobs }: { jobs: Job[] }) {
	if (jobs.length <= 1) return null;
	const layers: string[][] = [];
	const depth = new Map<string, number>();
	const resolve = (j: Job, seen: Set<string>): number => {
		const cached = depth.get(j.job_key);
		if (cached !== undefined) return cached;
		if (seen.has(j.job_key)) return 0;
		seen.add(j.job_key);
		let d = 0;
		for (const n of j.needs) {
			const parent = jobs.find((x) => x.job_key === n);
			if (parent) d = Math.max(d, resolve(parent, seen) + 1);
		}
		depth.set(j.job_key, d);
		return d;
	};
	for (const j of jobs) {
		const d = resolve(j, new Set());
		while (layers.length <= d) layers.push([]);
		layers[d]!.push(j.job_key);
	}
	if (layers.length <= 1) return null;

	const W = 168, H = 34, GX = 64, GY = 16;
	const pos = new Map<string, { x: number; y: number }>();
	layers.forEach((keys, li) => {
		keys.forEach((k, i) => pos.set(k, { x: li * (W + GX), y: i * (H + GY) }));
	});
	const maxLayerJobs = Math.max(...layers.map((l) => l.length));
	const width = layers.length * W + (layers.length - 1) * GX;
	const height = maxLayerJobs * H + (maxLayerJobs - 1) * GY;

	return (
		<svg width="100%" viewBox={`0 0 ${width} ${height}`} className="block">
			{jobs.flatMap((j) =>
				j.needs.map((n) => {
					const from = pos.get(n);
					const to = pos.get(j.job_key);
					if (!from || !to) return null;
					const x1 = from.x + W, y1 = from.y + H / 2;
					const x2 = to.x, y2 = to.y + H / 2;
					const mx = (x1 + x2) / 2;
					return (
						<path
							key={`${n}->${j.key}`}
							d={`M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`}
							fill="none"
							stroke="var(--border-strong)"
							strokeWidth="1.25"
							strokeDasharray="3 3"
						/>
					);
				}),
			)}
			{jobs.map((j) => {
				const p = pos.get(j.job_key)!;
				const running = j.status === "in_progress";
				const sc = running ? "var(--run)" : (STATUS_COLOR[j.conclusion] ?? "var(--border-strong)");
				return (
					<g key={j.key}>
						<rect x={p.x} y={p.y} width={W} height={H} rx="2" fill="var(--bg-raised)" stroke={sc} strokeWidth="1.25" />
						<rect x={p.x + 8} y={p.y + H / 2 - 3} width="6" height="6" rx="1" fill={sc}>
							{running && <animate attributeName="opacity" values="1;0.3;1" dur="1.4s" repeatCount="indefinite" />}
						</rect>
						<text x={p.x + 22} y={p.y + H / 2 + 4} fontSize="11" fill="var(--text)" fontFamily="var(--mono)">
							{j.name.length > 20 ? j.name.slice(0, 19) + "…" : j.name}
						</text>
					</g>
				);
			})}
		</svg>
	);
}
