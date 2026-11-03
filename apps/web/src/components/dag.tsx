import type { Job } from "../lib/types";

// Layered SVG dependency graph: jobs grouped by topological depth, edges from
// each need to its dependents. Read-only; the list view carries interactions.
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

	const W = 160, H = 30, GX = 60, GY = 18;
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
							strokeWidth="1.5"
						/>
					);
				}),
			)}
			{jobs.map((j) => {
				const p = pos.get(j.job_key)!;
				const failed = j.conclusion === "failure";
				const ok = j.conclusion === "success";
				const stroke = failed ? "var(--err)" : ok ? "var(--ok)" : "var(--border-strong)";
				return (
					<g key={j.key}>
						<rect
							x={p.x}
							y={p.y}
							width={W}
							height={H}
							rx="6"
							fill="var(--bg-raised)"
							stroke={stroke}
							strokeWidth="1.5"
						/>
						<text
							x={p.x + W / 2}
							y={p.y + H / 2 + 4}
							textAnchor="middle"
							fontSize="11"
							fill="var(--text)"
						>
							{j.name.length > 22 ? j.name.slice(0, 21) + "…" : j.name}
						</text>
					</g>
				);
			})}
		</svg>
	);
}
