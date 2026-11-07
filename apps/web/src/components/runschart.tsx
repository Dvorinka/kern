import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { projectColor } from "../lib/format";
import type { Run } from "../lib/types";

export interface DayPoint {
	/** "Nov 7" style label */
	label: string;
	/** local midnight ISO — usable as a /runs?day= filter */
	dayISO: string;
	total: number;
	failed: number;
	cancelled: number;
	projects: { slug: string; count: number }[];
	today: boolean;
}

const W = 760;
const H = 210;
const PAD_L = 34;
const PAD_R = 10;
const PAD_T = 14;
const PAD_B = 26;
const PLOT_W = W - PAD_L - PAD_R;
const PLOT_H = H - PAD_T - PAD_B;

// 14-day window of runs/day, oldest first, with a per-project breakdown.
export function runsPerDay(runs: Run[]): DayPoint[] {
	const days: DayPoint[] = [];
	const now = new Date();
	for (let i = 13; i >= 0; i--) {
		const d = new Date(now);
		d.setDate(now.getDate() - i);
		d.setHours(0, 0, 0, 0);
		days.push({
			label: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
			dayISO: d.toISOString(),
			total: 0,
			failed: 0,
			cancelled: 0,
			projects: [],
			today: i === 0,
		});
	}
	const projCount = days.map(() => new Map<string, number>());
	for (const r of runs) {
		const t = new Date(r.created_at);
		t.setHours(0, 0, 0, 0);
		const idx = 13 - Math.round((Date.now() - t.getTime()) / 86_400_000);
		if (idx < 0 || idx > 13) continue;
		days[idx]!.total++;
		if (r.conclusion === "failure") days[idx]!.failed++;
		if (r.status === "cancelled" || r.conclusion === "cancelled") days[idx]!.cancelled++;
		const m = projCount[idx]!;
		m.set(r.project_slug, (m.get(r.project_slug) ?? 0) + 1);
	}
	days.forEach((d, i) => {
		d.projects = [...projCount[i]!.entries()]
			.map(([slug, count]) => ({ slug, count }))
			.sort((a, b) => b.count - a.count);
	});
	return days;
}

function niceCeil(n: number): number {
	if (n <= 5) return 5;
	const step = Math.pow(10, Math.floor(Math.log10(n)));
	return Math.ceil(n / (step / 2)) * (step / 2);
}

// catmull-rom → cubic bezier, for a smooth line through the daily points.
function smoothPath(pts: { x: number; y: number }[]): string {
	if (pts.length < 2) return pts.length ? `M ${pts[0]!.x} ${pts[0]!.y}` : "";
	let d = `M ${pts[0]!.x} ${pts[0]!.y}`;
	for (let i = 0; i < pts.length - 1; i++) {
		const p0 = pts[Math.max(0, i - 1)]!;
		const p1 = pts[i]!;
		const p2 = pts[i + 1]!;
		const p3 = pts[Math.min(pts.length - 1, i + 2)]!;
		const c1x = p1.x + (p2.x - p0.x) / 6;
		const c1y = p1.y + (p2.y - p0.y) / 6;
		const c2x = p2.x - (p3.x - p1.x) / 6;
		const c2y = p2.y - (p3.y - p1.y) / 6;
		d += ` C ${c1x} ${c1y}, ${c2x} ${c2y}, ${p2.x} ${p2.y}`;
	}
	return d;
}

export function RunsChart({ days, version }: { days: DayPoint[]; version?: string }) {
	const nav = useNavigate();
	const wrapRef = useRef<HTMLDivElement>(null);
	const [hover, setHover] = useState<number | null>(null);

	const geom = useMemo(() => {
		const max = Math.max(...days.map((d) => d.total), 1);
		const yMax = niceCeil(max);
		const x = (i: number) => PAD_L + (i / (days.length - 1)) * PLOT_W;
		const y = (v: number) => PAD_T + PLOT_H - (v / yMax) * PLOT_H;
		const totalPts = days.map((d, i) => ({ x: x(i), y: y(d.total) }));
		const failPts = days.map((d, i) => ({ x: x(i), y: y(d.failed) }));
		const line = smoothPath(totalPts);
		const failLine = smoothPath(failPts);
		const area = `${line} L ${totalPts[totalPts.length - 1]!.x} ${PAD_T + PLOT_H} L ${totalPts[0]!.x} ${PAD_T + PLOT_H} Z`;
		return { yMax, x, y, totalPts, failPts, line, failLine, area };
	}, [days]);

	const onMove = (e: React.MouseEvent) => {
		const rect = wrapRef.current?.getBoundingClientRect();
		if (!rect) return;
		const px = ((e.clientX - rect.left) / rect.width) * W;
		const idx = Math.round(((px - PAD_L) / PLOT_W) * (days.length - 1));
		setHover(Math.max(0, Math.min(days.length - 1, idx)));
	};

	const h = hover != null ? days[hover] : null;
	const anyRuns = days.some((d) => d.total > 0);

	return (
		<div ref={wrapRef} className="runschart" onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
			<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="block h-[210px] w-full">
				<defs>
					<linearGradient id="rc-fill" x1="0" y1="0" x2="0" y2="1">
						<stop offset="0%" stopColor="var(--ink)" stopOpacity="0.16" />
						<stop offset="100%" stopColor="var(--ink)" stopOpacity="0.01" />
					</linearGradient>
				</defs>
				{/* gridlines */}
				{[0, 1, 2, 3].map((i) => {
					const v = (geom.yMax * (3 - i)) / 3;
					const yy = geom.y(v);
					return (
						<g key={i}>
							<line x1={PAD_L} x2={W - PAD_R} y1={yy} y2={yy} className="rc-grid" />
							<text x={PAD_L - 8} y={yy + 3} className="rc-axis" textAnchor="end">
								{Math.round(v)}
							</text>
						</g>
					);
				})}
				{!anyRuns && (
					<text x={W / 2} y={H / 2} className="rc-empty" textAnchor="middle">
						No runs in the last 14 days.
					</text>
				)}
				{/* area + total line */}
				{anyRuns && (
					<>
						<path d={geom.area} fill="url(#rc-fill)" />
						<path d={geom.line} className="rc-line" />
						{/* failure line */}
						{days.some((d) => d.failed > 0) && (
							<path d={geom.failLine} className="rc-fail-line" />
						)}
						{/* day markers */}
						{days.map((d, i) => (
							<g key={i}>
								{d.today && (
									<rect
										x={geom.x(i) - PLOT_W / days.length / 2}
										y={PAD_T}
										width={PLOT_W / days.length}
										height={PLOT_H}
										className="rc-today"
									/>
								)}
								{d.total > 0 && (
									<circle cx={geom.x(i)} cy={geom.y(d.total)} r="2.5" className="rc-dot" />
								)}
								{d.failed > 0 && (
									<circle
										cx={geom.x(i)}
										cy={geom.y(d.failed)}
										r="3"
										className="rc-fail-dot"
									/>
								)}
								{(d.today || i % 6 === 1) && (
									<text
										x={geom.x(i)}
										y={H - 8}
										className="rc-axis"
										textAnchor="middle"
									>
										{d.today ? "today" : d.label}
									</text>
								)}
							</g>
						))}
						{/* hover crosshair */}
						{h && hover != null && (
							<g>
								<line
									x1={geom.x(hover)}
									x2={geom.x(hover)}
									y1={PAD_T}
									y2={PAD_T + PLOT_H}
									className="rc-hover"
								/>
								{h.total > 0 && (
									<circle
										cx={geom.x(hover)}
										cy={geom.y(h.total)}
										r="4"
										className="rc-hover-dot"
									/>
								)}
							</g>
						)}
						{/* click targets — one per day */}
						{days.map((d, i) => (
							<rect
								key={`hit-${i}`}
								x={geom.x(i) - PLOT_W / days.length / 2}
								y={PAD_T}
								width={PLOT_W / days.length}
								height={PLOT_H}
								className="rc-hit"
								onClick={() => nav(`/runs?day=${encodeURIComponent(d.dayISO)}`)}
							>
								<title>{`${d.label} · ${d.total} run${d.total === 1 ? "" : "s"}${d.failed ? `, ${d.failed} failed` : ""} — click for runs`}</title>
							</rect>
						))}
					</>
				)}
			</svg>
			{h && (
				<div
					className="rc-tip"
					style={{
						left: `${Math.min(88, Math.max(4, (hover! / (days.length - 1)) * 100))}%`,
					}}
				>
					<div className="rc-tip-head">
						<span>{h.today ? "today" : h.label}</span>
						<span className="mono">
							{h.total} run{h.total === 1 ? "" : "s"}
							{h.failed > 0 && <span className="rc-tip-fail"> · {h.failed} failed</span>}
						</span>
					</div>
					{h.projects.slice(0, 5).map((p) => (
						<div className="rc-tip-row" key={p.slug}>
							<i style={{ background: projectColor(p.slug) }} />
							<span className="rc-tip-slug">{p.slug}</span>
							<span className="mono">{p.count}</span>
						</div>
					))}
					{version && h.today && (
						<div className="rc-tip-ver">deployed v{version.replace(/^v/, "")}</div>
					)}
				</div>
			)}
		</div>
	);
}
