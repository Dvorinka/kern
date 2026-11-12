import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

export function Button({
	className,
	variant = "default",
	size = "default",
	...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
	variant?: "default" | "primary" | "danger" | "ghost";
	size?: "default" | "sm";
}) {
	return (
		<button
			className={cn(
				"inline-flex cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-full font-medium transition-colors",
				"disabled:cursor-not-allowed disabled:opacity-50",
				size === "sm" ? "h-7 px-3 text-xs" : "h-8 px-3.5 text-xs",
				variant === "default" &&
					"border border-[var(--line2)] bg-[var(--raise)] text-[var(--ink)] hover:border-[var(--ink)]",
				variant === "primary" &&
					"bg-[var(--ink)] text-[var(--paper)] border border-[var(--ink)] hover:bg-[var(--live)] hover:border-[var(--live)] hover:text-white",
				variant === "danger" &&
					"border border-[var(--ctl)] bg-[var(--raise)] text-[var(--err)] hover:border-[var(--err)]",
				variant === "ghost" && "text-[var(--dim)] hover:bg-[var(--raise2)] hover:text-[var(--ink)]",
				className,
			)}
			{...props}
		/>
	);
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
	return (
		<input
			className={cn(
				"h-9 w-full rounded-full border border-[var(--line2)] bg-[var(--raise)] px-4 font-mono text-xs",
				"text-[var(--ink)] placeholder:font-sans placeholder:text-[var(--faint)] outline-none",
				"focus:border-[var(--ink)]",
				className,
			)}
			{...props}
		/>
	);
}

export function Card({ className, children }: { className?: string; children: ReactNode }) {
	return <div className={cn("panel", className)}>{children}</div>;
}

export function CardHeader({
	title,
	sub,
	actions,
}: {
	title: string;
	sub?: string;
	actions?: ReactNode;
}) {
	return (
		<div className="phead">
			<span className="pt">{title}</span>
			<span className="r">
				{sub && <span>{sub}</span>}
				{actions}
			</span>
		</div>
	);
}

export function PageHeader({
	title,
	sub,
	actions,
	mark,
}: {
	title: string;
	sub?: ReactNode;
	actions?: ReactNode;
	// mark gives each page its own tile — a pattern for pipeline-shaped
	// pages, a state square for the rest.
	mark?: "lint" | "build" | "web" | "live" | "err" | "ok";
}) {
	return (
		<div className="ph">
			<span
				className={cn(
					"phm",
					mark === "lint" || mark === "build" || mark === "web"
						? `pat-${mark}`
						: mark
							? `sqm ${mark}`
							: "pat-build",
				)}
			/>
			<div className="phb">
				<h1>{title}</h1>
				{sub && <p>{sub}</p>}
			</div>
			{actions && <div className="pha">{actions}</div>}
		</div>
	);
}

// Honest empty states keep their frame: a dashed sheet, a state square,
// one title, one hint, an optional action.
export function EmptyState({
	icon,
	title,
	hint,
	action,
}: {
	icon?: ReactNode;
	title: string;
	hint?: string;
	action?: ReactNode;
}) {
	return (
		<div className="empty">
			<span className="empty-tile">{icon ?? <i className="sq dim" />}</span>
			<b>{title}</b>
			{hint && <p>{hint}</p>}
			{action && <div className="empty-act">{action}</div>}
		</div>
	);
}

// Loading reads as the page it will become: three shimmering rows.
export function Spinner() {
	return (
		<div className="skel" role="status" aria-label="Loading">
			{[88, 64, 74].map((w, i) => (
				<i key={i} style={{ width: `${w}%`, animationDelay: `${i * 0.15}s` }} />
			))}
		</div>
	);
}

// Label-over-value for padded cards. For divided cell strips use .cells/.cell
// markup directly; for side-by-side rows use the .kv CSS pattern.
export function KV({ k, v, mono }: { k: string; v: ReactNode; mono?: boolean }) {
	return (
		<div className="min-w-0 py-1">
			<div className="text-[11px] text-[var(--faint)]">{k}</div>
			<div
				className={cn(
					"mt-0.5 text-xs text-[var(--ink)]",
					mono && "font-mono break-all text-[var(--dim)]",
				)}
			>
				{v}
			</div>
		</div>
	);
}

// RunnerIcon — the operator-set runner glyph. An http(s) URL, protocol-
// relative URL or root-relative path renders as an image; anything else
// (emoji, short label) renders as a text glyph.
export function RunnerIcon({
	icon,
	className = "rricon",
}: {
	icon?: string | null;
	className?: string;
}) {
	if (!icon) return null;
	if (/^(https?:)?\/\//.test(icon) || icon.startsWith("/")) {
		return <img className={cn(className, "rrimg")} src={icon} alt="" />;
	}
	return <span className={className}>{icon}</span>;
}

// ProjectIcon — the project-chosen image from the repo, falling back to the
// pattern tile. GitHub repos load via raw.githubusercontent.com; local
// checkouts via the /icon endpoint.
export function ProjectIcon({
	slug,
	repoFullName,
	defaultBranch,
	iconPath,
	localPath,
	className = "phm",
}: {
	slug: string;
	repoFullName?: string | null;
	defaultBranch?: string | null;
	iconPath?: string | null;
	localPath?: string | null;
	className?: string;
}) {
	if (iconPath) {
		const src = repoFullName
			? `https://raw.githubusercontent.com/${repoFullName}/${defaultBranch || "main"}/${iconPath}`
			: localPath
				? `/api/v1/projects/${encodeURIComponent(slug)}/icon`
				: null;
		if (src) return <img className={`${className} pimg`} src={src} alt="" />;
	}
	return null;
}
