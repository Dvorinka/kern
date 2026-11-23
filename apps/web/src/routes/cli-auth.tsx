import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, Navigate, useSearchParams } from "react-router-dom";
import { Check, ShieldCheck, Terminal, X } from "lucide-react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { FanMark } from "../components/layout";
import { Button, Input, Spinner } from "../components/ui";

type LookupResponse = {
	user_code: string;
	name: string;
	hostname: string;
	days: number;
	status: string;
	expires_at: string;
};

function formatCode(raw: string): string {
	const clean = raw.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 8);
	return clean.length > 4 ? `${clean.slice(0, 4)}-${clean.slice(4)}` : clean;
}

export function CliAuthPage() {
	const { user, loading } = useAuth();
	const [params, setParams] = useSearchParams();
	const urlCode = formatCode(params.get("code") ?? "");
	const [codeInput, setCodeInput] = useState(urlCode);
	const activeCode = urlCode.length === 9 ? urlCode : "";

	const { data, isLoading, error } = useQuery({
		queryKey: ["cli-auth-lookup", activeCode],
		queryFn: () =>
			api.get<LookupResponse>(`/auth/cli/lookup?code=${encodeURIComponent(activeCode)}`),
		enabled: Boolean(user && activeCode),
		retry: false,
	});

	const [customName, setCustomName] = useState<string | null>(null);
	const [customDays, setCustomDays] = useState<number | null>(null);
	const [result, setResult] = useState<"approved" | "denied" | null>(null);

	const decide = useMutation({
		mutationFn: (approve: boolean) =>
			api.post<{ status: string; token_prefix?: string; name?: string }>("/auth/cli/approve", {
				user_code: activeCode,
				approve,
				...(approve
					? {
							name: (customName ?? data?.name ?? "kern-cli").trim(),
							days: customDays ?? data?.days ?? 90,
						}
					: {}),
			}),
		onSuccess: (res) => {
			setResult(res.status === "approved" ? "approved" : "denied");
		},
	});

	if (loading) {
		return (
			<div className="console-bg grid min-h-screen place-items-center">
				<Spinner />
			</div>
		);
	}

	if (!user) {
		const next = `/auth/cli${activeCode ? `?code=${encodeURIComponent(activeCode)}` : ""}`;
		return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
	}

	const submitCode = (e: FormEvent) => {
		e.preventDefault();
		const formatted = formatCode(codeInput);
		if (formatted.length === 9) {
			setParams({ code: formatted });
		}
	};

	const effectiveName = customName ?? data?.name ?? "kern-cli";
	const effectiveDays = customDays ?? data?.days ?? 90;

	return (
		<div className="console-bg flex min-h-screen flex-col items-center justify-center px-4 py-12">
			<Link to="/" className="brand mb-8 no-underline">
				<span className="tile">
					<FanMark />
				</span>
				<span>KERN</span>
			</Link>

			<div className="panel w-full max-w-md p-6">
				<div className="mb-4 flex items-center gap-3">
					<span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl border border-[var(--line)] bg-[var(--raise2)] text-[var(--ink)]">
						<Terminal size={18} />
					</span>
					<div className="min-w-0">
						<h1 className="text-lg font-extrabold tracking-tight text-[var(--ink)]">
							Authorize KERN CLI
						</h1>
						<p className="text-xs text-[var(--faint)]">
							Signed in as <span className="font-mono text-[var(--dim)]">{user.email}</span>
						</p>
					</div>
				</div>

				{result === "approved" ? (
					<div className="rounded-xl border border-[var(--ok-accent)]/35 bg-[var(--ok-accent)]/10 p-4">
						<div className="flex items-center gap-2 text-sm font-semibold text-[var(--ok-accent)]">
							<Check size={16} /> CLI authorized
						</div>
						<p className="mt-1.5 text-xs text-[var(--dim)]">
							A new API token has been delivered to your terminal. You can close this tab now.
						</p>
						<div className="mt-4">
							<Link to="/" className="lnk text-xs">
								Return to dashboard
							</Link>
						</div>
					</div>
				) : result === "denied" ? (
					<div className="rounded-xl border border-[var(--err)]/35 bg-[var(--err)]/10 p-4">
						<div className="flex items-center gap-2 text-sm font-semibold text-[var(--err)]">
							<X size={16} /> Request denied
						</div>
						<p className="mt-1.5 text-xs text-[var(--dim)]">
							No token was issued. If you did not start this login, no further action is needed.
						</p>
						<div className="mt-4">
							<Link to="/" className="lnk text-xs">
								Return to dashboard
							</Link>
						</div>
					</div>
				) : !activeCode ? (
					<form onSubmit={submitCode} className="space-y-3">
						<p className="text-xs leading-relaxed text-[var(--dim)]">
							Enter the 8-character verification code displayed by{" "}
							<code className="font-mono text-[var(--ink)]">kern auth login</code>.
						</p>
						<Input
							value={codeInput}
							onChange={(e) => setCodeInput(formatCode(e.target.value))}
							placeholder="XXXX-XXXX"
							className="h-11 text-center font-mono text-lg tracking-[0.25em] uppercase"
							autoFocus
							maxLength={9}
						/>
						<Button
							type="submit"
							variant="primary"
							className="w-full justify-center"
							disabled={formatCode(codeInput).length !== 9}
						>
							Continue
						</Button>
					</form>
				) : isLoading ? (
					<Spinner />
				) : error || !data ? (
					<div className="space-y-3">
						<div className="rounded-xl border border-[var(--err)]/35 bg-[var(--err)]/10 p-3.5 text-xs text-[var(--err)]">
							Verification code <span className="font-mono font-semibold">{activeCode}</span> was
							not found or has expired.
						</div>
						<Button
							variant="ghost"
							size="sm"
							onClick={() => {
								setCodeInput("");
								setParams({});
							}}
						>
							Enter another code
						</Button>
					</div>
				) : data.status !== "pending" ? (
					<div className="rounded-xl border border-[var(--line)] bg-[var(--raise2)] p-4 text-xs text-[var(--dim)]">
						This code has already been <span className="font-semibold text-[var(--ink)]">{data.status}</span>.
					</div>
				) : (
					<div className="space-y-4">
						<div className="rounded-xl border border-[var(--line)] bg-[var(--raise2)] px-4 py-3 text-center">
							<div className="text-[10px] font-semibold uppercase tracking-widest text-[var(--faint)]">
								Verification code
							</div>
							<div className="mt-1 font-mono text-2xl font-extrabold tracking-[0.22em] text-[var(--ink)]">
								{data.user_code}
							</div>
							{data.hostname && (
								<div className="mt-1 text-[11px] text-[var(--faint)]">
									Requested from <span className="font-mono text-[var(--dim)]">{data.hostname}</span>
								</div>
							)}
						</div>

						<div className="space-y-2.5">
							<label className="block">
								<span className="mb-1 block text-[11px] font-medium text-[var(--dim)]">
									Token label
								</span>
								<Input
									value={effectiveName}
									onChange={(e) => setCustomName(e.target.value)}
									placeholder="kern-cli"
									className="w-full font-mono text-xs"
								/>
							</label>
							<label className="block">
								<span className="mb-1 block text-[11px] font-medium text-[var(--dim)]">
									Token expiration
								</span>
								<select
									className="fsel w-full"
									value={String(effectiveDays)}
									onChange={(e) => setCustomDays(parseInt(e.target.value, 10) || 0)}
								>
									<option value="30">30 days</option>
									<option value="90">90 days</option>
									<option value="365">1 year</option>
									<option value="0">Never expires</option>
								</select>
							</label>
						</div>

						<div className="flex items-start gap-2 text-[11px] text-[var(--faint)]">
							<ShieldCheck size={14} className="mt-0.5 shrink-0 text-[var(--dim)]" />
							<span>
								Confirm the code above matches your terminal before authorizing. You can revoke this
								token anytime in Settings.
							</span>
						</div>

						{decide.error && (
							<div className="text-xs text-[var(--err)]">{decide.error.message}</div>
						)}

						<div className="flex items-center gap-2 pt-1">
							<Button
								variant="primary"
								className="flex-1 justify-center"
								disabled={decide.isPending}
								onClick={() => decide.mutate(true)}
							>
								{decide.isPending ? "Authorizing…" : "Authorize CLI"}
							</Button>
							<Button
								variant="ghost"
								disabled={decide.isPending}
								onClick={() => decide.mutate(false)}
							>
								Deny
							</Button>
						</div>
					</div>
				)}
			</div>
		</div>
	);
}
