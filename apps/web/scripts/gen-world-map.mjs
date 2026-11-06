// Regenerates src/lib/world-land.ts from Natural Earth land via world-atlas.
// One-off dev tool — deps are not in package.json. Run:
//   cd /tmp && mkdir worldmap && cd worldmap && npm i world-atlas@2 topojson-client@3 d3-geo@3
//   node <repo>/apps/web/scripts/gen-world-map.mjs /tmp/worldmap <repo>/apps/web/src/lib/world-land.ts
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";

const [depsDir, outFile] = process.argv.slice(2);
if (!depsDir || !outFile) {
	console.error("usage: node gen-world-map.mjs <deps-dir> <out-file>");
	process.exit(1);
}
const require = createRequire(depsDir + "/");
const topo = JSON.parse(readFileSync(depsDir + "/node_modules/world-atlas/land-110m.json", "utf8"));
const { feature } = require("topojson-client");
const { geoEquirectangular, geoPath, geoGraticule10 } = require("d3-geo");

const W = 1000;
const H = 500;
const proj = geoEquirectangular()
	.scale(W / (2 * Math.PI))
	.translate([W / 2, H / 2])
	.precision(0.5);
const path = geoPath(proj);
const landD = path(feature(topo, topo.objects.land));
const gratD = path(geoGraticule10());

writeFileSync(
	outFile,
	`// generated from world-atlas land-110m (Natural Earth, public domain) via
// apps/web/scripts/gen-world-map.mjs — equirectangular, viewBox 0 0 1000 500.
export const WORLD_VIEWBOX = "0 0 1000 500";
export const WORLD_LAND_PATH = ${JSON.stringify(landD)};
export const WORLD_GRATICULE_PATH = ${JSON.stringify(gratD)};
`,
);
console.log("land bytes:", landD.length, "graticule bytes:", gratD.length);
