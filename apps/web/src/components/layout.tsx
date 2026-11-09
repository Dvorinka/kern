import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { LogOut, Menu, Search, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import type { OverviewResponse, Project, Run, Runner, UpdateInfo } from "../lib/types";
import { NotificationsBell } from "./notifications";
import { cn } from "./ui";

const NAV: { to: string; label: string; end?: boolean }[] = [
	{ to: "/", label: "Overview", end: true },
	{ to: "/projects", label: "Projects" },
	{ to: "/runs", label: "Runs" },
	{ to: "/runners", label: "Runners" },
	{ to: "/usage", label: "Usage" },
	{ to: "/artifacts", label: "Artifacts" },
	{ to: "/cache", label: "Cache" },
	{ to: "/secrets", label: "Secrets" },
	{ to: "/environments", label: "Environments" },
	{ to: "/deliveries", label: "Webhooks" },
	{ to: "/settings", label: "Settings" },
];

// The hub-fan mark: a push fans out from the hub to the runners.
// Geometry mirrors docs/brand/logo.svg, normalized to 256².
export function FanMark({ size = 19 }: { size?: number }) {
	return (
		<svg width={size} height={size} viewBox="0 0 256 256" aria-hidden>
			<g fill="none" stroke="currentColor" strokeWidth="16" strokeLinecap="round">
				<path d="M66 86 L92 108" />
				<path d="M134 116 C 158 106 170 92 178 70 M136 128 H174 M134 140 C 158 150 170 164 178 186" />
			</g>
			<circle cx="52" cy="72" r="21" fill="currentColor" />
			<circle cx="112" cy="128" r="26" fill="currentColor" />
			<circle cx="194" cy="52" r="21" fill="currentColor" />
			<circle cx="198" cy="128" r="21" fill="currentColor" />
			<circle cx="194" cy="204" r="21" fill="currentColor" />
			<path
				d="M187 129 L194 136 L205 118"
				fill="none"
				stroke="var(--paper)"
				strokeWidth="8"
				strokeLinecap="round"
				strokeLinejoin="round"
			/>
		</svg>
	);
}

export function Layout() {
	const { user, prefs, loading, logout } = useAuth();
	const qc = useQueryClient();
	const nav = useNavigate();
	const [palette, setPalette] = useState(false);
	const [menu, setMenu] = useState(false);
	const loc = useLocation();
	const [theme, setTheme] = useState(document.documentElement.dataset.theme === "ink" ? "ink" : "paper");

	useEffect(() => setMenu(false), [loc.pathname]);

	const { data: meta } = useQuery({
		queryKey: ["meta"],
		queryFn: () => api.get<{ name: string; version: string }>("/meta"),
		staleTime: Number.POSITIVE_INFINITY,
	});
	const { data: runners } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
		refetchInterval: 10000,
	});
	const { data: overview } = useQuery({
		queryKey: ["overview"],
		queryFn: () => api.get<OverviewResponse>("/overview"),
		refetchInterval: 15000,
	});
	const { data: update } = useQuery({
		queryKey: ["update"],
		queryFn: () => api.get<UpdateInfo>("/instance/update"),
		staleTime: 30 * 60_000,
		retry: 0,
	});

	useEffect(() => {
		if (!loading && !user) nav("/login", { replace: true });
	}, [loading, user, nav]);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
				e.preventDefault();
				setPalette((p) => !p);
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const fleet = useMemo(() => {
		const rs = runners?.runners ?? [];
		return { online: rs.filter((r) => r.status === "online").length, total: rs.length };
	}, [runners]);

	// Nav badges surface what needs attention without opening the page.
	const badges = useMemo(() => {
		const o = overview?.overview;
		const m = new Map<string, { n: number; err?: boolean }>();
		if (o && o.jobs_queued > 0) m.set("/runs", { n: o.jobs_queued });
		const offline = fleet.total - fleet.online;
		if (offline > 0) m.set("/runners", { n: offline });
		const failed = overview?.webhooks?.failed_24h ?? 0;
		if (failed > 0) m.set("/deliveries", { n: failed, err: true });
		return m;
	}, [overview, fleet]);

	// The saved account preference wins once /auth/me resolves; localStorage
	// (read by theme-init.js before first paint) stays the instant fallback.
	const serverTheme = prefs?.theme;
	useEffect(() => {
		if (serverTheme !== "ink" && serverTheme !== "paper") return;
		document.documentElement.dataset.theme = serverTheme;
		localStorage.setItem("kern-theme", serverTheme);
		setTheme(serverTheme);
	}, [serverTheme]);

	const toggleTheme = () => {
		const t = theme === "paper" ? "ink" : "paper";
		document.documentElement.dataset.theme = t;
		localStorage.setItem("kern-theme", t);
		setTheme(t);
		void api
			.put("/auth/me/prefs", { prefs: { ...prefs, theme: t } })
			.then(() => qc.invalidateQueries({ queryKey: ["me"] }))
			.catch(() => {});
	};

	if (loading || !user) {
		return <div className="flex h-full items-center justify-center text-[var(--faint)]">…</div>;
	}

	const initial = (user.name || user.email || "?").trim()[0]?.toUpperCase() ?? "?";

	const navLinks = (onNavigate?: () => void) =>
		NAV.map((n) => (
			<NavLink
				key={n.to}
				to={n.to}
				end={n.end}
				onClick={onNavigate}
				className={({ isActive }) => (isActive ? "on" : "")}
			>
				{n.label}
				{n.to === "/runs" && (overview?.overview.jobs_running ?? 0) > 0 && (
					<i className="nlv" title={`${overview!.overview.jobs_running} running`} />
				)}
				{badges.has(n.to) && (
					<span className={cn("ct", badges.get(n.to)!.err && "err")}>
						{badges.get(n.to)!.n}
					</span>
				)}
			</NavLink>
		));

	return (
		<div className="flex h-full flex-col">
			<header className="hdr">
				<div className="brand">
					<Link to="/" aria-label="KERN overview" className="tile">
						<FanMark />
					</Link>
					<Link to="/" className="nm" style={{ color: "inherit" }}>
						KERN
					</Link>
					<NavLink to="/settings" className="ver" title="Instance settings">
						{update?.available && (
							<span className="udot" title={`${update.latest} available`} />
						)}
						{meta?.version ?? ""}
					</NavLink>
				</div>
				<nav className="navp" aria-label="Main">
					{navLinks()}
				</nav>
				<div className="hdrr">
					<button
						className="mbtn"
						aria-label={menu ? "Close menu" : "Open menu"}
						aria-expanded={menu}
						onClick={() => setMenu((m) => !m)}
					>
						{menu ? <X size={17} /> : <Menu size={17} />}
					</button>
					<button
						className="find"
						aria-label="Search runs and projects"
						onClick={() => {
							setMenu(false);
							setPalette(true);
						}}
					>
						<Search size={15} />
						<span className="find-l">Search runs and projects</span>
						<kbd>⌘K</kbd>
					</button>
					<NotificationsBell />
					<button className="inv" onClick={toggleTheme} title="Switch paper / ink">
						Invert
					</button>
					<Link to="/settings" className="avb" title={user.email}>
						{initial}
					</Link>
					<button
						className="avb"
						title="Sign out"
						onClick={async () => {
							await logout();
							nav("/login");
						}}
					>
						<LogOut size={13} />
					</button>
				</div>
				{menu && (
					<nav className="mmenu" aria-label="Main menu">
						{navLinks(() => setMenu(false))}
					</nav>
				)}
			</header>
			{menu && <div className="mscrim" onClick={() => setMenu(false)} />}
			<main className="console-bg min-w-0 flex-1 overflow-y-auto">
				<div className="mx-auto max-w-[1360px] px-[22px] pb-16 pt-[18px]">
					<Outlet />
				</div>
			</main>
			{palette && <CommandK onClose={() => setPalette(false)} />}
		</div>
	);
}

// ⌘K palette — every page is reachable, plus projects, runners and recent
// runs via the endpoints the app already queries. Navigates on select.
const PAGES: { label: string; sub: string; to: string }[] = [
	{ label: "Overview", sub: "page", to: "/" },
	{ label: "Projects", sub: "page", to: "/projects" },
	{ label: "Runs", sub: "page", to: "/runs" },
	{ label: "Runners", sub: "page", to: "/runners" },
	{ label: "Usage", sub: "page", to: "/usage" },
	{ label: "Artifacts", sub: "page", to: "/artifacts" },
	{ label: "Cache", sub: "page", to: "/cache" },
	{ label: "Secrets", sub: "page", to: "/secrets" },
	{ label: "Environments", sub: "page", to: "/environments" },
	{ label: "Webhooks", sub: "page", to: "/deliveries" },
	{ label: "Settings", sub: "page", to: "/settings" },
];

function CommandK({ onClose }: { onClose: () => void }) {
	const nav = useNavigate();
	const [q, setQ] = useState("");
	const [sel, setSel] = useState(0);
	const { data: projects } = useQuery({
		queryKey: ["projects"],
		queryFn: () => api.get<{ projects: Project[] }>("/projects"),
	});
	const { data: runs } = useQuery({
		queryKey: ["runs", ""],
		queryFn: () => api.get<{ runs: Run[]; total: number }>("/runs?limit=50"),
	});
	const { data: runners } = useQuery({
		queryKey: ["runners"],
		queryFn: () => api.get<{ runners: Runner[] }>("/runners"),
	});

	const items = useMemo(() => {
		const needle = q.trim().toLowerCase();
		const out: { label: string; sub: string; to: string; sq: string }[] = [];
		for (const p of PAGES) {
			if (!needle || p.label.toLowerCase().includes(needle)) out.push({ ...p, sq: "ok" });
		}
		for (const r of runners?.runners ?? []) {
			if (
				!needle ||
				`${r.name} ${r.hostname} ${r.labels.join(" ")}`.toLowerCase().includes(needle)
			) {
				out.push({
					label: r.name,
					sub: `runner · ${r.status}${r.active_jobs ? ` · ${r.active_jobs} jobs` : ""}`,
					to: "/runners",
					sq: r.status === "online" ? "idle" : "dim",
				});
			}
		}
		for (const p of projects?.projects ?? []) {
			if (!needle || `${p.name} ${p.slug}`.toLowerCase().includes(needle)) {
				out.push({ label: p.name, sub: `project · ${p.slug}`, to: `/projects/${p.slug}`, sq: "ok" });
			}
		}
		for (const r of runs?.runs ?? []) {
			const hay =
				`${r.workflow_name} #${r.run_number} ${r.project_slug} ${r.commit_message}`.toLowerCase();
			if (!needle || hay.includes(needle)) {
				out.push({
					label: `${r.workflow_name} #${r.run_number}`,
					sub: r.commit_message || r.project_slug,
					to: `/runs/${r.id}`,
					sq:
						r.status === "in_progress" || r.status === "queued"
							? "live"
							: r.conclusion === "failure"
								? "bad"
								: "ok",
				});
			}
		}
		return out.slice(0, 14);
	}, [q, projects, runs, runners]);

	const go = (to: string) => {
		onClose();
		nav(to);
	};

	return (
		<div
			className="fixed inset-0 z-50 flex items-start justify-center bg-black/45 pt-[14vh] backdrop-blur-sm"
			onClick={onClose}
		>
			<div
				className="panel ckpanel overflow-hidden"
				onClick={(e) => e.stopPropagation()}
				onKeyDown={(e) => {
					if (e.key === "Escape") onClose();
					if (e.key === "ArrowDown") {
						e.preventDefault();
						setSel((s) => Math.min(items.length - 1, s + 1));
					}
					if (e.key === "ArrowUp") {
						e.preventDefault();
						setSel((s) => Math.max(0, s - 1));
					}
					if (e.key === "Enter" && items[sel]) go(items[sel].to);
				}}
			>
				<div className="border-b border-[var(--line)] px-5 py-4">
					<input
						autoFocus
						value={q}
						onChange={(e) => {
							setQ(e.target.value);
							setSel(0);
						}}
						placeholder="Search pages, projects, runners, runs…"
						className="w-full bg-transparent text-[14px] text-[var(--ink)] outline-none placeholder:text-[var(--faint)]"
					/>
				</div>
				<div className="max-h-[340px] overflow-y-auto p-1.5">
					{items.length === 0 && (
						<div className="px-4 py-3 text-xs text-[var(--faint)]">Nothing matches.</div>
					)}
					{items.map((it, i) => (
						<button
							key={it.to}
							onClick={() => go(it.to)}
							onMouseEnter={() => setSel(i)}
							className={cn("ckit", i === sel && "s")}
						>
							<i className={`sq ${it.sq}`} style={{ width: 12, height: 12 }} />
							<span className="min-w-0 flex-1">
								<b className="truncate">{it.label}</b>
								<span className="truncate">{it.sub}</span>
							</span>
						</button>
					))}
				</div>
			</div>
		</div>
	);
}

export function Row({
	children,
	onClick,
	className,
}: {
	children: ReactNode;
	onClick?: () => void;
	className?: string;
}) {
	return (
		<div
			onClick={onClick}
			className={cn(
				"flex items-center gap-3 border-b border-[var(--line)] px-6 py-2.5 last:border-0",
				onClick && "cursor-pointer hover:bg-[var(--raise2)]",
				className,
			)}
		>
			{children}
		</div>
	);
}
