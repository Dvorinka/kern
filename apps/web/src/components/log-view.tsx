import { useEffect, useRef, useState } from "react";
import { cn } from "./ui";

// Live step log viewer. Streams via SSE while the step runs; falls back to a
// single read for finished steps. ANSI codes are stripped for display.
// Numbered mono lines, a block cursor on the live tail, Wrap/Raw/Follow.
interface LogChunk {
	offset: number;
	text?: string;
	status?: string;
	conclusion?: string;
	message?: string;
}

// oxlint-disable-next-line no-control-regex -- stripping ANSI escapes is the point
const ANSI = /\x1b\[[0-9;]*[A-Za-z]/g;

export function LogView({
	stepId,
	active,
	header,
}: {
	stepId: string;
	active: boolean;
	header?: string;
}) {
	const [text, setText] = useState("");
	const [err, setErr] = useState("");
	const [wrap, setWrap] = useState(true);
	const [follow, setFollow] = useState(true);
	const endRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		setText("");
		setErr("");
		let cancelled = false;
		let es: EventSource | null = null;

		const load = async () => {
			if (!active) {
				const res = await fetch(`/api/v1/steps/${stepId}/logs`, { credentials: "same-origin" });
				if (cancelled) return;
				if (res.ok) {
					const d = await res.json();
					setText(d.text ?? "");
				}
				return;
			}
			es = new EventSource(`/api/v1/steps/${stepId}/logs/stream`);
			es.addEventListener("log", (ev) => {
				if (cancelled) return;
				const chunk: LogChunk = JSON.parse((ev as MessageEvent).data);
				setText((t) => t + (chunk.text ?? ""));
			});
			es.addEventListener("end", () => {
				es?.close();
			});
			es.addEventListener("error", (ev) => {
				if (ev instanceof MessageEvent) {
					const chunk: LogChunk = JSON.parse(ev.data);
					setErr(chunk.message ?? "log read failed");
				}
			});
			es.onerror = () => {
				es?.close();
			};
		};
		void load();
		return () => {
			cancelled = true;
			es?.close();
		};
	}, [stepId, active]);

	useEffect(() => {
		if (follow) endRef.current?.scrollIntoView({ block: "end" });
	}, [text, follow]);

	const clean = text.replace(ANSI, "");
	const lines = clean ? clean.split("\n") : [];

	return (
		<div className="slog">
			<div className="lh">
				<b>{header ?? "log"}</b>
				{active ? "streaming" : "finished"}
				<span className="r">
					<button className={cn(wrap && "on")} onClick={() => setWrap((w) => !w)}>
						Wrap
					</button>
					<button className={cn(follow && "on")} onClick={() => setFollow((f) => !f)}>
						Follow
					</button>
				</span>
			</div>
			<div className={cn("log", !wrap && "raw")}>
				{lines.length === 0 && !err && (
					<div className="l">
						<span className="ln">·</span>
						<span className="lm">No log output yet.</span>
					</div>
				)}
				{lines.map((l, i) => (
					<div className="l" key={i}>
						<span className="ln">{i + 1}</span>
						<span className="lm">
							{l}
							{active && i === lines.length - 1 && <span className="cursor" />}
						</span>
					</div>
				))}
				{err && (
					<div className="l">
						<span className="ln">!</span>
						<span className="lm" style={{ color: "var(--err)" }}>
							{err}
						</span>
					</div>
				)}
				<div ref={endRef} />
			</div>
		</div>
	);
}
