import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FleetMapResponse } from "../lib/types";
import { WORLD_GRATICULE_PATH, WORLD_LAND_PATH } from "../lib/world-land";
import { jobHue } from "./fingerprint";

// Equirectangular map on the 0 0 1000 500 asset; the base viewBox crops the
// poles (lat > ~74 and < ~-65) so the panel stays compact.
const W = 1000;
const H = 500;
const BASE_VB = { x: 0, y: 46, w: 1000, h: 384 };
const ASPECT = BASE_VB.w / BASE_VB.h;

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

interface Vb {
	x: number;
	y: number;
	w: number;
	h: number;
}

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));

// fitView frames the located fleet plus the control pin inside the pole crop,
// padded so pins do not sit on the frame edge.
function fitView(points: [number, number][]): Vb {
	if (points.length === 0) return BASE_VB;
	const xs = points.map((p) => p[0]);
	const ys = points.map((p) => p[1]);
	const cx = (Math.min(...xs) + Math.max(...xs)) / 2;
	const cy = (Math.min(...ys) + Math.max(...ys)) / 2;
	let w = Math.max(Math.max(...xs) - Math.min(...xs) + 200, 340);
	w = Math.max(w, (Math.max(...ys) - Math.min(...ys) + 140) * ASPECT);
	w = Math.min(w, 1040);
	const h = w / ASPECT;
	return {
		x: clamp(cx - w / 2, 0, W - w),
		y: clamp(cy - h / 2, 0, H - h),
		w,
		h,
	};
}

export function FleetMap({ data }: { data: FleetMapResponse }) {
	const control = data.control;
	const pins = data.runners.filter((r) => r.lat != null && r.lng != null);
	const unlocated = data.runners.length - pins.length;

	// pins sharing one cell (same egress, or a runner under the control plane)
	// fan into a ring — control takes slot 0, runners take the rest
	const cellOf = (lat: number, lng: number) => `${lat.toFixed(1)}|${lng.toFixed(1)}`;
	const cells = new Map<string, number>();
	const slotOf = (lat: number, lng: number) => {
		const k = cellOf(lat, lng);
		const n = cells.get(k) ?? 0;
		cells.set(k, n + 1);
		return { k, slot: n };
	};
	const ctlCell = control ? slotOf(control.lat, control.lng) : null;
	const placed = pins.map((r) => {
		const { k, slot } = slotOf(r.lat!, r.lng!);
		const [x, y] = project(r.lat!, r.lng!);
		return { r, x, y, k, slot };
	});
	const ring = (k: string, slot: number): [number, number] => {
		const total = cells.get(k) ?? 1;
		if (total <= 1) return [0, 0];
		const angle = (slot / total) * Math.PI * 2 - Math.PI / 2;
		return [Math.cos(angle) * 11, Math.sin(angle) * 11];
	};

	// ── viewport: fit to fleet once, then the user's pan/zoom owns it ──
	const [vb, setVb] = useState<Vb>(BASE_VB);
	const svgRef = useRef<SVGSVGElement>(null);
	const fitted = useRef(false);
	const pts = useMemo<[number, number][]>(() => {
		const out = placed.map((p) => [p.x, p.y] as [number, number]);
		if (control) out.push(project(control.lat, control.lng));
		return out;
	}, [data]);
	const refit = useCallback(() => setVb(fitView(pts)), [pts]);
	useEffect(() => {
		if (!fitted.current && pts.length) {
			fitted.current = true;
			refit();
		}
	}, [pts, refit]);

	const zoomAt = useCallback((fx: number, fy: number, factor: number) => {
		setVb((v) => {
			const w = clamp(v.w * factor, 90, 1060);
			const k = w / v.w;
			const h = w / ASPECT;
			return {
				x: clamp(fx - (fx - v.x) * k, 0, W - w),
				y: clamp(fy - (fy - v.y) * k, 0, H - h),
				w,
				h,
			};
		});
	}, []);
	// wheel zoom needs a non-passive listener to hold the page still
	useEffect(() => {
		const el = svgRef.current;
		if (!el) return;
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			const rect = el.getBoundingClientRect();
			setVb((v) => {
				const fx = v.x + ((e.clientX - rect.left) / rect.width) * v.w;
				const fy = v.y + ((e.clientY - rect.top) / rect.height) * v.h;
				const w = clamp(v.w * (e.deltaY > 0 ? 1.22 : 1 / 1.22), 90, 1060);
				const k = w / v.w;
				const h = w / ASPECT;
				return {
					x: clamp(fx - (fx - v.x) * k, 0, W - w),
					y: clamp(fy - (fy - v.y) * k, 0, H - h),
					w,
					h,
				};
			});
		};
		el.addEventListener("wheel", onWheel, { passive: false });
		return () => el.removeEventListener("wheel", onWheel);
	}, []);

	const [panning, setPanning] = useState(false);
	const pan = useRef<{ sx: number; sy: number; vb: Vb } | null>(null);
	const onDown = (e: React.PointerEvent<SVGSVGElement>) => {
		pan.current = { sx: e.clientX, sy: e.clientY, vb };
		setPanning(true);
		e.currentTarget.setPointerCapture(e.pointerId);
	};
	const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
		const s = pan.current;
		if (!s) return;
		const rect = e.currentTarget.getBoundingClientRect();
		const dx = ((e.clientX - s.sx) / rect.width) * s.vb.w;
		const dy = ((e.clientY - s.sy) / rect.height) * s.vb.h;
		setVb({
			...s.vb,
			x: clamp(s.vb.x - dx, 0, W - s.vb.w),
			y: clamp(s.vb.y - dy, 0, H - s.vb.h),
		});
	};
	const onUp = () => {
		pan.current = null;
		setPanning(false);
	};

	const busyAny = pins.some((r) => r.jobs.length > 0);
	const drainingAny = pins.some((r) => r.status === "draining");
	const offlineAny = pins.some((r) => r.status === "offline");
	const ctlPt = control ? project(control.lat, control.lng) : null;

	return (
		<div className="fleetmap">
			<svg
				ref={svgRef}
				viewBox={`${vb.x} ${vb.y} ${vb.w} ${vb.h}`}
				role="img"
				aria-label="Fleet world map"
				className={panning ? "panning" : undefined}
				onPointerDown={onDown}
				onPointerMove={onMove}
				onPointerUp={onUp}
				onPointerLeave={onUp}
			>
				<path className="fm-grat" d={WORLD_GRATICULE_PATH} />
				<path className="fm-land" d={WORLD_LAND_PATH} />
				{control &&
					placed.flatMap(({ r }) =>
						r.jobs.map((j, ji) => {
							const hue = HUE_VAR[jobHue(j.key || j.name)] ?? "var(--dim)";
							const bow = ji % 2 === 0 ? 3 + ji : -(3 + ji);
							const d = arcPath([control.lng, control.lat], [r.lng!, r.lat!], bow);
							return (
								<g key={`${r.id}-${j.name}-${ji}`}>
									<path className="fm-arc" d={d} stroke={hue}>
										<title>{`${j.name} · ${control.city || "control"} → ${r.city || r.name}`}</title>
									</path>
									<circle r="1.6" fill={hue}>
										<animateMotion dur="2.8s" repeatCount="indefinite" path={d} />
									</circle>
								</g>
							);
						}),
					)}
				{control &&
					ctlPt &&
					ctlCell &&
					(() => {
						const [dx, dy] = ring(ctlCell.k, ctlCell.slot);
						const shared = (cells.get(ctlCell.k) ?? 1) > 1;
						return (
							<g transform={`translate(${ctlPt[0] + dx} ${ctlPt[1] + dy})`}>
								<rect className="fm-ctl" x="-4" y="-4" width="8" height="8" />
								<rect className="fm-ctlcore" x="-1.5" y="-1.5" width="3" height="3" />
								<text className="fm-label city" y={shared && dy < 0 ? -9 : 16} textAnchor="middle">
									control
								</text>
								<title>{`control plane — ${control.city || "unknown city"}, ${control.country || ""} (${control.source})`}</title>
							</g>
						);
					})()}
				{placed.map(({ r, x, y, k, slot }) => {
					const [dx, dy] = ring(k, slot);
					// labels sit radially outward in shared cells so names never stack
					const shared = (cells.get(k) ?? 1) > 1;
					const lx = shared && dx > 2 ? 7 : shared && dx < -2 ? -7 : 0;
					const ly = shared ? (Math.abs(dx) > 2 ? 3 : dy > 0 ? 15 : -8) : 13;
					const anchor = lx > 0 ? "start" : lx < 0 ? "end" : "middle";
					const hue = r.jobs.length
						? HUE_VAR[jobHue(r.jobs[0]!.key || r.jobs[0]!.name)]
						: undefined;
					const cls = r.jobs.length ? "fm-run busy" : `fm-run ${r.status}`;
					return (
						<g key={r.id} transform={`translate(${x + dx} ${y + dy})`}>
							<rect
								className={cls}
								x="-3.5"
								y="-3.5"
								width="7"
								height="7"
								style={hue ? { fill: hue } : undefined}
							/>
							<text className="fm-label" x={lx || undefined} y={ly} textAnchor={anchor}>
								{r.name}
							</text>
							{r.city && !shared && (
								<text className="fm-label city" y={ly + 8} textAnchor="middle">
									{r.city}
								</text>
							)}
							<title>
								{`${r.name} — ${r.jobs.length ? `running ${r.jobs.length} job${r.jobs.length === 1 ? "" : "s"}` : r.status}` +
									` · ${r.city || "unlocated"}${r.country ? `, ${r.country}` : ""}`}
							</title>
						</g>
					);
				})}
			</svg>
			<div className="fm-zoom">
				<button
					type="button"
					title="Zoom in"
					onClick={() => zoomAt(vb.x + vb.w / 2, vb.y + vb.h / 2, 1 / 1.45)}
				>
					+
				</button>
				<button
					type="button"
					title="Zoom out"
					onClick={() => zoomAt(vb.x + vb.w / 2, vb.y + vb.h / 2, 1.45)}
				>
					−
				</button>
				<button type="button" title="Fit fleet" onClick={refit}>
					⌖
				</button>
			</div>
			<div className="fm-legend">
				{control && (
					<span>
						<i style={{ border: "1.2px solid var(--ink)", background: "none" }} />
						control
					</span>
				)}
				<span>
					<i style={{ background: "var(--dim)" }} />
					online
				</span>
				{busyAny && (
					<span>
						<i style={{ background: "var(--lint)" }} />
						running a job
					</span>
				)}
				{offlineAny && (
					<span>
						<i style={{ border: "1px dashed var(--ctl)", background: "none" }} />
						offline
					</span>
				)}
				{drainingAny && (
					<span>
						<i style={{ border: "1px solid var(--warn)", background: "none" }} />
						draining
					</span>
				)}
				{busyAny && (
					<span>
						<i
							style={{
								width: 12,
								height: 0,
								background: "none",
								borderTop: "1px dashed var(--lint)",
								borderRadius: 0,
								verticalAlign: "2px",
							}}
						/>
						live job arc
					</span>
				)}
			</div>
			{unlocated > 0 && (
				<span
					className="fm-note"
					title={data.runners
						.filter((r) => r.lat == null || r.lng == null)
						.map((r) => r.name)
						.join(", ")}
				>
					{unlocated} runner{unlocated === 1 ? "" : "s"} without a location — resolves on next
					heartbeat or edit manually on the runners page
				</span>
			)}
		</div>
	);
}
