import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { ListChecks } from "lucide-react";
import { useAuth } from "../lib/auth";
import { Button, Input } from "../components/ui";

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
		<div className="flex h-full items-center justify-center bg-[var(--bg)]">
			<form
				onSubmit={submit}
				className="w-80 rounded-lg border border-[var(--border)] bg-[var(--bg-raised)] p-6"
			>
				<div className="mb-5 flex items-center gap-2">
					<ListChecks size={18} className="text-[var(--accent)]" />
					<span className="text-base font-semibold tracking-tight">KERN</span>
				</div>
				<label className="mb-3 block">
					<span className="mb-1 block text-xs text-[var(--text-muted)]">Email</span>
					<Input type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
				</label>
				<label className="mb-4 block">
					<span className="mb-1 block text-xs text-[var(--text-muted)]">Password</span>
					<Input type="password" required value={password} onChange={(e) => setPassword(e.target.value)} />
				</label>
				{err && <p className="mb-3 text-xs text-[var(--err)]">{err}</p>}
				<Button type="submit" variant="primary" className="w-full" disabled={busy}>
					{busy ? "Signing in…" : "Sign in"}
				</Button>
			</form>
		</div>
	);
}
