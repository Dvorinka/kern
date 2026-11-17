import type { CSSProperties } from "react";
import type { Job, Run } from "../lib/types";
import { duration } from "../lib/format";
import { jobHue, jobTint } from "./fingerprint";

// Pipeline trace — the mockup's .plate: one shared clock for every job.
// Pattern bars carry job identity, blue means live, the stop-x marks a
// failure, dashed is a job still parked on a need or a runner.

const LH = 56;

// niceTicks picks a readable axis: ~7 divisions at a round interval.
function niceTicks(spanMs: number): { tickMs: number; count: number; totalMs: number } {
	const cands = [
		5_000, 10_000, 15_000, 30_000, 60_000,
		2 * 60_000, 5 * 60_000, 10 * 60_000, 15 * 60_000, 30 * 60_000,
		60 * 60_000, 2 * 60 * 60_000,
	];
	const tick = cands.find((c) => spanMs / c <= 7) ?? 4 * 60 * 60_000;
	const count = Math.max(1, Math.ceil(spanMs / tick));
	return { tickMs: tick, count, totalMs: count * tick };
}

const mmss = (ms: number) =>
	`${Math.floor(ms / 60000)}:${String(Math.floor((ms % 60000) / 1000)).padStart(2, "0")}`;

function tickLabel(ms: number): string {
	if (ms === 0) return "0:00";
	if (ms < 3_600_000) return mmss(ms);
	if (ms % 3_600_000 === 0) return `${ms / 3_600_000}:00`;
	const h = Math.floor(ms / 3_600_000);
	return `${h}:${String(Math.round((ms % 3_600_000) / 60000)).padStart(2, "0")}`;
}

interface Lane {
	j: Job;
	qs: number;
	st: number | null;
	en: number | null;
	running: boolean;
	failed: boolean;
	cancelled: boolean;
	skipped: boolean;
	waiting: boolean;
}

export function RunGantt({
	run,
	jobs,
	runners,
	onPickJob,
}: {
	run: Run;
	jobs: Job[];
	runners: Map<string, string>;
	onPickJob?: (jobID: string) => void;
}) {
	const t0 = Date.parse(run.started_at ?? run.created_at);
	const live = run.status === "in_progress" || run.status === "queued";
	const lastEnd = Math.max(
		...jobs.map((j) => (j.completed_at ? Date.parse(j.completed_at) : 0)),
		Date.parse(run.completed_at ?? "") || 0,
	);
	const now = Date.now();
	const spanMs = Math.max(live ? now - t0 : lastEnd - t0, 30_000);
	let { count, totalMs } = niceTicks(spanMs * 1.06);
	while (count > 8) {
		count = Math.ceil(count / 2);
	}
	const tickMs = totalMs / count;
	const pct = (t: number) => Math.min(100, Math.max(0, ((t - t0) / totalMs) * 100));

	const laneData: Lane[] = jobs.map((j) => {
		const qs = Date.parse(j.queued_at);
		const st = j.started_at ? Date.parse(j.started_at) : null;
		const en = j.completed_at ? Date.parse(j.completed_at) : live ? now : st;
		const running = j.status === "in_progress" || j.status === "waiting";
		const failed = j.conclusion === "failure";
		const cancelled = j.status === "cancelled" || j.conclusion === "cancelled";
		const skipped = j.status === "skipped";
		const waiting = st == null && !skipped && !cancelled;
		return { j, qs, st, en, running, failed, skipped, waiting, cancelled };
	});

	const elapsed = now - t0;
	const flagHide = totalMs * 0.085;

	const laneSub = ({ j, waiting, skipped, cancelled }: Lane): string => {
		if (skipped) return "did not run";
		if (cancelled) return "cancelled";
		if (j.status === "waiting_approval") return "needs an approval";
		if (waiting) return j.needs.length ? `waiting for ${j.needs.join(", ")}` : "queued";
		return j.runner_id ? (runners.get(j.runner_id) ?? "runner") : "unassigned";
	};

	const laneDur = ({ j, st, running, failed, skipped, waiting, cancelled }: Lane) => {
		if (skipped) return <b className="mute">skipped</b>;
		if (cancelled) return <b className="mute">cancelled</b>;
		if (waiting) return <b className="mute">{j.status === "waiting_approval" ? "approval" : "waiting"}</b>;
		if (running) return <b className="live">{mmss(now - st!)}</b>;
		if (failed)
			return (
				<b className="fail">
					failed {j.completed_at ? duration(j.started_at, j.completed_at) : ""}
				</b>
			);
		return <b>{j.completed_at && st != null ? duration(j.started_at, j.completed_at) : "—"}</b>;
	};

	// dependency connector: the need a job actually waited on (latest finisher)
	const conns = laneData
		.map((l, i) => {
			if (l.skipped) return null;
			const best = (l.j.needs ?? [])
				.map((k) => laneData.findIndex((x) => !x.skipped && (x.j.job_key === k || x.j.name === k || x.j.key === k)))
				.filter((x) => x >= 0)
				.sort((a, b) => (laneData[b]!.en ?? now) - (laneData[a]!.en ?? now))[0];
			if (best == null) return null;
			const p = laneData[best]!;
			const x1 = pct(p.en ?? now);
			const x2 = l.st != null ? pct(l.st) : pct(l.qs);
			if (x2 <= x1 + 0.01) return null;
			return { i, best, x1, x2, dashed: l.st == null };
		})
		.filter(Boolean) as { i: number; best: number; x1: number; x2: number; dashed: boolean }[];

	return (
		<div className="tsec">
			<div className="sec2">
				<h3>Trace</h3>
				<span>
					{jobs.length} job{jobs.length === 1 ? "" : "s"} · one clock for every job
				</span>
			</div>
			<div className="plate">
				<div className="tw">
					{/* lane labels */}
					<div className="tcol">
						<div className="axis" />
						{laneData.map((l) => (
							<div
								className={`lane${onPickJob ? " pick" : ""}`}
								style={{ height: LH }}
								key={l.j.id}
								role={onPickJob ? "button" : undefined}
								tabIndex={onPickJob ? 0 : undefined}
								onClick={onPickJob ? () => onPickJob(l.j.id) : undefined}
								onKeyDown={
									onPickJob
										? (e) => {
												if (e.key === "Enter") onPickJob(l.j.id);
											}
										: undefined
								}
								title={onPickJob ? `Show steps for ${l.j.name}` : undefined}
							>
								<b style={{ color: jobTint(l.j.job_key || l.j.name) }}>{l.j.name}</b>
								<small>{laneSub(l)}</small>
							</div>
						))}
					</div>
					{/* axis + tracks */}
					<div className="tcol mid">
						<div className="axis">
							{Array.from({ length: count + 1 }, (_, i) => {
								const t = i * tickMs;
								const hide = live && Math.abs(t - elapsed) < flagHide && i !== 0;
								return (
									<i key={i} style={{ left: `${(t / totalMs) * 100}%`, visibility: hide ? "hidden" : undefined }}>
										{tickLabel(t)}
									</i>
								);
							})}
							{live && (
								<i className="flag" style={{ left: `${pct(now)}%` }}>
									{mmss(elapsed)}
								</i>
							)}
						</div>
						<div className="tracks">
							<svg
								viewBox={`0 0 100 ${laneData.length * LH}`}
								preserveAspectRatio="none"
								aria-hidden="true"
							>
								{Array.from({ length: count + 1 }, (_, i) => (
									<path
										key={i}
										d={`M${(i * tickMs * 100) / totalMs} 0V${laneData.length * LH}`}
										className="gline"
										vectorEffect="non-scaling-stroke"
									/>
								))}
								{conns.map((c) => {
									const y1 = c.best * LH + LH / 2;
									const y2 = c.i * LH + LH / 2;
									const xm = (c.x1 + c.x2) / 2;
									return (
										<path
											key={c.i}
											d={`M${c.x1} ${y1}C${xm} ${y1} ${xm} ${y2} ${c.x2} ${y2}`}
											className="conn"
											vectorEffect="non-scaling-stroke"
											strokeDasharray={c.dashed ? "1 4" : undefined}
										/>
									);
								})}
							</svg>
							{laneData.map((l) => (
								<div className="lane" style={{ height: LH }} key={l.j.id}>
									{l.skipped ? null : l.cancelled ? (
										<i
											className="bar q"
											style={{
												left: `${pct(l.st ?? l.qs)}%`,
												width: `${Math.max(0.6, pct(l.en ?? now) - pct(l.st ?? l.qs))}%`,
											}}
										/>
									) : l.st == null ? (
										<i
											className="bar q"
											style={{ left: `${pct(l.qs)}%`, width: `${Math.max(1.5, pct(now) - pct(l.qs))}%` }}
										/>
									) : (
										<>
											<i
												className={`bar pat-${jobHue(l.j.job_key || l.j.name)}${l.running ? " run" : ""}${l.failed ? " fail" : ""}`}
												style={
													{
														left: `${pct(l.st)}%`,
														width: `${Math.max(0.6, pct(l.en ?? now) - pct(l.st))}%`,
														"--jhc": jobTint(l.j.job_key || l.j.name),
													} as CSSProperties
												}
												title={`${l.j.name} · ${duration(l.j.started_at, l.j.completed_at)}`}
											/>
											{l.running && <i className="head" style={{ left: `${pct(now)}%` }} />}
											{l.failed && l.en != null && <i className="stopx" style={{ left: `${pct(l.en)}%` }} />}
										</>
									)}
								</div>
							))}
						</div>
						{live && <i className="nowline" style={{ left: `${pct(now)}%` }} />}
					</div>
					{/* per-lane duration */}
					<div className="tcol e">
						<div className="axis" />
						{laneData.map((l) => (
							<div className="lane end" style={{ height: LH }} key={l.j.id}>
								{laneDur(l)}
							</div>
						))}
					</div>
				</div>
			</div>
		</div>
	);
}
