import { CheckCircle2, XCircle, CircleDashed, Loader2, MinusCircle, Ban } from "lucide-react";
import { cn } from "./ui";

// One status vocabulary for runs, jobs and steps: GitHub's conclusion model
// plus the queued/running/waiting states.
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
	const conf: Record<string, { label: string; cls: string; Icon: typeof CheckCircle2; spin?: boolean }> = {
		success: { label: "success", cls: "text-[var(--ok)]", Icon: CheckCircle2 },
		failure: { label: "failure", cls: "text-[var(--err)]", Icon: XCircle },
		cancelled: { label: "cancelled", cls: "text-[var(--text-muted)]", Icon: Ban },
		skipped: { label: "skipped", cls: "text-[var(--text-faint)]", Icon: MinusCircle },
		queued: { label: "queued", cls: "text-[var(--text-muted)]", Icon: CircleDashed },
		waiting: { label: "waiting", cls: "text-[var(--text-muted)]", Icon: CircleDashed },
		in_progress: { label: "running", cls: "text-[var(--run)]", Icon: Loader2, spin: true },
		running: { label: "running", cls: "text-[var(--run)]", Icon: Loader2, spin: true },
		completed: { label: "completed", cls: "text-[var(--ok)]", Icon: CheckCircle2 },
		online: { label: "online", cls: "text-[var(--ok)]", Icon: CheckCircle2 },
		offline: { label: "offline", cls: "text-[var(--text-faint)]", Icon: MinusCircle },
		draining: { label: "draining", cls: "text-[var(--warn)]", Icon: CircleDashed },
	};
	const c = conf[kind] ?? conf.queued!;
	return (
		<span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", c.cls, className)}>
			<c.Icon size={14} className={cn(c.spin && "animate-spin")} strokeWidth={2} />
			{c.label}
		</span>
	);
}
