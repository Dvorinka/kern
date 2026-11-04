import { NavLink, Outlet, useNavigate } from "react-router-dom";
import {
	Activity,
	Database,
	FolderGit2,
	KeyRound,
	LayoutDashboard,
	LogOut,
	Moon,
	Package,
	Server,
	Sun,
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useAuth } from "../lib/auth";
import { cn } from "./ui";

const NAV = [
	{ to: "/", label: "Overview", icon: LayoutDashboard, end: true },
	{ to: "/projects", label: "Projects", icon: FolderGit2 },
	{ to: "/runs", label: "Runs", icon: Activity },
	{ to: "/runners", label: "Runners", icon: Server },
	{ to: "/secrets", label: "Secrets", icon: KeyRound },
	{ to: "/artifacts", label: "Artifacts", icon: Package },
	{ to: "/cache", label: "Cache", icon: Database },
];

export function Layout() {
	const { user, loading, logout } = useAuth();
	const nav = useNavigate();
	const [dark, setDark] = useState(() => !document.documentElement.classList.contains("light"));

	useEffect(() => {
		document.documentElement.classList.toggle("dark", dark);
		document.documentElement.classList.toggle("light", !dark);
	}, [dark]);

	useEffect(() => {
		if (!loading && !user) nav("/login", { replace: true });
	}, [loading, user, nav]);

	if (loading || !user) {
		return <div className="flex h-full items-center justify-center text-[var(--text-faint)]">…</div>;
	}

	return (
		<div className="flex h-full">
			{/* Console rail: always the dark brand surface, in both themes. */}
			<aside className="flex w-56 shrink-0 flex-col border-r border-black/40 bg-[var(--rail)]">
				<div className="flex h-14 items-center gap-2.5 border-b border-white/10 px-4">
					<img src="/favicon.svg" alt="" className="size-6 rounded-[3px]" />
					<div className="leading-none">
						<div className="text-[15px] font-bold tracking-[0.22em] text-white">KERN</div>
						<div className="mt-1 text-[9px] font-medium uppercase tracking-[0.18em] text-white/40">
							ci / cd control plane
						</div>
					</div>
				</div>
				<nav className="flex-1 space-y-px overflow-y-auto p-2 pt-3">
					<div className="px-2.5 pb-1.5 text-[9px] font-semibold uppercase tracking-[0.18em] text-white/30">
						Console
					</div>
					{NAV.map((n) => (
						<NavLink
							key={n.to}
							to={n.to}
							end={n.end}
							className={({ isActive }) =>
								cn(
									"group relative flex items-center gap-2.5 rounded-[3px] px-2.5 py-[7px] text-[12.5px] font-medium transition-colors",
									isActive
										? "bg-white/8 text-white before:absolute before:inset-y-0 before:left-0 before:w-[2.5px] before:bg-[var(--accent-bright)]"
										: "text-[var(--rail-text)]/60 hover:bg-white/5 hover:text-[var(--rail-text)]",
								)
							}
						>
							<n.icon size={15} strokeWidth={1.8} />
							{n.label}
						</NavLink>
					))}
				</nav>
				<div className="border-t border-white/10 p-2">
					<div className="flex items-center gap-2 rounded-[3px] px-2 py-1.5">
						<span className="live-dot shrink-0" />
						<span className="min-w-0 flex-1 truncate font-mono text-[11px] text-[var(--rail-text)]/60" title={user.email}>
							{user.email}
						</span>
						<button
							onClick={() => setDark((d) => !d)}
							className="cursor-pointer rounded-[3px] p-1 text-[var(--rail-text)]/50 hover:bg-white/10 hover:text-[var(--rail-text)]"
							title="Toggle theme"
						>
							{dark ? <Sun size={14} /> : <Moon size={14} />}
						</button>
						<button
							onClick={async () => {
								await logout();
								nav("/login");
							}}
							className="cursor-pointer rounded-[3px] p-1 text-[var(--rail-text)]/50 hover:bg-white/10 hover:text-[var(--rail-text)]"
							title="Sign out"
						>
							<LogOut size={14} />
						</button>
					</div>
				</div>
			</aside>
			<main className="dotgrid-faint min-w-0 flex-1 overflow-y-auto">
				<div className="mx-auto max-w-7xl px-7 py-6">
					<Outlet />
				</div>
			</main>
		</div>
	);
}

export function Row({ children, onClick, className }: { children: ReactNode; onClick?: () => void; className?: string }) {
	return (
		<div
			onClick={onClick}
			className={cn(
				"flex items-center gap-3 border-b border-[var(--border)] px-4 py-2.5 last:border-0",
				onClick && "cursor-pointer hover:bg-[var(--bg-subtle)]",
				className,
			)}
		>
			{children}
		</div>
	);
}
