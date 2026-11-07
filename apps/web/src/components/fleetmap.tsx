import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Minus, Plus, RotateCcw } from "lucide-react";
import type { FleetMapPin, FleetMapResponse } from "../lib/types";
import { WORLD_DOTS } from "../lib/world-dots";
import { jobHue } from "./fingerprint";

// Dot-matrix world, rasterized from Natural Earth 110m land — the mockup's
// map verbatim. Equirectangular, cropped above 83°N and below 58°S.
const MW = 1200;
const MH = 470;
const LAT_T = 83;
const LAT_B = -58;
const VB = { x: 0, y: 0, w: MW, h: MH };
const ASPECT = MH / MW;

const LON = (l: number) => ((l + 180) / 360) * MW;
const LAT = (l: number) => ((LAT_T - l) / (LAT_T - LAT_B)) * MH;

// one <circle> per "1" cell — the same dot grid the mockup renders
const DOTS: { x: number; y: number }[] = [];
{
	const cols = WORLD_DOTS[0]!.length;
	const rows = WORLD_DOTS.length;
	const sp = MW / cols;
	const rp = MH / rows;
	WORLD_DOTS.forEach((row, ri) => {
		for (let c = 0; c < row.length; c++) {
			if (row[c] === "1") DOTS.push({ x: (c + 0.5) * sp, y: (ri + 0.5) * rp });
		}
	});
}

interface View {
	x: number;
	y: number;
	w: number;
	h: number;
}

export interface Site {
	key: string;
	x: number;
	y: number;
	runners: FleetMapPin[];
	jobs: { runner_id: string; key: string; name: string }[];
	/** live = a job is running here, idle = reachable, off = every runner down */
	st: "live" | "idle" | "off";
	city: string;
	code: string;
}

// short site code like the mockup's hel/osl/fra — first three of the city
const siteCode = (city: string, fallback: string) =>
	(city.trim() || fallback).replace(/\s.*/, "").slice(0, 3).toLowerCase();

// runners sharing one egress resolve to one point — group them into a site.
// Exported so the overview can draw the same sites as chips under the map.
export function fleetSites(runners: FleetMapPin[]): Site[] {
	const cells = new Map<string, FleetMapPin[]>();
	for (const r of runners) {
		if (r.lat == null || r.lng == null) continue;
		const k = `${r.lat.toFixed(1)}|${r.lng.toFixed(1)}`;
		(cells.get(k) ?? cells.set(k, []).get(k)!).push(r);
	}
	return [...cells.entries()].map(([key, rs]) => {
		const lat = rs.reduce((a, r) => a + r.lat!, 0) / rs.length;
		const lng = rs.reduce((a, r) => a + r.lng!, 0) / rs.length;
		const jobs = rs.flatMap((r) => r.jobs);
		const st: Site["st"] = jobs.length
			? "live"
			: rs.some((r) => r.status === "online" || r.status === "draining")
				? "idle"
				: "off";
		return {
			key,
			x: LON(lng),
			y: LAT(lat),
			runners: rs,
			jobs,
			st,
			city: rs.find((r) => r.city)?.city ?? rs[0]!.name,
			code: siteCode(rs.find((r) => r.city)?.city ?? rs[0]!.name, rs[0]!.name),
		};
	});
}

export function FleetMap({ data }: { data: FleetMapResponse }) {
	const nav = useNavigate();
	const svgRef = useRef<SVGSVGElement>(null);
	const wrapRef = useRef<HTMLDivElement>(null);
	const [view, setView] = useState<View>({ ...VB });
	const [hover, setHover] = useState<string | null>(null);
	const [tip, setTip] = useState<{ x: number; y: number } | null>(null);
	const drag = useRef<{ px: number; py: number; vx: number; vy: number; moved: boolean } | null>(null);
	const suppressClick = useRef(false);

	const control = data.control;
	const sites = useMemo<Site[]>(() => fleetSites(data.runners), [data.runners]);
	const unlocated = data.runners.length - sites.reduce((a, s) => a + s.runners.length, 0);

	const hx = control ? LON(control.lng) : 0;
	const hy = control ? LAT(control.lat) : 0;

	// client px → svg user units inside the current viewBox
	const toSvg = useCallback(
		(cx: number, cy: number): [number, number] => {
			const rect = svgRef.current?.getBoundingClientRect();
			if (!rect) return [0, 0];
			const v = view;
			return [v.x + ((cx - rect.left) / rect.width) * v.w, v.y + ((cy - rect.top) / rect.height) * v.h];
		},
		[view],
	);

	// wheel zoom needs a non-passive listener — React's onWheel can't
	// preventDefault reliably.
	useEffect(() => {
		const el = svgRef.current;
		if (!el) return;
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			const [mx, my] = toSvg(e.clientX, e.clientY);
			setView((v) => {
				const f = e.deltaY > 0 ? 1.18 : 1 / 1.18;
				const w = Math.min(1600, Math.max(260, v.w * f));
				const h = w * ASPECT;
				const k = w / v.w;
				return { w, h, x: mx - (mx - v.x) * k, y: my - (my - v.y) * k };
			});
		};
		el.addEventListener("wheel", onWheel, { passive: false });
		return () => el.removeEventListener("wheel", onWheel);
	}, [toSvg]);

	const zoomBy = (f: number) =>
		setView((v) => {
			const cx = v.x + v.w / 2;
			const cy = v.y + v.h / 2;
			const w = Math.min(1600, Math.max(260, v.w * f));
			const h = w * ASPECT;
			const k = w / v.w;
			return { w, h, x: cx - (cx - v.x) * k, y: cy - (cy - v.y) * k };
		});

	const hovered = hover ? sites.find((s) => s.key === hover) : null;

	// label side: right of the marker unless that runs off the edge
	const labelFor = (s: Site) => {
		const left = s.x > MW * 0.7;
		return { x: s.x + (left ? -16 : 16), y: s.y + 6, an: left ? "end" : "start" };
	};

	return (
		<div className="fleetmap" ref={wrapRef}>
			<svg
				ref={svgRef}
				viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
				role="img"
				aria-label="Fleet world map — scroll to zoom, drag to pan, click a site"
				className={drag.current?.moved ? "fm-dragging" : ""}
				onPointerDown={(e) => {
					drag.current = { px: e.clientX, py: e.clientY, vx: view.x, vy: view.y, moved: false };
					(e.target as Element).setPointerCapture?.(e.pointerId);
				}}
				onPointerMove={(e) => {
					const d = drag.current;
					if (d) {
						const rect = svgRef.current?.getBoundingClientRect();
						if (rect) {
							const dx = ((e.clientX - d.px) / rect.width) * view.w;
							const dy = ((e.clientY - d.py) / rect.height) * view.h;
							if (Math.abs(e.clientX - d.px) + Math.abs(e.clientY - d.py) > 4) d.moved = true;
							setView((v) => ({ ...v, x: d.vx - dx, y: d.vy - dy }));
						}
						return;
					}
					const rect = wrapRef.current?.getBoundingClientRect();
					if (rect) {
						setTip({
							x: Math.min(86, Math.max(2, ((e.clientX - rect.left) / rect.width) * 100)),
							y: Math.min(88, Math.max(2, ((e.clientY - rect.top) / rect.height) * 100)),
						});
					}
				}}
				onPointerUp={() => {
					if (drag.current?.moved) {
						suppressClick.current = true;
						setHover(null);
					}
					drag.current = null;
				}}
				onPointerLeave={() => {
					drag.current = null;
					setHover(null);
					setTip(null);
				}}
			>
				{/* the land dot-matrix */}
				{DOTS.map((d, i) => (
					<circle key={i} cx={d.x.toFixed(1)} cy={d.y.toFixed(1)} r="3" className="fm-dot" />
				))}
				{/* logo-fan arcs to the hub — one per live job, one dashed per quiet site */}
				{control &&
					sites.map((s) => {
						const n = Math.max(1, s.jobs.length);
						return Array.from({ length: n }, (_, i) => {
							// fan coincident job arcs apart, alternating sides
							const bow = i === 0 ? 0 : i % 2 ? (i + 1) * 10 : -i * 10;
							const mx = (s.x + hx) / 2;
							const my = Math.min(s.y, hy) - Math.abs(hx - s.x) * 0.3 - 14 + bow;
							const live = i < s.jobs.length;
							return (
								<path
									key={`${s.key}-${i}`}
									d={`M${s.x.toFixed(1)} ${s.y.toFixed(1)}Q${mx.toFixed(1)} ${my.toFixed(1)} ${hx.toFixed(1)} ${hy.toFixed(1)}`}
									className={live ? "fm-arc" : "fm-arc fm-arc-idle"}
									strokeOpacity={live ? 0.75 : 0.4}
								/>
							);
						});
					})}
				{/* the hub — the rotated square, always at the control plane */}
				{control && (
					<g>
						<rect
							x={hx - 7}
							y={hy - 7}
							width="14"
							height="14"
							rx="4"
							className="fm-hub"
							transform={`rotate(45 ${hx} ${hy})`}
						/>
						<text x={hx - 16} y={hy + 6} textAnchor="end" className="fm-label">
							{siteCode(control.city, "hub")}
						</text>
					</g>
				)}
				{/* site markers */}
				{sites.map((s) => {
					const l = labelFor(s);
					const isHover = hover === s.key;
					return (
						<g
							key={s.key}
							className={isHover ? "fm-site-hi" : ""}
							onPointerEnter={(e) => {
								e.stopPropagation();
								setHover(s.key);
							}}
							onPointerLeave={() => setHover((h) => (h === s.key ? null : h))}
							onClick={(e) => {
								e.stopPropagation();
								if (suppressClick.current) {
									suppressClick.current = false;
									return;
								}
								nav("/runners");
							}}
							style={{ cursor: "pointer" }}
						>
							<rect x={s.x - 14} y={s.y - 14} width="28" height="28" className="fm-hit" />
							{s.st === "live" && (
								<>
									<circle className="halosvg" cx={s.x} cy={s.y} r="8" fill="var(--live)" />
									<circle cx={s.x} cy={s.y} r="8" fill="var(--paper)" stroke="var(--live)" strokeWidth="4" />
								</>
							)}
							{s.st === "idle" && (
								<circle cx={s.x} cy={s.y} r="7" fill="var(--paper)" stroke="var(--ink)" strokeWidth="3" />
							)}
							{s.st === "off" && (
								<circle
									cx={s.x}
									cy={s.y}
									r="7"
									fill="var(--paper)"
									stroke="var(--faint)"
									strokeWidth="2"
									strokeDasharray="3 3"
								/>
							)}
							{s.runners.length > 1 && (
								<text x={s.x} y={s.y + 22} textAnchor="middle" className="fm-count">
									×{s.runners.length}
								</text>
							)}
							<text x={l.x} y={l.y} textAnchor={l.an as "start" | "end"} className="fm-label">
								{s.code}
							</text>
						</g>
					);
				})}
			</svg>
			<div className="fm-zoom">
				<button onClick={() => zoomBy(1 / 1.4)} title="Zoom out" aria-label="Zoom out">
					<Minus size={12} />
				</button>
				<button onClick={() => zoomBy(1.4)} title="Zoom in" aria-label="Zoom in">
					<Plus size={12} />
				</button>
				<button onClick={() => setView({ ...VB })} title="Reset view" aria-label="Reset view">
					<RotateCcw size={12} />
				</button>
			</div>
			{hovered && tip && (
				<div className="fm-tip" style={{ left: `${tip.x}%`, top: `${tip.y}%` }}>
					<div className="fm-tip-name">{hovered.city}</div>
					<div className="fm-tip-sub">
						{hovered.runners.map((r) => r.name).join(", ")} ·{" "}
						{hovered.st === "live" ? `${hovered.jobs.length} live job${hovered.jobs.length === 1 ? "" : "s"}` : hovered.st}
					</div>
					{hovered.jobs.length > 0 && (
						<div className="fm-tip-jobs">
							{hovered.jobs.slice(0, 4).map((j, i) => (
								<div key={i}>
									<i className={`jc-swatch j-${jobHue(j.key || j.name)}`} />
									{j.name}
								</div>
							))}
							{hovered.jobs.length > 4 && <div>+{hovered.jobs.length - 4} more</div>}
						</div>
					)}
					<div className="fm-tip-hint">click for runners →</div>
				</div>
			)}
			{unlocated > 0 && (
				<span className="fm-note">
					{unlocated} runner{unlocated === 1 ? "" : "s"} without a location — resolves on
					next heartbeat or edit manually on the runners page
				</span>
			)}
		</div>
	);
}
