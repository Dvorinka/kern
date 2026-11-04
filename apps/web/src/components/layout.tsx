import { NavLink, Outlet, useNavigate } from "react-router-dom";
import {
	Activity,
	Database,
	FolderGit2,
	HardDrive,
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
			<aside className="flex w-52 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--bg-raised)]">
				<div className="flex h-12 items-center gap-2.5 border-b border-[var(--border)] px-4">
					<img src="/favicon.svg" alt="" className="size-5 rounded-[5px]" />
					<span className="text-sm font-semibold tracking-tight">KERN</span>
				</div>
				<nav className="flex-1 space-y-0.5 p-2">
					{NAV.map((n) => (
						<NavLink
							key={n.to}
							to={n.to}
							end={n.end}
							className={({ isActive }) =>
								cn(
									"flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] font-medium transition-colors",
									isActive
										? "bg-[var(--bg-subtle)] text-[var(--text)]"
										: "text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]",
								)
							}
						>
							<n.icon size={15} strokeWidth={1.8} />
							{n.label}
						</NavLink>
					))}
				</nav>
				<div className="border-t border-[var(--border)] p-2">
					<div className="flex items-center gap-2 rounded-md px-2 py-1.5">
						<HardDrive size={14} className="text-[var(--text-faint)]" />
						<span className="min-w-0 flex-1 truncate text-xs text-[var(--text-muted)]" title={user.email}>
							{user.email}
						</span>
						<button
							onClick={() => setDark((d) => !d)}
							className="cursor-pointer rounded p-1 text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]"
							title="Toggle theme"
						>
							{dark ? <Sun size={14} /> : <Moon size={14} />}
						</button>
						<button
							onClick={async () => {
								await logout();
								nav("/login");
							}}
							className="cursor-pointer rounded p-1 text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]"
							title="Sign out"
						>
							<LogOut size={14} />
						</button>
					</div>
				</div>
			</aside>
			<main className="min-w-0 flex-1 overflow-y-auto">
				<div className="mx-auto max-w-6xl px-6 py-6">
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
