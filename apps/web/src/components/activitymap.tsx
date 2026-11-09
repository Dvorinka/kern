import { useMemo } from "react";
import { useNavigate } from "react-router-dom";

export interface DayTotals {
	date: string; // yyyy-mm-dd
	total: number;
	failed: number;
	cancelled: number;
	live: number;
}

type DayCell = {
	date: string;
	label: string;
	count: number;
	status: "none" | "success" | "failure" | "running" | "cancelled";
	today: boolean;
};

// A GitHub-contributions grid for runs: one cell per day, week columns,
// cell colour = the day's dominant status, fill weight = run count.
// Counts come from the server aggregate — history is not limited to
// whatever the runs list happened to return.
export function ActivityMap({ counts, weeks = 10 }: { counts: DayTotals[]; weeks?: number }) {
	const nav = useNavigate();

	const days = useMemo(() => {
		const byDate = new Map(counts.map((c) => [c.date, c]));
		const out: DayCell[] = [];
		for (let i = weeks * 7 - 1; i >= 0; i--) {
			const d = new Date();
			d.setDate(d.getDate() - i);
			const key = d.toISOString().slice(0, 10);
			const m = byDate.get(key);
			const count = m ? m.total : 0;
			const ok = m ? m.total - m.failed - m.cancelled - m.live : 0;
			let status: DayCell["status"] = "none";
			if (m && m.live > 0) status = "running";
			else if (m && m.failed >= Math.max(ok, 1)) status = "failure";
			else if (m && ok > 0) status = "success";
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
	}, [counts, weeks]);

	const max = Math.max(...days.map((d) => d.count), 1);
	// weeks as columns, days as rows — pad the first column to start on Sunday
	const first = days.length ? new Date(days[0]!.date).getDay() : 0;
	const padded: (DayCell | null)[] = [...Array(first).fill(null), ...days];
	const cols: (DayCell | null)[][] = [];
	for (let i = 0; i < padded.length; i += 7) cols.push(padded.slice(i, i + 7));

	const legend = { success: 0, failure: 0, running: 0, cancelled: 0 };
	for (const d of days) {
		if (d.status === "success") legend.success += d.count;
		else if (d.status === "failure") legend.failure += d.count;
		else if (d.status === "running") legend.running += d.count;
		else if (d.status === "cancelled") legend.cancelled += d.count;
	}

	// month tick under each week column when the month rolls over
	const monthTicks = cols.map((col, i) => {
		const first = col.find((d) => d)?.date;
		if (!first) return "";
		const m = new Date(`${first}T00:00:00`).toLocaleDateString(undefined, { month: "short" });
		const prev = i > 0 ? cols[i - 1]!.find((d) => d)?.date : null;
		const pm = prev
			? new Date(`${prev}T00:00:00`).toLocaleDateString(undefined, { month: "short" })
			: null;
		return m !== pm ? m : "";
	});

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
			<div className="amonths">
				{monthTicks.map((m, i) => (
					<span key={i}>{m}</span>
				))}
			</div>
			<div className="amleg">
				<span>{days.filter((d) => d.count > 0).length} active days</span>
				<span className="key i ok" />
				{legend.success} ok
				<span className="key i err" />
				{legend.failure} failed
				{legend.running > 0 && (
					<>
						<span className="key i live" />
						{legend.running} live
					</>
				)}
				{legend.cancelled > 0 && (
					<>
						<span className="key i cxl" />
						{legend.cancelled} cancelled
					</>
				)}
			</div>
		</div>
	);
}
