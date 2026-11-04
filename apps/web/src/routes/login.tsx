import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { Button, Input } from "../components/ui";
import { KernelMark } from "../components/layout";

export function LoginPage() {
	const { login } = useAuth();
	const nav = useNavigate();
	const [email, setEmail] = useState("");
	const [password, setPassword] = useState("");
	const [err, setErr] = useState("");
	const [busy, setBusy] = useState(false);

	const submit = async (e: FormEvent) => {
		e.preventDefault();
		setBusy(true);
		setErr("");
		try {
			await login(email, password);
			nav("/", { replace: true });
		} catch {
			setErr("Invalid email or password.");
		} finally {
			setBusy(false);
		}
	};

	return (
		<div className="console-bg flex h-full items-center justify-center">
			<form onSubmit={submit} className="panel w-80 rounded-xl p-6">
				<div className="mb-5 flex items-center gap-2.5">
					<KernelMark size={16} />
					<div>
						<div className="text-[13px] font-semibold tracking-[0.22em]">KERN</div>
						<div className="text-[11px] text-[var(--faint)]">Distributed CI/CD control plane</div>
					</div>
				</div>
				<label className="mb-3 block">
					<span className="mb-1 block text-xs text-[var(--text-muted)]">Email</span>
					<Input
						type="email"
						required
						autoFocus
						autoComplete="email"
						value={email}
						onChange={(e) => setEmail(e.target.value)}
					/>
				</label>
				<label className="mb-4 block">
					<span className="mb-1 block text-xs text-[var(--text-muted)]">Password</span>
					<Input
						type="password"
						required
						autoComplete="current-password"
						value={password}
						onChange={(e) => setPassword(e.target.value)}
					/>
				</label>
				{err && <p className="mb-3 text-xs text-[var(--err)]">{err}</p>}
				<Button type="submit" variant="primary" className="w-full" disabled={busy}>
					{busy ? "Signing in…" : "Sign in"}
				</Button>
			</form>
		</div>
	);
}
