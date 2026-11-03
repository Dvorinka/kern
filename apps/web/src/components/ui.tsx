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
				"inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-md font-medium transition-colors",
				"disabled:cursor-not-allowed disabled:opacity-50",
				size === "sm" ? "h-7 px-2.5 text-xs" : "h-8 px-3 text-[13px]",
				variant === "default" &&
					"border border-[var(--border)] bg-[var(--bg-raised)] text-[var(--text)] hover:bg-[var(--bg-subtle)]",
				variant === "primary" && "bg-[var(--accent)] text-white hover:opacity-90",
				variant === "danger" && "bg-[var(--err)] text-white hover:opacity-90",
				variant === "ghost" && "text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]",
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
				"h-8 w-full rounded-md border border-[var(--border)] bg-[var(--bg-raised)] px-2.5 text-[13px]",
				"text-[var(--text)] placeholder:text-[var(--text-faint)] outline-none",
				"focus:border-[var(--accent)] focus:ring-2 focus:ring-[var(--accent)]/20",
				className,
			)}
			{...props}
		/>
	);
}

export function Card({ className, children }: { className?: string; children: ReactNode }) {
	return (
		<div className={cn("rounded-lg border border-[var(--border)] bg-[var(--bg-raised)]", className)}>
			{children}
		</div>
	);
}

export function PageHeader({
	title,
	sub,
	actions,
}: {
	title: string;
	sub?: string;
	actions?: ReactNode;
}) {
	return (
		<div className="mb-5 flex items-start justify-between gap-4">
			<div>
				<h1 className="text-lg font-semibold tracking-tight">{title}</h1>
				{sub && <p className="mt-0.5 text-[13px] text-[var(--text-muted)]">{sub}</p>}
			</div>
			{actions && <div className="flex items-center gap-2">{actions}</div>}
		</div>
	);
}

export function EmptyState({
	icon,
	title,
	hint,
}: {
	icon?: ReactNode;
	title: string;
	hint?: string;
}) {
	return (
		<div className="flex flex-col items-center justify-center gap-2 py-14 text-center">
			{icon && <div className="text-[var(--text-faint)]">{icon}</div>}
			<div className="text-[13px] font-medium text-[var(--text-muted)]">{title}</div>
			{hint && <div className="max-w-sm text-xs text-[var(--text-faint)]">{hint}</div>}
		</div>
	);
}

export function Spinner() {
	return (
		<div className="flex justify-center py-14">
			<div className="h-5 w-5 animate-spin rounded-full border-2 border-[var(--border-strong)] border-t-[var(--accent)]" />
		</div>
	);
}

export function KV({ k, v, mono }: { k: string; v: ReactNode; mono?: boolean }) {
	return (
		<div className="flex min-w-0 items-baseline gap-3 py-1">
			<span className="w-36 shrink-0 truncate text-xs text-[var(--text-faint)]">{k}</span>
			<span className={cn("min-w-0 truncate text-[13px]", mono && "font-mono text-xs")}>{v}</span>
		</div>
	);
}
