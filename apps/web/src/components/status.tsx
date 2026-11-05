import { cn } from "./ui";

// One status vocabulary for runs, jobs and steps: GitHub's conclusion model
// plus queued/running/waiting. Rendered as an 8px status square — the kernel
// mark doubled is the running state. Colour lives on the square only.
const CONF: Record<string, { label: string; cls: string }> = {
	success: { label: "success", cls: "ok" },
	completed: { label: "completed", cls: "ok" },
	online: { label: "online", cls: "ok" },
	failure: { label: "failure", cls: "bad" },
	cancelled: { label: "cancelled", cls: "dim" },
	skipped: { label: "skipped", cls: "dim" },
	queued: { label: "queued", cls: "dim" },
	waiting: { label: "waiting", cls: "dim" },
	waiting_approval: { label: "approval", cls: "warn" },
	awaiting_approval: { label: "awaiting approval", cls: "warn" },
	in_progress: { label: "running", cls: "live" },
	running: { label: "running", cls: "live" },
	offline: { label: "offline", cls: "dim" },
	draining: { label: "draining", cls: "warn" },
	received: { label: "received", cls: "warn" },
	processed: { label: "processed", cls: "ok" },
	ignored: { label: "ignored", cls: "dim" },
	failed: { label: "failed", cls: "bad" },
};

export function statusClass(status: string, conclusion?: string): string {
	const kind = conclusion && conclusion !== "" ? conclusion : status;
	return (CONF[kind] ?? CONF.queued!).cls;
}

export function statusLabel(status: string, conclusion?: string): string {
	const kind = conclusion && conclusion !== "" ? conclusion : status;
	return (CONF[kind] ?? CONF.queued!).label;
}

// Bare status square — rows, lanes, anywhere the glyph alone carries meaning.
export function StatusSq({
	status,
	conclusion,
	className,
}: {
	status: string;
	conclusion?: string;
	className?: string;
}) {
	return <span className={cn("sq", statusClass(status, conclusion), className)} />;
}

export function StatusPill({
	status,
	conclusion,
	className,
}: {
	status: string;
	conclusion?: string;
	className?: string;
}) {
	const cls = statusClass(status, conclusion);
	return (
		<span
			className={cn(
				"tag",
				cls === "ok" && "ok",
				cls === "bad" && "err",
				cls === "warn" && "warn",
				className,
			)}
		>
			<span className={cn("sq", cls)} style={{ width: 7, height: 7 }} />
			{statusLabel(status, conclusion)}
		</span>
	);
}
