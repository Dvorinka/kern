import { cn } from "./ui";

// One status vocabulary for runs, jobs and steps: GitHub's conclusion model
// plus the queued/running/waiting states. Rendered as a square-signal dot
// plus micro-label — the console's status glyph.
const CONF: Record<string, { label: string; color: string; soft: string; pulse?: boolean }> = {
	success: { label: "success", color: "var(--ok)", soft: "var(--ok-soft)" },
	completed: { label: "completed", color: "var(--ok)", soft: "var(--ok-soft)" },
	online: { label: "online", color: "var(--ok)", soft: "var(--ok-soft)" },
	failure: { label: "failure", color: "var(--err)", soft: "var(--err-soft)" },
	cancelled: { label: "cancelled", color: "var(--text-muted)", soft: "var(--bg-subtle)" },
	skipped: { label: "skipped", color: "var(--text-faint)", soft: "var(--bg-subtle)" },
	queued: { label: "queued", color: "var(--text-muted)", soft: "var(--bg-subtle)" },
	waiting: { label: "waiting", color: "var(--text-muted)", soft: "var(--bg-subtle)" },
	in_progress: { label: "running", color: "var(--run)", soft: "var(--run-soft)", pulse: true },
	running: { label: "running", color: "var(--run)", soft: "var(--run-soft)", pulse: true },
	offline: { label: "offline", color: "var(--text-faint)", soft: "var(--bg-subtle)" },
	draining: { label: "draining", color: "var(--warn)", soft: "var(--warn-soft)" },
};

export function StatusPill({
	status,
	conclusion,
	className,
}: {
	status: string;
	conclusion?: string;
	className?: string;
}) {
	const kind = conclusion && conclusion !== "" ? conclusion : status;
	const c = CONF[kind] ?? CONF.queued!;
	return (
		<span
			className={cn(
				"inline-flex items-center gap-1.5 rounded-[3px] border px-1.5 py-[3px] font-mono text-[10.5px] font-medium leading-none tracking-[0.02em]",
				className,
			)}
			style={{ color: c.color, background: c.soft, borderColor: "color-mix(in srgb, " + c.color + " 35%, transparent)" }}
		>
			<span
				className={cn("inline-block h-[6px] w-[6px] rounded-[1px]", c.pulse && "animate-pulse")}
				style={{ background: c.color }}
			/>
			{c.label}
		</span>
	);
}
