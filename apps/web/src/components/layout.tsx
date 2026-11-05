import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import {
	Activity,
	BarChart3,
	Database,
	FolderGit2,
	KeyRound,
	LayoutDashboard,
	LogOut,
	Package,
	Server,
	Settings2,
	Shield,
	Webhook,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import type { Project, Run, Runner, UpdateInfo } from "../lib/types";
import { NotificationsBell } from "./notifications";
import { cn } from "./ui";

const NAV: {
	sec?: string;
	items: { to: string; label: string; icon: typeof Activity; end?: boolean }[];
}[] = [
	{
		items: [
			{ to: "/", label: "Overview", icon: LayoutDashboard, end: true },
			{ to: "/projects", label: "Projects", icon: FolderGit2 },
			{ to: "/runs", label: "Runs", icon: Activity },
			{ to: "/runners", label: "Runners", icon: Server },
			{ to: "/usage", label: "Usage", icon: BarChart3 },
		],
	},
	{
		sec: "Output",
		items: [
			{ to: "/artifacts", label: "Artifacts", icon: Package },
			{ to: "/cache", label: "Cache", icon: Database },
			{ to: "/secrets", label: "Secrets", icon: KeyRound },
			{ to: "/environments", label: "Environments", icon: Shield },
			{ to: "/deliveries", label: "Webhooks", icon: Webhook },
		],
	},
	{ sec: "System", items: [{ to: "/settings", label: "Settings", icon: Settings2 }] },
];

export function KernelMark({ size = 15 }: { size?: number }) {
	return (
		<svg width={size} height={size} viewBox="0 0 16 16" aria-hidden>
			<rect x="1.5" y="1.5" width="13" height="13" fill="none" stroke="#f2f2f2" strokeWidth="1.5" />
			<rect x="6" y="6" width="4" height="4" fill="#f2f2f2" />
		</svg>
	);
}

function crumbs(pathname: string): string[] {
	const parts = pathname.split("/").filter(Boolean);
	if (parts.length === 0) return ["kern", "overview"];
	if (parts[0] === "runs" && parts[1]) return ["runs", `#${parts[1].slice(0, 8)}`];
	return ["kern", ...parts];
}

export function Layout() {
	const { user, loading, logout } = useAuth();
	const nav = useNavigate();
	const { pathname } = useLocation();
	const [palette, setPalette] = useState(false);

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

	if (loading || !user) {
		return <div className="flex h-full items-center justify-center text-[var(--faint)]">…</div>;
	}

	const parts = crumbs(pathname);
	const initial = (user.name || user.email || "?").trim()[0]?.toUpperCase() ?? "?";

	return (
		<div className="flex h-full">
			<aside className="rail flex w-[196px] shrink-0 flex-col border-r border-[var(--line)]">
				<div className="flex items-center gap-2.5 border-b border-[var(--line)] px-[18px] pb-3.5 pt-[15px]">
					<img src="/logo.svg" alt="" className="size-[18px] rounded-[5px]" />
					<span className="text-[13px] font-semibold tracking-[0.22em]">KERN</span>
					<NavLink to="/settings" className="verlink ml-auto" title="Instance settings">
						{update?.available && <span className="udot" title={`${update.latest} available`} />}
						{meta?.version ?? ""}
					</NavLink>
				</div>
				<nav className="flex flex-1 flex-col gap-px overflow-y-auto p-2">
					{NAV.map((g, gi) => (
						<div key={gi} className="contents">
							{g.sec && (
								<div className="px-[9px] pb-1 pt-3.5 text-[11px] text-[var(--faint)]">{g.sec}</div>
							)}
							{g.items.map((n) => (
								<NavLink
									key={n.to}
									to={n.to}
									end={n.end}
									className={({ isActive }) =>
										cn(
											"flex items-center gap-[9px] rounded-[7px] px-[9px] py-[5px] text-xs transition-colors",
											isActive
												? "bg-[var(--raise2)] text-[var(--ink)] before:-ml-px before:h-[5px] before:w-[5px] before:shrink-0 before:rounded-[1px] before:bg-[var(--ink)] before:content-['']"
												: "text-[var(--dim)] hover:text-[var(--ink)]",
										)
									}
								>
									<n.icon size={13} strokeWidth={1.7} className="opacity-65" />
									{n.label}
								</NavLink>
							))}
						</div>
					))}
				</nav>
				<div className="flex items-center gap-2 border-t border-[var(--line)] px-[18px] py-[11px] text-[11px] text-[var(--dim)]">
					<span
						className={cn("sq", fleet.online > 0 ? "ok" : "dim")}
						style={{ width: 6, height: 6 }}
					/>
					fleet · {fleet.online}/{fleet.total} online
					<button
						onClick={async () => {
							await logout();
							nav("/login");
						}}
						className="ml-auto cursor-pointer rounded p-1 text-[var(--faint)] hover:bg-[var(--raise2)] hover:text-[var(--dim)]"
						title={`Sign out (${user.email})`}
					>
						<LogOut size={12} />
					</button>
				</div>
			</aside>
			<main className="console-bg min-w-0 flex-1 overflow-y-auto">
				<nav className="mnav">
					{NAV.flatMap((g) => g.items).map((n) => (
						<NavLink
							key={n.to}
							to={n.to}
							end={n.end}
							className={({ isActive }) => cn(isActive && "on")}
						>
							{n.label}
						</NavLink>
					))}
				</nav>
				<div className="flex h-11 items-center gap-[9px] border-b border-[var(--line)] px-[22px] text-xs text-[var(--dim)]">
					{parts.map((p, i) => (
						<span key={i} className="contents">
							{i > 0 && <span className="text-[var(--faint)]">/</span>}
							{i === parts.length - 1 ? (
								<b className="font-medium text-[var(--ink)]">{p}</b>
							) : (
								<span>{p}</span>
							)}
						</span>
					))}
					<div className="ml-auto flex items-center gap-[9px]">
						<button
							onClick={() => setPalette(true)}
							className="flex h-[26px] w-[170px] cursor-pointer items-center gap-[7px] rounded-lg border border-[var(--line)] px-2.5 text-[11px] text-[var(--faint)] hover:border-[var(--ctl)]"
						>
							<svg
								width="11"
								height="11"
								fill="none"
								stroke="currentColor"
								strokeWidth="2"
								viewBox="0 0 24 24"
							>
								<circle cx="11" cy="11" r="7" />
								<path d="M21 21l-4-4" />
							</svg>
							Search…
						</button>
						<span className="kbd">⌘K</span>
						<NotificationsBell />
						<span className="av" title={user.email}>
							{initial}
						</span>
					</div>
				</div>
				<div className="mx-auto max-w-[1240px] px-[26px] py-[22px]">
					<Outlet />
				</div>
			</main>
			{palette && <CommandK onClose={() => setPalette(false)} />}
		</div>
	);
}

// ⌘K palette — searches projects and recent runs via the endpoints the app
// already queries, navigates on select.
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

	const items = useMemo(() => {
		const needle = q.trim().toLowerCase();
		const out: { label: string; sub: string; to: string }[] = [];
		for (const p of projects?.projects ?? []) {
			if (!needle || `${p.name} ${p.slug}`.toLowerCase().includes(needle)) {
				out.push({ label: p.name, sub: `project · ${p.slug}`, to: `/projects/${p.slug}` });
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
				});
			}
		}
		return out.slice(0, 12);
	}, [q, projects, runs]);

	const go = (to: string) => {
		onClose();
		nav(to);
	};

	return (
		<div
			className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 pt-[14vh]"
			onClick={onClose}
		>
			<div
				className="panel w-[480px] overflow-hidden"
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
				<div className="border-b border-[var(--line)] px-4 py-3">
					<input
						autoFocus
						value={q}
						onChange={(e) => {
							setQ(e.target.value);
							setSel(0);
						}}
						placeholder="Search projects and runs…"
						className="w-full bg-transparent text-[13px] text-[var(--ink)] outline-none placeholder:text-[var(--faint)]"
					/>
				</div>
				<div className="max-h-[320px] overflow-y-auto py-1.5">
					{items.length === 0 && (
						<div className="px-4 py-3 text-xs text-[var(--faint)]">Nothing matches.</div>
					)}
					{items.map((it, i) => (
						<button
							key={it.to}
							onClick={() => go(it.to)}
							onMouseEnter={() => setSel(i)}
							className={cn(
								"flex w-full cursor-pointer items-baseline gap-3 px-4 py-2 text-left",
								i === sel && "bg-[var(--raise2)]",
							)}
						>
							<span className="min-w-0 flex-1 truncate text-[13px] text-[var(--ink)]">
								{it.label}
							</span>
							<span className="shrink-0 font-mono text-[11px] text-[var(--faint)]">{it.sub}</span>
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
				"flex items-center gap-3 border-b border-[var(--line)] px-4 py-2.5 last:border-0",
				onClick && "cursor-pointer hover:bg-[var(--raise2)]",
				className,
			)}
		>
			{children}
		</div>
	);
}
