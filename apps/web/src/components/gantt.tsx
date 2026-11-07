import type { Job, Run } from "../lib/types";
import { duration } from "../lib/format";
import { jobHue } from "./fingerprint";

// Pipeline gantt: every job lane on one shared clock. Done carries the job
// hue tint, live is white with stripes under a now-cursor, queued hatches
// from its claim time. Lane labels carry the runner name.

// niceTicks picks a readable axis: ~7 divisions at a round interval.
function niceTicks(spanMs: number): { tickMs: number; count: number; totalMs: number } {
	const cands = [
		5_000,
		10_000,
		15_000,
		30_000,
		60_000,
		2 * 60_000,
		5 * 60_000,
		10 * 60_000,
		15 * 60_000,
		30 * 60_000,
		60 * 60_000,
		2 * 60 * 60_000,
	];
	const tick = cands.find((c) => spanMs / c <= 7) ?? 4 * 60 * 60_000;
	const count = Math.max(1, Math.ceil(spanMs / tick));
	return { tickMs: tick, count, totalMs: count * tick };
}

function tickLabel(ms: number): string {
	if (ms === 0) return "0";
	if (ms % 3_600_000 === 0) return `${ms / 3_600_000}h`;
	if (ms % 60_000 === 0) return `${ms / 60_000}m`;
	return `${ms / 1000}s`;
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
	const { tickMs, count, totalMs } = niceTicks(spanMs);
	const pct = (t: number) => Math.min(100, Math.max(0, ((t - t0) / totalMs) * 100));

	const laneData = jobs.map((j) => {
		const qs = Date.parse(j.queued_at);
		const st = j.started_at ? Date.parse(j.started_at) : null;
		const en = j.completed_at ? Date.parse(j.completed_at) : live ? now : st;
		const running = j.status === "in_progress" || j.status === "waiting";
		const failed = j.conclusion === "failure" || j.status === "cancelled";
		return { j, qs, st, en, running, failed };
	});

	const elapsed = duration(run.started_at ?? run.created_at, run.completed_at);

	return (
		<div className="panel mt-4">
			<div className="gt-head">
				<span className="pt">Pipeline</span>
				<span className="gt-sub">
					{jobs.length} job{jobs.length === 1 ? "" : "s"} · elapsed{" "}
					<b className="mono">{elapsed}</b>
				</span>
				<span className="legend">
					<span>
						<i className="sw-done" />
						done
					</span>
					<span>
						<i className="sw-run" />
						running
					</span>
					<span>
						<i className="sw-wait" />
						queued
					</span>
					<span>
						<i className="sw-err" />
						failed
					</span>
				</span>
			</div>
			<div className="gbody">
				<div className="axis-col">
					<div className="axis" />
					<div className="lnames">
						{laneData.map(({ j }) => (
							<div
								className={`ln${onPickJob ? " pick" : ""}`}
								key={j.id}
								role={onPickJob ? "button" : undefined}
								tabIndex={onPickJob ? 0 : undefined}
								onClick={onPickJob ? () => onPickJob(j.id) : undefined}
								onKeyDown={
									onPickJob
										? (e) => {
												if (e.key === "Enter") onPickJob(j.id);
											}
										: undefined
								}
								title={onPickJob ? `Show steps for ${j.name}` : undefined}
							>
								{j.name}
								<small>{j.runner_id ? (runners.get(j.runner_id) ?? "runner") : "unassigned"}</small>
							</div>
						))}
					</div>
				</div>
				<div className="lanes">
					<div className="axis">
						{Array.from({ length: count + 1 }, (_, i) => (
							<span key={i} style={{ left: `${(i / count) * 100}%` }}>
								{tickLabel(i * tickMs)}
							</span>
						))}
					</div>
					<div
						className="ggrid"
						style={{
							background: `repeating-linear-gradient(to right, transparent 0 calc(${100 / count}% - 1px), var(--line) calc(${100 / count}% - 1px) ${100 / count}%)`,
						}}
					/>
					{laneData.map(({ j, qs, st, en, running, failed }) => (
						<div className="lane" key={j.id}>
							{st != null && qs < st && (
								<div
									className="seg gap"
									style={{ left: `${pct(qs)}%`, width: `${Math.max(0.3, pct(st) - pct(qs))}%` }}
								/>
							)}
							{st != null && en != null && (
								<div
									className={`seg ${onPickJob ? "pick " : ""}${failed ? "fail" : running ? "live" : `done ${jobHue(j.job_key || j.name)}`}`}
									style={{ left: `${pct(st)}%`, width: `${Math.max(0.6, pct(en) - pct(st))}%` }}
									title={`${j.name} · ${duration(j.started_at, j.completed_at)}`}
									onClick={onPickJob ? () => onPickJob(j.id) : undefined}
								>
									{pct(en) - pct(st) > 7 ? duration(j.started_at, j.completed_at) : ""}
								</div>
							)}
							{st == null && (
								<div
									className={`seg wait${onPickJob ? " pick" : ""}`}
									style={{ left: `${pct(qs)}%`, width: `${Math.max(1.5, pct(now) - pct(qs))}%` }}
									onClick={onPickJob ? () => onPickJob(j.id) : undefined}
									title={
										j.status === "waiting_approval"
											? `${j.name} · waiting for approval`
											: `${j.name} · queued since ${new Date(qs).toLocaleTimeString()}`
									}
								>
									{j.status === "waiting_approval" ? "approval" : "queued"}
								</div>
							)}
						</div>
					))}
					{live && <div className="now" style={{ left: `${pct(now)}%` }} data-t={elapsed} />}
				</div>
			</div>
			<div
				className="cells"
				style={{ gridTemplateColumns: `repeat(${Math.min(4, laneData.length + 3)}, 1fr)` }}
			>
				<div className="cell">
					<div className="k">Runner</div>
					<div className="v mono">
						{laneData.find((l) => l.j.runner_id)?.j.runner_id
							? (runners.get(laneData.find((l) => l.j.runner_id)!.j.runner_id!) ?? "—")
							: "—"}
					</div>
				</div>
				<div className="cell">
					<div className="k">Image</div>
					<div className="v mono">{laneData.find((l) => l.j.image)?.j.image ?? "—"}</div>
				</div>
				<div className="cell">
					<div className="k">Duration</div>
					<div className="v mono">{elapsed}</div>
				</div>
				<div className="cell">
					<div className="k">Queued wait</div>
					<div className="v mono">{queueWait(jobs)}</div>
				</div>
			</div>
		</div>
	);
}

function queueWait(jobs: Job[]): string {
	const waits = jobs
		.filter((j) => j.started_at && j.queued_at)
		.map((j) => Date.parse(j.started_at!) - Date.parse(j.queued_at));
	if (waits.length === 0) return "—";
	const max = Math.max(...waits);
	if (max < 1000) return `${max}ms`;
	const s = Math.round(max / 1000);
	return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}
