import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import type { Run } from "../lib/types";

type DayCell = {
	date: string;
	label: string;
	count: number;
	status: "none" | "success" | "failure" | "running" | "cancelled";
	today: boolean;
};

// A GitHub-contributions grid for runs: one cell per day, week columns,
// cell colour = the day's dominant status, fill weight = run count.
// Failures and live runs keep their state colour; success scales on ink.
export function ActivityMap({ runs, weeks = 10 }: { runs: Run[]; weeks?: number }) {
	const nav = useNavigate();

	const days = useMemo(() => {
		const byDate = new Map<string, { s: number; f: number; r: number; c: number }>();
		for (const r of runs) {
			const d = (r.created_at ?? "").slice(0, 10);
			if (!d) continue;
			const m = byDate.get(d) ?? { s: 0, f: 0, r: 0, c: 0 };
			if (r.status === "in_progress" || r.status === "queued" || r.status === "awaiting_approval") m.r++;
			else if (r.conclusion === "failure") m.f++;
			else if (r.conclusion === "success") m.s++;
			else m.c++;
			byDate.set(d, m);
		}
		const out: DayCell[] = [];
		for (let i = weeks * 7 - 1; i >= 0; i--) {
			const d = new Date();
			d.setDate(d.getDate() - i);
			const key = d.toISOString().slice(0, 10);
			const m = byDate.get(key);
			const count = m ? m.s + m.f + m.r + m.c : 0;
			let status: DayCell["status"] = "none";
			if (m && m.r > 0) status = "running";
			else if (m && m.f >= Math.max(m.s, 1)) status = "failure";
			else if (m && m.s > 0) status = "success";
			else if (count > 0) status = "cancelled";
			out.push({
				date: key,
				label: d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" }),
				count,
				status,
				today: i === 0,
			});
		}
		return out;
	}, [runs, weeks]);

	const max = Math.max(...days.map((d) => d.count), 1);
	// weeks as columns, days as rows — pad the first column to start on Sunday
	const first = days.length ? new Date(days[0]!.date).getDay() : 0;
	const padded: (DayCell | null)[] = [...Array(first).fill(null), ...days];
	const cols: (DayCell | null)[][] = [];
	for (let i = 0; i < padded.length; i += 7) cols.push(padded.slice(i, i + 7));

	const counts = { success: 0, failure: 0, running: 0 };
	for (const d of days) {
		if (d.status === "success") counts.success += d.count;
		else if (d.status === "failure") counts.failure += d.count;
		else if (d.status === "running") counts.running += d.count;
	}

	return (
		<div className="amap">
			<div className="agrid">
				{cols.map((col, ci) => (
					<div className="acol" key={ci}>
						{col.map((d, di) =>
							d ? (
								<button
									key={d.date}
									className={`acell st-${d.status}${d.today ? " tdy" : ""}`}
									style={{
										opacity:
											d.status === "none" ? 1 : 0.28 + (d.count / max) * 0.72,
									}}
									title={`${d.label} — ${d.count} run${d.count === 1 ? "" : "s"}${d.status === "failure" ? " · failures" : ""} — open day`}
									onClick={() => nav(`/runs?day=${d.date}`)}
									aria-label={`${d.label}: ${d.count} runs`}
								/>
							) : (
								<i key={ci + "-" + di} className="acell gap" />
							),
						)}
					</div>
				))}
			</div>
			<div className="amleg">
				<span>{days.filter((d) => d.count > 0).length} active days</span>
				<span className="key i ok" />
				{counts.success} ok
				<span className="key i err" />
				{counts.failure} failed
				{counts.running > 0 && (
					<>
						<span className="key i live" />
						{counts.running} live
					</>
				)}
			</div>
		</div>
	);
}
