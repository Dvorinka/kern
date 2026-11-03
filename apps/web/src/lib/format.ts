// Display helpers: relative times, durations, byte sizes.

export function relTime(iso: string | null | undefined): string {
	if (!iso) return "—";
	const then = new Date(iso).getTime();
	const diff = Date.now() - then;
	const abs = Math.abs(diff);
	const suffix = diff >= 0 ? "ago" : "from now";
	if (abs < 45_000) return "just now";
	if (abs < 3_600_000) return `${Math.round(abs / 60_000)}m ${suffix}`;
	if (abs < 86_400_000) return `${Math.round(abs / 3_600_000)}h ${suffix}`;
	if (abs < 30 * 86_400_000) return `${Math.round(abs / 86_400_000)}d ${suffix}`;
	return new Date(iso).toLocaleDateString();
}

export function duration(startIso: string | null, endIso: string | null): string {
	if (!startIso) return "—";
	const end = endIso ? new Date(endIso).getTime() : Date.now();
	const ms = Math.max(0, end - new Date(startIso).getTime());
	if (ms < 1000) return `${ms}ms`;
	const s = Math.floor(ms / 1000);
	if (s < 60) return `${s}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m ${s % 60}s`;
	const h = Math.floor(m / 60);
	return `${h}h ${m % 60}m`;
}

export function seconds(n: number): string {
	if (!n || n <= 0) return "—";
	if (n < 1) return `${Math.round(n * 1000)}ms`;
	if (n < 60) return `${n.toFixed(1)}s`;
	const m = Math.floor(n / 60);
	return `${m}m ${Math.round(n % 60)}s`;
}

export function bytes(n: number | null | undefined): string {
	if (!n || n <= 0) return "0 B";
	const units = ["B", "KiB", "MiB", "GiB"];
	let v = n;
	let i = 0;
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024;
		i++;
	}
	return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function shortSha(sha: string | null | undefined): string {
	if (!sha) return "—";
	return sha.length > 8 ? sha.slice(0, 8) : sha;
}

export function refName(ref: string | null | undefined): string {
	if (!ref) return "—";
	return ref.replace(/^refs\/(heads|tags)\//, "");
}
