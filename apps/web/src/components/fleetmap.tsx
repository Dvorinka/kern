import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Minus, Plus, RotateCcw } from "lucide-react";
import type { FleetMapResponse } from "../lib/types";
import { WORLD_GRATICULE_PATH, WORLD_LAND_PATH } from "../lib/world-land";
import { jobHue } from "./fingerprint";

// Equirectangular map on the 0 0 1000 500 asset; the viewBox crops the poles
// (lat > ~74 and < ~-65) so the panel stays compact.
const W = 1000;
const H = 500;
const VB = { x: 0, y: 46, w: 1000, h: 384 };
const ASPECT = VB.h / VB.w;

interface View {
	x: number;
	y: number;
	w: number;
	h: number;
}

function project(lat: number, lng: number): [number, number] {
	return [((lng + 180) / 360) * W, ((90 - lat) / 180) * H];
}

// slerp follows the great circle between two lng/lat pairs — the honest arc.
function slerpPoint(
	from: [number, number],
	to: [number, number],
	t: number,
	bow: number,
): [number, number] {
	const rad = Math.PI / 180;
	const vec = (ll: [number, number]): [number, number, number] => {
		const lam = ll[0] * rad;
		const phi = ll[1] * rad;
		return [Math.cos(phi) * Math.cos(lam), Math.cos(phi) * Math.sin(lam), Math.sin(phi)];
	};
	const a = vec(from);
	const b = vec(to);
	const dot = Math.min(1, Math.max(-1, a[0] * b[0] + a[1] * b[1] + a[2] * b[2]));
	const theta = Math.acos(dot);
	if (theta < 1e-4) return to;
	const s1 = Math.sin((1 - t) * theta) / Math.sin(theta);
	const s2 = Math.sin(t * theta) / Math.sin(theta);
	const x = s1 * a[0] + s2 * b[0];
	const y = s1 * a[1] + s2 * b[1];
	const z = s1 * a[2] + s2 * b[2];
	let lng = Math.atan2(y, x) / rad;
	const lat = Math.atan2(z, Math.hypot(x, y)) / rad + bow * Math.sin(Math.PI * t);
	if (lng > 180) lng -= 360;
	if (lng < -180) lng += 360;
	return [lng, lat];
}

// arcPath samples the great circle; bow fans coincident control→runner arcs
// apart so a second live job to the same runner reads as its own line.
function arcPath(from: [number, number], to: [number, number], bow: number): string {
	let d = "";
	for (let i = 0; i <= 48; i++) {
		const [lng, lat] = slerpPoint(from, to, i / 48, bow);
		const [x, y] = project(lat, lng);
		d += `${i === 0 ? "M" : "L"}${x.toFixed(1)} ${y.toFixed(1)}`;
	}
	return d;
}

const HUE_VAR: Record<string, string> = {
	lint: "var(--lint)",
	build: "var(--build)",
	web: "var(--web)",
};

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
	const pins = data.runners.filter((r) => r.lat != null && r.lng != null);
	const unlocated = data.runners.length - pins.length;
	// runners sharing one egress resolve to one point — fan them into a ring
	const cells = new Map<string, number>();
	const placed = pins.map((r) => {
		const cell = `${r.lat!.toFixed(1)}|${r.lng!.toFixed(1)}`;
		const n = cells.get(cell) ?? 0;
		cells.set(cell, n + 1);
		const [x, y] = project(r.lat!, r.lng!);
		return { r, x, y, slot: n };
	});
	const totals = new Map<string, number>();
	for (const p of placed) {
		const k = `${p.r.lat!.toFixed(1)}|${p.r.lng!.toFixed(1)}`;
		totals.set(k, (totals.get(k) ?? 0) + 1);
	}

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
				const w = Math.min(1600, Math.max(220, v.w * f));
				const h = w * ASPECT;
				// keep the cursor-anchored point stationary
				const k = w / v.w;
				return {
					w,
					h,
					x: mx - (mx - v.x) * k,
					y: my - (my - v.y) * k,
				};
			});
		};
		el.addEventListener("wheel", onWheel, { passive: false });
		return () => el.removeEventListener("wheel", onWheel);
	}, [toSvg]);

	const zoomBy = (f: number) =>
		setView((v) => {
			const cx = v.x + v.w / 2;
			const cy = v.y + v.h / 2;
			const w = Math.min(1600, Math.max(220, v.w * f));
			const h = w * ASPECT;
			const k = w / v.w;
			return { w, h, x: cx - (cx - v.x) * k, y: cy - (cy - v.y) * k };
		});

	const hovered = hover ? data.runners.find((r) => r.id === hover) : null;

	return (
		<div className="fleetmap" ref={wrapRef}>
			<svg
				ref={svgRef}
				viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
				role="img"
				aria-label="Fleet world map — scroll to zoom, drag to pan, click a runner"
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
				<path className="fm-grat" d={WORLD_GRATICULE_PATH} />
				<path className="fm-land" d={WORLD_LAND_PATH} />
				{control &&
					placed.flatMap(({ r }) =>
						r.jobs.map((j, ji) => {
							const hue = HUE_VAR[jobHue(j.key || j.name)] ?? "var(--dim)";
							const bow = ji % 2 === 0 ? 3 + ji : -(3 + ji);
							const d = arcPath([control.lng, control.lat], [r.lng!, r.lat!], bow);
							const active = hover === r.id;
							return (
								<g key={`${r.id}-${j.name}-${ji}`} className={active ? "fm-arc-hi" : ""}>
									<path className="fm-arc" d={d} stroke={hue} />
									<circle r="1.6" fill={hue}>
										<animateMotion dur="2.8s" repeatCount="indefinite" path={d} />
									</circle>
								</g>
							);
						}),
					)}
				{control && (
					<g transform={`translate(${project(control.lat, control.lng).join(" ")})`}>
						<rect className="fm-ctl" x="-4" y="-4" width="8" height="8" />
						<rect className="fm-ctlcore" x="-1.5" y="-1.5" width="3" height="3" />
						<title>{`control plane — ${control.city || "unknown city"}, ${control.country || ""} (${control.source})`}</title>
					</g>
				)}
				{placed.map(({ r, x, y, slot }) => {
					const total = totals.get(`${r.lat!.toFixed(1)}|${r.lng!.toFixed(1)}`) ?? 1;
					// fan radius grows with cluster size so hit areas don't overlap
					const ringR = total > 1 ? 6 + total * 2.5 : 0;
					const angle = total > 1 ? (slot / total) * Math.PI * 2 - Math.PI / 2 : 0;
					const dx = total > 1 ? Math.cos(angle) * ringR : 0;
					const dy = total > 1 ? Math.sin(angle) * ringR : 0;
					const hue = r.jobs.length ? HUE_VAR[jobHue(r.jobs[0]!.key || r.jobs[0]!.name)] : undefined;
					const cls = r.jobs.length ? "fm-run busy" : `fm-run ${r.status}`;
					const isHover = hover === r.id;
					return (
						<g
							key={r.id}
							transform={`translate(${x + dx} ${y + dy})`}
							className={isHover ? "fm-pin-hi" : ""}
							onPointerEnter={(e) => {
								e.stopPropagation();
								setHover(r.id);
							}}
							onPointerLeave={() => setHover((h) => (h === r.id ? null : h))}
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
							{r.jobs.length > 0 && <circle className="fm-ping" r="7" />}
							{/* generous invisible hit area — the visible pin is 7px */}
							<rect className="fm-hit" x="-9" y="-9" width="18" height="18" />
							<rect
								className={cls}
								x="-3.5"
								y="-3.5"
								width="7"
								height="7"
								style={hue ? { fill: hue } : undefined}
							/>
						</g>
					);
				})}
				{(() => {
					// one count label per co-located cell, not per runner
					const seen = new Set<string>();
					return placed.map(({ r, y }) => {
						const cell = `${r.lat!.toFixed(1)}|${r.lng!.toFixed(1)}`;
						const total = totals.get(cell) ?? 1;
						if (total <= 1 || seen.has(cell)) return null;
						seen.add(cell);
						const cx = placed.find((p) => `${p.r.lat!.toFixed(1)}|${p.r.lng!.toFixed(1)}` === cell)!.x;
						return (
							<text key={`n-${cell}`} x={cx} y={y + 20} className="fm-count" textAnchor="middle">
								×{total}
							</text>
						);
					});
				})()}
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
					<div className="fm-tip-name">{hovered.name}</div>
					<div className="fm-tip-sub">
						{hovered.city || "unlocated"}
						{hovered.country ? `, ${hovered.country}` : ""} · {hovered.status}
					</div>
					{hovered.jobs.length > 0 && (
						<div className="fm-tip-jobs">
							{hovered.jobs.slice(0, 4).map((j, i) => (
								<div key={i}>
									<i style={{ background: HUE_VAR[jobHue(j.key || j.name)] ?? "var(--dim)" }} />
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
