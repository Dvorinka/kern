import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { FanMark } from "../components/layout";

export function LoginPage() {
	const { login } = useAuth();
	const nav = useNavigate();
	const [email, setEmail] = useState("");
	const [password, setPassword] = useState("");
	const [err, setErr] = useState("");
	const [busy, setBusy] = useState(false);
	const { data: meta } = useQuery({
		queryKey: ["meta"],
		queryFn: () => api.get<{ version: string }>("/meta"),
		retry: 0,
		staleTime: Number.POSITIVE_INFINITY,
	});

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
		<div className="console-bg">
			<div className="lbrand brand">
				<span className="tile">
					<FanMark />
				</span>
				<span>KERN</span>
			</div>
			<div className="login">
				<div className="lp">
					<svg viewBox="0 0 400 300" aria-hidden="true">
						<defs>
							<pattern id="pb" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
								<rect width="6" height="6" fill="var(--ink)" />
								<rect width="2.5" height="6" fill="var(--paper)" />
							</pattern>
							<pattern id="pw" width="7" height="7" patternUnits="userSpaceOnUse">
								<rect width="7" height="7" fill="var(--ink)" />
								<circle cx="3.5" cy="3.5" r="1.7" fill="var(--paper)" />
							</pattern>
						</defs>
						<g fill="none" stroke="var(--paper)" strokeWidth="5" strokeLinecap="round">
							<path d="M60 150 H150" />
							<path d="M190 150 H215 C250 150 250 60 285 60 H320" />
							<path d="M190 150 H320" />
							<path d="M190 150 H215 C250 150 250 240 285 240 H320" />
						</g>
						<circle cx="50" cy="150" r="20" fill="var(--paper)" />
						<circle cx="160" cy="150" r="30" fill="var(--paper)" />
						<circle cx="338" cy="60" r="20" fill="var(--paper)" />
						<circle cx="338" cy="150" r="18" fill="url(#pb)" stroke="var(--paper)" strokeWidth="4" />
						<circle cx="338" cy="240" r="18" fill="url(#pw)" stroke="var(--paper)" strokeWidth="4" />
					</svg>
				</div>
				<form className="lbox" onSubmit={submit}>
					<h1>Sign in to KERN</h1>
					<p className="sub">Your workflows, on your own machines.</p>
					<div className="lfld">
						<label htmlFor="em">Email</label>
						<input
							id="em"
							type="email"
							required
							autoFocus
							autoComplete="email"
							value={email}
							onChange={(e) => setEmail(e.target.value)}
						/>
					</div>
					<div className="lfld">
						<label htmlFor="pw">Password</label>
						<input
							id="pw"
							type="password"
							required
							autoComplete="current-password"
							value={password}
							onChange={(e) => setPassword(e.target.value)}
						/>
					</div>
					{err && <p className="lerr">{err}</p>}
					<button className="lgo" type="submit" disabled={busy}>
						{busy ? "Signing in…" : "Sign in"}
					</button>
					<div className="lfoot">
						<span style={{ display: "flex", gap: 7, alignItems: "center" }}>
							<i className="sq live" /> API ok
						</span>
						{meta?.version && <span>{meta.version}</span>}
						<span>Self-hosted</span>
					</div>
				</form>
			</div>
		</div>
	);
}
