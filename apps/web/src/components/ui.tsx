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
				"inline-flex cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-lg font-medium transition-colors",
				"disabled:cursor-not-allowed disabled:opacity-50",
				size === "sm" ? "h-7 px-3 text-xs" : "h-8 px-3.5 text-xs",
				variant === "default" &&
					"border border-[var(--ctl)] bg-[var(--bg)] text-[var(--ink)] hover:border-[#555]",
				variant === "primary" &&
					"bg-[var(--ink)] text-black hover:bg-white border border-[var(--ink)] hover:border-white",
				variant === "danger" &&
					"border border-[var(--ctl)] bg-[var(--bg)] text-[var(--err)] hover:border-[var(--err)]",
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
				"h-8 w-full rounded-[7px] border border-[var(--ctl)] bg-transparent px-2.5 font-mono text-xs",
				"text-[var(--ink)] placeholder:font-sans placeholder:text-[var(--faint)] outline-none",
				"focus:border-[#555]",
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
}: {
	title: string;
	sub?: ReactNode;
	actions?: ReactNode;
}) {
	return (
		<div className="mb-5">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-2">
				<h1 className="text-xl font-semibold tracking-[-0.02em]">{title}</h1>
				{actions && <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>}
			</div>
			{sub && <div className="mt-1.5 text-xs text-[var(--dim)]">{sub}</div>}
		</div>
	);
}

// Honest empty states keep their frame: one dashed box, one sentence.
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
		<div className="emptynote" style={{ margin: "14px 16px" }}>
			{icon}
			{title}
			{hint ? ` — ${hint}` : ""}
		</div>
	);
}

export function Spinner() {
	return (
		<div className="flex justify-center py-14">
			<span className="sq live" />
		</div>
	);
}

// First-load placeholder for list cards — bars in the same row frame as
// real rows, staggered pulse, so the page never jumps when data lands.
export function SkeletonRows({ rows = 4 }: { rows?: number }) {
	return (
		<div aria-hidden>
			{Array.from({ length: rows }, (_, i) => (
				<div className="skl" key={i}>
					<span className="skl-main">
						<i style={{ width: `${36 + ((i * 31) % 38)}%`, animationDelay: `${i * 90}ms` }} />
						<i
							className="sub"
							style={{ width: `${20 + ((i * 17) % 22)}%`, animationDelay: `${i * 90}ms` }}
						/>
					</span>
					<i className="side" style={{ animationDelay: `${i * 90}ms` }} />
				</div>
			))}
		</div>
	);
}

// Failed queries get the same dashed frame as empty states, in err colour,
// with a retry that re-fires the query.
export function ErrorNote({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
	const msg = error instanceof Error ? error.message : "Something went wrong.";
	return (
		<div className="errnote">
			<span className="sq bad" />
			<span>{msg}</span>
			{onRetry && (
				<button className="act" onClick={onRetry}>
					Retry
				</button>
			)}
		</div>
	);
}

// Label-over-value for padded cards. For divided cell strips use .cells/.cell
// markup directly; for side-by-side rows use the .kv CSS pattern.
export function KV({ k, v, mono }: { k: string; v: ReactNode; mono?: boolean }) {
	return (
		<div className="py-1">
			<div className="text-[11px] text-[var(--faint)]">{k}</div>
			<div
				className={cn("mt-0.5 text-xs text-[var(--ink)]", mono && "font-mono text-[var(--dim)]")}
			>
				{v}
			</div>
		</div>
	);
}
