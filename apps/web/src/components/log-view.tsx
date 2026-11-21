import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { cn } from "./ui";

// Live step log viewer. Streams via SSE while the step runs; falls back to a
// single read for finished steps. ANSI codes are stripped for display.
// Search filters to matching lines, the level chips filter by detected
// severity, and tokens (times, paths, flags, durations, results) carry the
// palette inside an otherwise monochrome stream.
interface LogChunk {
	offset: number;
	text?: string;
	status?: string;
	conclusion?: string;
	message?: string;
}

// Strip CSI, OSC (hyperlinks, clipboard writes), charset/single escapes and
// stray C0 controls — runner logs are attacker-adjacent text, not a terminal.
// oxlint-disable-next-line no-control-regex -- stripping ANSI escapes is the point
const ANSI = /\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-2]?|\x1b[=>M78DHc]|[\x00-\x08\x0b-\x1f\x7f]/g;

// jarvis: token scan is O(lines × one regex pass); fine to ~10k lines, beyond
// that the viewer stays usable but highlighting costs a frame on big pastes.
type Lvl = "err" | "warn" | "cmd" | "grp" | "dim" | "";

interface LLine {
	n: number;
	text: string;
	lvl: Lvl;
	g?: number;
}

const RE_ERR = /\b(error|failed|failure|fatal|panic|errno)\b|\bFAIL(?:ED)?\b|✗|✘|::error/i;
const RE_WARN = /\bwarn(?:ing)?s?\b|::warning/i;
const RE_CMD = /^\s*(\$|>|❯)\s|##\[command\]/;

function classify(raw: string): { text: string; lvl: Lvl } | null {
	const m = raw.match(/^##\[([a-z]+)\](.*)$/);
	if (m) {
		const kind = m[1]!;
		if (kind === "endgroup") return null;
		const text = (m[2] ?? "").trimStart();
		if (kind === "group") return { text, lvl: "grp" };
		if (kind === "error") return { text, lvl: "err" };
		if (kind === "warning" || kind === "notice") return { text, lvl: "warn" };
		if (kind === "debug" || kind === "command") return { text, lvl: kind === "command" ? "cmd" : "dim" };
		return { text, lvl: "" };
	}
	if (RE_ERR.test(raw)) return { text: raw, lvl: "err" };
	if (RE_WARN.test(raw)) return { text: raw, lvl: "warn" };
	if (RE_CMD.test(raw)) return { text: raw, lvl: "cmd" };
	return { text: raw, lvl: "" };
}

// One alternation over the interesting token shapes; the first capture group
// that fired names the tone. Ordered so paths/urls beat generic numbers.
const TOK =
	/(https?:\/\/[^\s"'>)]+)|((?:\/[\w.@~+-]+){2,}\/?)|(--?[a-zA-Z][\w-]*(?:=\S+)?)|(\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b|\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[^\s]*)|(✓|✔|\bok\b|\bpassed\b|\bsuccess(?:ful)?\b|\bPASSED\b)|(\[\d+\/\d+\]|\b\d+(?:\.\d+)?(?:ms|s|m|h|ns|µs)\b|\b\d+(?:\.\d+)?(?:KB|MB|GB|B)\b|\b\d+(?:\.\d+)?%)/g;

function toks(text: string): ReactNode[] {
	const out: ReactNode[] = [];
	let last = 0;
	let k = 0;
	TOK.lastIndex = 0;
	for (let m = TOK.exec(text); m; m = TOK.exec(text)) {
		if (m.index > last) out.push(text.slice(last, m.index));
		const cls = m[1] || m[2] || m[3] ? "tok-p" : m[4] ? "tok-t" : m[5] ? "tok-ok" : "tok-n";
		out.push(
			<span key={k++} className={cls}>
				{m[0]}
			</span>,
		);
		last = m.index + m[0].length;
	}
	if (last < text.length) out.push(text.slice(last));
	return out;
}

// Search matches get the selection yellow — the only place the colour means
// "you picked this" everywhere else too.
function withHits(text: string, nodes: ReactNode[], q: string): ReactNode[] {
	if (!q) return nodes;
	const low = text.toLowerCase();
	const ql = q.toLowerCase();
	if (!low.includes(ql)) return nodes;
	// token boundaries don't matter to a reader — re-run over the plain text
	const out: ReactNode[] = [];
	let last = 0;
	let k = 0;
	for (let i = low.indexOf(ql, last); i >= 0; i = low.indexOf(ql, last)) {
		if (i > last) out.push(text.slice(last, i));
		out.push(
			<span key={k++} className="lhit">
				{text.slice(i, i + ql.length)}
			</span>,
		);
		last = i + ql.length;
	}
	out.push(text.slice(last));
	return out;
}

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
	const [lines, setLines] = useState<LLine[]>([]);
	const [partial, setPartial] = useState("");
	const [collapsed, setCollapsed] = useState<Set<number>>(new Set());
	const [err, setErr] = useState("");
	const [trunc, setTrunc] = useState<{ next: number; size: number } | null>(null);
	const [wrap, setWrap] = useState(true);
	const [follow, setFollow] = useState(true);
	const [q, setQ] = useState("");
	const [lvl, setLvl] = useState<"" | "err" | "warn" | "cmd">("");
	const [selLine, setSelLine] = useState<number | null>(null);
	const [copied, setCopied] = useState(false);
	const logRef = useRef<HTMLDivElement>(null);
	const [away, setAway] = useState(false);

	// Incremental parse: each appended chunk classifies only its own
	// completed lines — the old useMemo re-stripped ANSI and re-classified
	// the entire accumulated log on every SSE chunk (O(N²) on long logs).
	const pendingRef = useRef("");
	// Open ##[group] stack — ##[endgroup] lines are dropped by classify, so
	// membership is tracked here on the raw text.
	const grpRef = useRef<number[]>([]);
	const appendText = (chunk: string) => {
		if (!chunk) return;
		const joined = pendingRef.current + chunk.replace(ANSI, "");
		const parts = joined.split("\n");
		const pending = parts.pop() ?? "";
		pendingRef.current = pending;
		setPartial(pending);
		if (parts.length === 0) return;
		setLines((prev) => {
			const out = prev.slice();
			for (const raw of parts) {
				if (raw.startsWith("##[endgroup]")) {
					grpRef.current.pop();
					continue;
				}
				const c = classify(raw);
				if (c) {
					const n = out.length + 1;
					out.push({ n, g: grpRef.current[grpRef.current.length - 1] ?? 0, ...c });
					if (raw.startsWith("##[group]")) grpRef.current.push(n);
				}
			}
			return out;
		});
	};
	const flushPending = () => {
		const p = pendingRef.current;
		pendingRef.current = "";
		setPartial("");
		if (!p) return;
		setLines((prev) => {
			const c = classify(p);
			return c ? [...prev, { n: prev.length + 1, ...c }] : prev;
		});
	};

	useEffect(() => {
		setText("");
		setLines([]);
		setPartial("");
		setCollapsed(new Set());
		pendingRef.current = "";
		grpRef.current = [];
		setErr("");
		setTrunc(null);
		setQ("");
		setLvl("");
		setSelLine(null);
		let cancelled = false;
		let es: EventSource | null = null;

		const load = async () => {
			if (!active) {
				const res = await fetch(`/api/v1/steps/${stepId}/logs`, { credentials: "same-origin" });
				if (cancelled) return;
				if (res.ok) {
					const d = await res.json();
					setText(d.text ?? "");
					appendText(d.text ?? "");
					flushPending();
					// The endpoint caps at 256 KiB — flag truncation and offer
					// paged continuation instead of silently dropping the rest.
					if ((d.size ?? 0) > (d.next_offset ?? 0)) {
						setTrunc({ next: d.next_offset, size: d.size });
					}
				} else {
					setErr(`log load failed (${res.status})`);
				}
				return;
			}
			let retries = 0;
			let ended = false;
			const open = (offset: number) => {
				let lastOff = offset;
				es = new EventSource(`/api/v1/steps/${stepId}/logs/stream?offset=${offset}`);
				const chunk = (ev: MessageEvent): LogChunk | null => {
					try {
						return JSON.parse(ev.data);
					} catch {
						return null;
					}
				};
				es.addEventListener("log", (ev) => {
					if (cancelled) return;
					const c = chunk(ev as MessageEvent);
					if (!c) return;
					lastOff = c.offset + new TextEncoder().encode(c.text ?? "").length;
					setText((t) => t + (c.text ?? ""));
					appendText(c.text ?? "");
					retries = 0;
				});
				es.addEventListener("end", () => {
					ended = true;
					flushPending();
					es?.close();
				});
				// Server-sent error events are application errors — show them;
				// transport errors are transient and reconnect from lastOff.
				es.addEventListener("error", (ev) => {
					if (ev instanceof MessageEvent) {
						const c = chunk(ev);
						setErr(c?.message ?? "log read failed");
						es?.close();
					}
				});
				es.onerror = () => {
					es?.close();
					if (cancelled || ended) return;
					const delay = Math.min(8000, 500 * 2 ** retries++);
					setTimeout(() => {
						if (!cancelled && !ended) open(lastOff);
					}, delay);
				};
			};
			open(0);
		};
		void load();
		return () => {
			cancelled = true;
			es?.close();
		};
	}, [stepId, active]);

	const parsed = lines;

	const counts = useMemo(() => {
		const c = { err: 0, warn: 0, cmd: 0 };
		for (const l of parsed) {
			if (l.lvl === "err") c.err++;
			else if (l.lvl === "warn") c.warn++;
			else if (l.lvl === "cmd") c.cmd++;
		}
		return c;
	}, [parsed]);

	const filtering = q.trim() !== "" || lvl !== "";
	const shown = useMemo(() => {
		const needle = q.trim().toLowerCase();
		const filtering = !!needle || !!lvl;
		// Collapsed ##[group] bodies hide unless a filter is active.
		const hidden = (l: LLine): boolean => {
			if (filtering || collapsed.size === 0) return false;
			let g = l.g ?? 0;
			let guard = 0;
			while (g && guard++ < 64) {
				if (collapsed.has(g)) return true;
				g = parsed[g - 1]?.g ?? 0;
			}
			return false;
		};
		return parsed.filter(
			(l) =>
				!hidden(l) &&
				(!lvl || l.lvl === lvl) &&
				(!needle || l.text.toLowerCase().includes(needle)),
		);
	}, [parsed, q, lvl, collapsed]);

	useEffect(() => {
		const el = logRef.current;
		if (follow && !filtering && el) el.scrollTop = el.scrollHeight;
	}, [text, follow, filtering]);

	// #L<n> deep links: once lines land, jump to the hashed row and mark it.
	const didHashRef = useRef(false);
	useEffect(() => {
		if (didHashRef.current || parsed.length === 0) return;
		const m = location.hash.match(/^#L(\d+)$/);
		if (!m) return;
		const n = Number(m[1]);
		didHashRef.current = true;
		setSelLine(n);
		setFollow(false);
		logRef.current?.querySelector(`#L${CSS.escape(String(n))}`)?.scrollIntoView();
	}, [parsed]);

	const copyAll = async () => {
		try {
			await navigator.clipboard.writeText(text.replace(ANSI, ""));
			setCopied(true);
			setTimeout(() => setCopied(false), 1400);
		} catch {
			/* clipboard may be unavailable — the button simply stays quiet */
		}
	};

	const download = () => {
		const blob = new Blob([text.replace(ANSI, "")], { type: "text/plain" });
		const a = document.createElement("a");
		a.href = URL.createObjectURL(blob);
		a.download = `${(header ?? "step").replace(/[^\w.-]+/g, "_")}.log`;
		a.click();
		URL.revokeObjectURL(a.href);
	};

	// Page forward through a truncated finished-step log — each click
	// appends the next server-side window until the log is complete.
	const loadMore = async () => {
		if (!trunc) return;
		const res = await fetch(`/api/v1/steps/${stepId}/logs?offset=${trunc.next}`, {
			credentials: "same-origin",
		});
		if (!res.ok) {
			setErr(`log load failed (${res.status})`);
			return;
		}
		const d = await res.json();
		setText((t) => t + (d.text ?? ""));
		appendText(d.text ?? "");
		setTrunc(
			(d.size ?? 0) > (d.next_offset ?? 0)
				? { next: d.next_offset, size: d.size }
				: null,
		);
	};

	return (
		<div className="slog">
			<div className="lh">
				<b>{header ?? "log"}</b>
				{active ? (
					<span className="ltag2">live</span>
				) : (
					<span style={{ color: "#777" }}>finished</span>
				)}
				<span style={{ color: "#555" }}>
					{parsed.length} lines{counts.err ? ` · ${counts.err} errors` : ""}
				</span>
				<span className="r" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
					<button className={cn("lchip", lvl === "" && "on")} onClick={() => setLvl("")}>
						All
					</button>
					{counts.err > 0 && (
						<button className={cn("lchip", lvl === "err" && "on")} onClick={() => setLvl(lvl === "err" ? "" : "err")}>
							<i className="e-dot" />
							Errors <span className="c">{counts.err}</span>
						</button>
					)}
					{counts.warn > 0 && (
						<button className={cn("lchip", lvl === "warn" && "on")} onClick={() => setLvl(lvl === "warn" ? "" : "warn")}>
							<i className="w-dot" />
							Warn <span className="c">{counts.warn}</span>
						</button>
					)}
					{counts.cmd > 0 && (
						<button className={cn("lchip", lvl === "cmd" && "on")} onClick={() => setLvl(lvl === "cmd" ? "" : "cmd")}>
							<i className="c-dot" />
							Cmds <span className="c">{counts.cmd}</span>
						</button>
					)}
					<label className="lsrch">
						<svg width="10" height="10" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
							<circle cx="11" cy="11" r="7" />
							<path d="M21 21l-4-4" />
						</svg>
						<input
							value={q}
							onChange={(e) => setQ(e.target.value)}
							placeholder="filter…"
							aria-label="Filter log lines"
						/>
						{q && <span className="n">{shown.length}</span>}
					</label>
					<button onClick={copyAll}>{copied ? "Copied" : "Copy"}</button>
					<button onClick={download}>Save</button>
					<button className={cn(wrap && "on")} onClick={() => setWrap((w) => !w)}>
						Wrap
					</button>
					<button className={cn(follow && !filtering && "on")} onClick={() => setFollow((f) => !f)}>
						Follow
					</button>
				</span>
			</div>
			{trunc && (
				<div className="ltrunc" style={{ padding: "4px 10px", color: "#a80", fontSize: 12 }}>
					log truncated — showing {trunc.next.toLocaleString()} of{" "}
					{trunc.size.toLocaleString()} bytes{" "}
					<button onClick={loadMore} style={{ marginLeft: 8 }}>
						load more
					</button>
				</div>
			)}
			<div
				className={cn("log", !wrap && "raw")}
				ref={logRef}
				onScroll={(e) => {
					const el = e.currentTarget;
					const d = el.scrollHeight - el.scrollTop - el.clientHeight;
					setAway(d > 80);
					setFollow(d <= 80);
				}}
			>
				{parsed.length === 0 && !err && (
					<div className="l">
						<span className="lnum">·</span>
						<span className="lm">No log output yet.</span>
					</div>
				)}
				{parsed.length > 0 && shown.length === 0 && (
					<div className="l">
						<span className="lnum">·</span>
						<span className="lm">No lines match.</span>
					</div>
				)}
				{shown.map((l) => {
					const nodes = l.lvl === "err" || l.lvl === "warn" ? [l.text] : toks(l.text);
					return (
						<div
							id={`L${l.n}`}
							className={cn("l", l.lvl && `l-l${l.lvl === "grp" ? "g" : l.lvl === "err" ? "e" : l.lvl === "warn" ? "w" : l.lvl === "cmd" ? "c" : "d"}`, active && l.n === parsed[parsed.length - 1]?.n && "l-lv", selLine === l.n && "l-sel")}
							key={l.n}
						>
							<span
								className="lnum"
								onClick={() => {
									setSelLine(selLine === l.n ? null : l.n);
									history.replaceState(null, "", `#L${l.n}`);
								}}
							>
								{l.n}
							</span>
							<span
								className="lm"
								style={l.lvl === "grp" ? { cursor: "pointer" } : undefined}
								onClick={
									l.lvl === "grp"
										? () =>
												setCollapsed((s) => {
													const n = new Set(s);
													if (n.has(l.n)) n.delete(l.n); else n.add(l.n);
													return n;
												})
										: undefined
								}
							>
								{l.lvl === "grp" && (collapsed.has(l.n) ? "▸ " : "▾ ")}
								{withHits(l.text, nodes, q.trim())}
								{active && l.n === parsed[parsed.length - 1]?.n && <span className="cursor" />}
							</span>
						</div>
					);
				})}
				{partial && (() => {
					const c = classify(partial);
					if (!c) return null;
					const n = parsed.length + 1;
					if (filtering && !partial.toLowerCase().includes(q.trim().toLowerCase()) && q.trim()) return null;
					return (
						<div className={cn("l", c.lvl && `l-l${c.lvl === "grp" ? "g" : c.lvl === "err" ? "e" : c.lvl === "warn" ? "w" : c.lvl === "cmd" ? "c" : "d"}`, active && "l-lv", selLine === n && "l-sel")}>
							<span className="lnum" onClick={() => setSelLine(selLine === n ? null : n)}>
								{n}
							</span>
							<span className="lm">
								{withHits(c.text, toks(c.text), q.trim())}
								{active && <span className="cursor" />}
							</span>
						</div>
					);
				})()}
				{err && (
					<div className="l">
						<span className="lnum">!</span>
						<span className="lm" style={{ color: "#ff5a47" }}>
							{err}
						</span>
					</div>
				)}
				{away && !filtering && (
					<button
						className="jump"
						onClick={() => {
							setFollow(true);
							const el = logRef.current;
							if (el) el.scrollTop = el.scrollHeight;
						}}
					>
						↓ latest
					</button>
				)}
			</div>
		</div>
	);
}
