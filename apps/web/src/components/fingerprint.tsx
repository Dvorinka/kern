import { useQueries } from "@tanstack/react-query";
import { api } from "../lib/api";
import type { Job, Run, RunDetail } from "../lib/types";
import { cn } from "./ui";

// Job-keyed hues: the same key always lands on the same lane colour.
// Names matching lint/build/web take their canonical hue; anything else
// hashes deterministically into the palette.
const HUES = ["lint", "build", "web"] as const;
export type JobHue = (typeof HUES)[number];

export function jobHue(key: string): JobHue {
	const k = key.toLowerCase();
	if (k.includes("lint") || k.includes("check")) return "lint";
	if (k.includes("build") || k.includes("test") || k.includes("compile")) return "build";
	if (k.includes("web") || k.includes("deploy") || k.includes("e2e") || k.includes("ui"))
		return "web";
	let h = 0;
	for (let i = 0; i < k.length; i++) h = (h * 31 + k.charCodeAt(i)) >>> 0;
	return HUES[h % 3] ?? "lint";
}

const HUE_CLS: Record<JobHue, string> = { lint: "l", build: "b", web: "w2" };
// Tinted variant: completed jobs take a real stage colour instead of the
// monochrome fill pattern — used where a bar must read as a pipeline.
const HUE_TINT: Record<string, string> = { l: "h1", b: "h2", w2: "h3" };

// Shared run-clock: every fingerprint is drawn on the same wall of minutes,
// extended when a run outlasts it.
export const FP_SCALE_MS = 8 * 60 * 1000;

interface Seg {
	cls: string;
	left: number;
	width: number;
	label: string;
}

// segs turns a run's jobs into positioned bar segments on the shared scale.
// Live jobs end at now; queued jobs hatch from their queued_at; a run that
// never started hatches a stub for its queue wait.
export function fingerprintSegs(
	jobs: Job[],
	run: Run,
	scaleMs = FP_SCALE_MS,
): { segs: Seg[]; scaleMs: number } {
	const t0 = Date.parse(run.started_at ?? run.created_at);
	const end = run.completed_at ? Date.parse(run.completed_at) : Date.now();
	const scale = Math.max(scaleMs, end - t0);
	const segs: Seg[] = [];
	const sorted = [...jobs].sort(
		(a, b) => Date.parse(a.started_at ?? a.queued_at) - Date.parse(b.started_at ?? b.queued_at),
	);
	for (const j of sorted) {
		const qs = Date.parse(j.queued_at);
		const st = j.started_at ? Date.parse(j.started_at) : null;
		const en = j.completed_at ? Date.parse(j.completed_at) : end;
		// queue wait between claim and start — hatched
		if (st != null && qs < st) {
			segs.push({
				cls: "q",
				left: ((qs - t0) / scale) * 100,
				width: Math.max(0.35, ((st - qs) / scale) * 100),
				label: `${j.name} queued ${dur(qs, st)}`,
			});
		}
		if (st == null) {
			// still queued — hatch the wait so far
			segs.push({
				cls: "q",
				left: Math.max(0, ((qs - t0) / scale) * 100),
				width: Math.max(0.35, ((en - qs) / scale) * 100),
				label: `${j.name} queued ${dur(qs, en)}`,
			});
			continue;
		}
		const live = j.status === "in_progress" || j.status === "waiting";
		const failed = j.conclusion === "failure" || j.status === "cancelled";
		const cls = failed ? "e" : live ? "r" : HUE_CLS[jobHue(j.job_key || j.name)];
		segs.push({
			cls,
			left: Math.max(0, ((st - t0) / scale) * 100),
			width: Math.max(0.35, ((en - st) / scale) * 100),
			label: `${j.name} ${failed ? "failed " : live ? "running " : ""}${dur(st, en)}`,
		});
	}
	if (segs.length === 0) {
		// run exists but claims no jobs yet — hatch its whole wait
		segs.push({
			cls: "q",
			left: 0,
			width: Math.max(2, ((end - t0) / scale) * 100),
			label: "queued",
		});
	}
	return { segs, scaleMs: scale };
}

function dur(a: number, b: number): string {
	const ms = Math.max(0, b - a);
	if (ms < 1000) return `${ms}ms`;
	const s = Math.floor(ms / 1000);
	if (s < 60) return `${s}s`;
	return `${Math.floor(s / 60)}m ${s % 60}s`;
}

export function Fingerprint({
	run,
	jobs,
	small,
	tinted,
	className,
}: {
	run: Run;
	jobs: Job[] | undefined;
	small?: boolean;
	tinted?: boolean;
	className?: string;
}) {
	const { segs } = fingerprintSegs(jobs ?? [], run);
	return (
		<div
			className={cn("pbar", small && "sm", className)}
			title={segs.map((s) => s.label).join(" · ") || "no job timing"}
			style={!jobs ? { background: "var(--raise2)" } : undefined}
		>
			{jobs &&
				segs.map((s, i) => (
					<i
						key={i}
						className={`pb ${tinted ? (HUE_TINT[s.cls] ?? s.cls) : s.cls}`}
						style={{
							left: `${Math.min(99.6, s.left)}%`,
							width: `${Math.min(100 - s.left, s.width)}%`,
						}}
					/>
				))}
		</div>
	);
}

// useRunDetails fans out /runs/:id for visible rows. Completed runs are
// immutable so they cache forever; live runs re-poll on the list's cadence.
export function useRunDetails(runs: Run[] | undefined) {
	const list = runs ?? [];
	return useQueries({
		queries: list.map((r) => {
			const live = r.status === "in_progress" || r.status === "queued";
			return {
				queryKey: ["run", r.id],
				queryFn: () => api.get<RunDetail>(`/runs/${r.id}`),
				staleTime: live ? 0 : Number.POSITIVE_INFINITY,
				refetchInterval: live ? 4000 : false,
			};
		}),
	});
}
