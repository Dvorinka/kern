import { useEffect, useRef, useState } from "react";

// Live step log viewer. Streams via SSE while the step runs; falls back to a
// single read for finished steps. ANSI codes are stripped for display.
interface LogChunk {
	offset: number;
	text?: string;
	status?: string;
	conclusion?: string;
	message?: string;
}

const ANSI = /\x1b\[[0-9;]*[A-Za-z]/g;

export function LogView({ stepId, active }: { stepId: string; active: boolean }) {
	const [text, setText] = useState("");
	const [err, setErr] = useState("");
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
		endRef.current?.scrollIntoView({ block: "end" });
	}, [text]);

	return (
		<pre className="font-mono max-h-96 overflow-auto rounded-md bg-[var(--bg)] p-3 text-xs leading-5 whitespace-pre-wrap break-all">
			{text.replace(ANSI, "") || <span className="text-[var(--text-faint)]">No log output yet.</span>}
			{err && <span className="text-[var(--err)]">{err}</span>}
			<div ref={endRef} />
		</pre>
	);
}
