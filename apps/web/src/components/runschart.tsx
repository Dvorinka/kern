import { useRef, useState } from "react";
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

// The mockup's .bars — a flex row of rounded columns, today in live blue.
// A failed segment caps the column in red; hover shows the day breakdown.
export function RunsChart({ days, version }: { days: DayPoint[]; version?: string }) {
	const nav = useNavigate();
	const wrapRef = useRef<HTMLDivElement>(null);
	const [hover, setHover] = useState<number | null>(null);

	const max = Math.max(...days.map((d) => d.total), 1);
	const anyRuns = days.some((d) => d.total > 0);
	const h = hover != null ? days[hover] : null;

	return (
		<div
			ref={wrapRef}
			className="runschart"
			onMouseLeave={() => setHover(null)}
			onMouseMove={(e) => {
				const rect = wrapRef.current?.getBoundingClientRect();
				if (!rect) return;
				const i = Math.floor(((e.clientX - rect.left - 22) / (rect.width - 44)) * days.length);
				setHover(Math.max(0, Math.min(days.length - 1, i)));
			}}
		>
			<div className="bars">
				{days.map((d, i) => (
					<i
						key={d.dayISO}
						className={d.today ? "tdy" : ""}
						style={{ height: `${Math.max(2, (d.total / max) * 100)}%` }}
						onMouseEnter={() => setHover(i)}
						onClick={() => nav(`/runs?day=${encodeURIComponent(d.dayISO)}`)}
						title={`${d.label} · ${d.total} run${d.total === 1 ? "" : "s"}${d.failed ? `, ${d.failed} failed` : ""} — click for runs`}
					>
						{d.failed > 0 && (
							<em style={{ height: `${Math.min(100, (d.failed / d.total) * 100)}%` }} />
						)}
					</i>
				))}
			</div>
			<div className="bax">
				<span>{days[0]?.label}</span>
				{!anyRuns && <span className="g3">no runs yet</span>}
				<span>today</span>
			</div>
			{h && (
				<div
					className="rc-tip"
					style={{
						left: `${Math.min(88, Math.max(8, ((hover! + 0.5) / days.length) * 100))}%`,
					}}
				>
					<div className="rc-tip-head">
						<span>{h.today ? "today" : h.label}</span>
						<span className="mono">
							{h.total} run{h.total === 1 ? "" : "s"}
							{h.failed > 0 && <span className="rc-tip-fail"> · {h.failed} failed</span>}
							{h.cancelled > 0 && <span className="rc-tip-cxl"> · {h.cancelled} cancelled</span>}
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
