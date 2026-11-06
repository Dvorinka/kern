import type { FleetMapResponse } from "../lib/types";
import { WORLD_GRATICULE_PATH, WORLD_LAND_PATH } from "../lib/world-land";
import { jobHue } from "./fingerprint";

// Equirectangular map on the 0 0 1000 500 asset; the viewBox crops the poles
// (lat > ~74 and < ~-65) so the panel stays compact.
const W = 1000;
const H = 500;
const MAP_VIEWBOX = "0 46 1000 384";

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

	return (
		<div className="fleetmap">
			<svg viewBox={MAP_VIEWBOX} role="img" aria-label="Fleet world map">
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
					const angle = total > 1 ? (slot / total) * Math.PI * 2 - Math.PI / 2 : 0;
					const dx = total > 1 ? Math.cos(angle) * 9 : 0;
					const dy = total > 1 ? Math.sin(angle) * 9 : 0;
					const hue = r.jobs.length ? HUE_VAR[jobHue(r.jobs[0]!.key || r.jobs[0]!.name)] : undefined;
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
							<title>
								{`${r.name} — ${r.city || "unlocated"}, ${r.country || ""}` +
									(r.jobs.length ? ` · ${r.jobs.length} job${r.jobs.length === 1 ? "" : "s"}` : "")}
							</title>
						</g>
					);
				})}
			</svg>
			{unlocated > 0 && (
				<span className="fm-note">
					{unlocated} runner{unlocated === 1 ? "" : "s"} without a location — resolves on next heartbeat or edit
					manually on the runners page
				</span>
			)}
		</div>
	);
}
