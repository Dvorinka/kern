import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Check, Copy, PlugZap, RefreshCw, Trash2 } from "lucide-react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { connectGitHubApp } from "../lib/github";
import type { APIToken, AuditEvent, Member, UpdateInfo } from "../lib/types";
import {
	Button,
	Card,
	CardHeader,
	EmptyState,
	Input,
	KV,
	PageHeader,
	Spinner,
} from "../components/ui";
import { Row } from "../components/layout";
import { relTime } from "../lib/format";

export function SettingsPage() {
	return (
		<>
			<PageHeader
				title="Settings"
				sub="Access tokens, automation policy, and instance information"
			/>
			<GitHubCard />
			<PublicCICard />
			<SelfHealCard />
			<AutoUpdateCard />
			<MembersCard />
			<NotifyCard />
			<TokensCard />
			<UpdateCard />
			<InstanceCard />
			<AuditCard />
		</>
	);
}

type GitHubAppRow = {
	id: string;
	slug: string;
	name: string;
	html_url: string;
	created_at: string;
};

function GitHubCard() {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["github-app"],
		queryFn: () =>
			api.get<{ env_app: { app_id: string } | null; apps: GitHubAppRow[] }>("/github-app/status"),
	});
	const connect = useMutation({ mutationFn: connectGitHubApp });
	const disconnect = useMutation({
		mutationFn: (id: string) => api.del(`/github-app/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["github-app"] }),
	});
	const apps = data?.apps ?? [];
	const connected = Boolean(data?.env_app) || apps.length > 0;
	return (
		<Card className="mb-4">
			<CardHeader
				title="GitHub"
				sub="App credentials KERN uses to read repos, receive webhooks, and post check runs"
				actions={
					<Button
						size="sm"
						variant="primary"
						disabled={connect.isPending}
						onClick={() => connect.mutate()}
					>
						<PlugZap size={13} />
						{connected ? "Connect another org" : "Connect GitHub"}
					</Button>
				}
			/>
			{isLoading ? (
				<Spinner />
			) : !connected ? (
				<div className="px-4 py-3 text-xs text-[var(--dim)]">
					No GitHub app connected. Connect one to link repositories — GitHub walks you through app
					creation and installation.
				</div>
			) : (
				<>
					{data?.env_app && (
						<Row>
							<span className="sq ok" style={{ width: 7, height: 7 }} />
							<span className="min-w-0 flex-1 text-xs text-[var(--dim)]">
								Environment app <span className="font-mono">#{data.env_app.app_id}</span> —
								configured on the host, takes precedence over org apps.
							</span>
						</Row>
					)}
					{apps.map((a) => (
						<Row key={a.id}>
							<span className="sq ok" style={{ width: 7, height: 7 }} />
							<span className="min-w-0 flex-1 truncate text-xs text-[var(--dim)]">
								<a className="lnk" href={a.html_url} target="_blank" rel="noreferrer">
									{a.name}
								</a>{" "}
								<span className="font-mono text-[var(--faint)]">@{a.slug}</span> · added{" "}
								{relTime(a.created_at)}
							</span>
							<Button
								size="sm"
								variant="ghost"
								title="Stop using this app (the GitHub app itself is untouched)"
								disabled={disconnect.isPending}
								onClick={() => {
									if (confirm(`Disconnect ${a.name}? KERN stops using its credentials.`))
										disconnect.mutate(a.id);
								}}
							>
								<Trash2 size={13} />
							</Button>
						</Row>
					))}
				</>
			)}
			{connect.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{connect.error.message}</div>
			)}
			{disconnect.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{disconnect.error.message}</div>
			)}
		</Card>
	);
}

function PublicCICard() {
	const qc = useQueryClient();
	const { data } = useQuery({
		queryKey: ["settings"],
		queryFn: () => api.get<{ self_heal: string; public_ci: string }>("/settings"),
	});
	const setPublic = useMutation({
		mutationFn: (v: string) => api.put("/settings", { public_ci: v }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
	});
	return (
		<Card className="mb-4">
			<CardHeader title="Public runs" />
			<Row>
				<span className="min-w-0 flex-1 text-xs text-[var(--dim)]">
					GitHub check links open without a login. Status, jobs, and step names only. Logs stay
					private.
				</span>
				<select
					className="fsel"
					aria-label="Public runs"
					value={data?.public_ci ?? "off"}
					disabled={setPublic.isPending}
					onChange={(e) => setPublic.mutate(e.target.value)}
				>
					<option value="off">off</option>
					<option value="on">on</option>
				</select>
			</Row>
			{setPublic.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{setPublic.error.message}</div>
			)}
		</Card>
	);
}

function SelfHealCard() {
	const qc = useQueryClient();
	const { data } = useQuery({
		queryKey: ["settings"],
		queryFn: () => api.get<{ self_heal: string; public_ci: string }>("/settings"),
	});
	const setHeal = useMutation({
		mutationFn: (v: string) => api.put("/settings", { self_heal: v }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["settings"] });
			qc.invalidateQueries({ queryKey: ["notifications"] });
		},
	});
	return (
		<Card className="mb-4">
			<CardHeader
				title="Self-heal"
				sub="allow-listed fixes for fixable step failures; every approval is audit-logged"
			/>
			<Row>
				<span className="min-w-0 flex-1 text-xs text-[var(--dim)]">
					<b className="font-medium text-[var(--ink)]">off</b> diagnoses only ·{" "}
					<b className="font-medium text-[var(--ink)]">approve</b> queues the fix for your approval
					in the bell menu · <b className="font-medium text-[var(--ink)]">auto</b> applies the
					allow-listed fix and retries once.
				</span>
				<select
					className="fsel"
					aria-label="Self-heal"
					value={data?.self_heal ?? "off"}
					disabled={setHeal.isPending}
					onChange={(e) => setHeal.mutate(e.target.value)}
				>
					<option value="off">off</option>
					<option value="approve">approve</option>
					<option value="auto">auto</option>
				</select>
			</Row>
			{setHeal.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{setHeal.error.message}</div>
			)}
		</Card>
	);
}

function AutoUpdateCard() {
	const qc = useQueryClient();
	const { data } = useQuery({
		queryKey: ["settings"],
		queryFn: () =>
			api.get<{ self_heal: string; public_ci: string; auto_update: string }>("/settings"),
	});
	const setAuto = useMutation({
		mutationFn: (v: string) => api.put("/settings", { auto_update: v }),
		onSuccess: () => {
			qc.invalidateQueries({ queryKey: ["settings"] });
			qc.invalidateQueries({ queryKey: ["update"] });
		},
	});
	return (
		<Card className="mb-4">
			<CardHeader
				title="Automatic updates"
				sub="off by default. A signed release is the only thing this will apply"
			/>
			<Row>
				<span className="min-w-0 flex-1 text-xs text-[var(--dim)]">
					When on, the host updater fast-forwards the checkout and rebuilds the API. The API process
					cannot replace its own container. Runners with{" "}
					<span className="font-mono">KERN_RUNNER_UPDATE=1</span> pull the signed image and replace
					themselves, one at a time. An unsigned release is shown and not applied.
				</span>
				<select
					className="fsel"
					aria-label="Automatic updates"
					value={data?.auto_update ?? "off"}
					disabled={setAuto.isPending}
					onChange={(e) => setAuto.mutate(e.target.value)}
				>
					<option value="off">off</option>
					<option value="on">on</option>
				</select>
			</Row>
			{setAuto.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{setAuto.error.message}</div>
			)}
		</Card>
	);
}

function UpdateCard() {
	const qc = useQueryClient();
	const { data } = useQuery({
		queryKey: ["update"],
		queryFn: () => api.get<UpdateInfo>("/instance/update"),
		retry: 0,
	});
	const refresh = useMutation({
		mutationFn: () => api.get<UpdateInfo>("/instance/update?refresh=1"),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["update"] }),
	});
	return (
		<Card className="mb-4">
			<CardHeader
				title="Updates"
				sub={data?.checked_at ? `checked ${relTime(data.checked_at)}` : "checking releases"}
				actions={
					<Button
						size="sm"
						variant="ghost"
						disabled={refresh.isPending}
						onClick={() => refresh.mutate()}
					>
						<RefreshCw size={12} className={refresh.isPending ? "animate-spin" : ""} />
						Check now
					</Button>
				}
			/>
			{!data ? (
				<Spinner />
			) : (
				<>
					{!data.ok ? (
						<div className="px-4 py-3 text-xs text-[var(--faint)]">
							Could not reach {data.repo} releases{data.error ? ` — ${data.error}` : ""}.
						</div>
					) : data.available ? (
						<Row>
							<span className="sq warn" style={{ width: 7, height: 7 }} />
							<span className="min-w-0 flex-1 text-xs">
								<span className="text-[var(--warn)]">{data.latest}</span> is available — you run{" "}
								<span className="font-mono">{data.current}</span>
							</span>
							<a className="lnk text-xs" href={data.url} target="_blank" rel="noreferrer">
								Release
							</a>
						</Row>
					) : (
						<Row>
							<span className="sq ok" style={{ width: 7, height: 7 }} />
							<span className="min-w-0 flex-1 text-xs text-[var(--dim)]">
								Running <span className="font-mono">{data.current}</span> — latest release of{" "}
								{data.repo} is{" "}
								<a className="lnk" href={data.url} target="_blank" rel="noreferrer">
									{data.latest}
								</a>
								.
							</span>
						</Row>
					)}
					<div className="border-t border-[var(--line)] px-4 py-2 text-xs text-[var(--dim)]">
						{data.manifest_ok
							? `Signed manifest for ${data.latest}${data.git_sha ? ` (${data.git_sha.slice(0, 7)})` : ""}.`
							: data.manifest_error || "No signed manifest on this release."}
						{data.auto_update === "on" &&
						data.ok &&
						data.apply_block &&
						data.apply_block !== data.manifest_error
							? ` ${data.apply_block}.`
							: ""}
						{data.auto_update === "on" && !data.apply_block && data.available
							? " The host updater will fast-forward master and rebuild the API."
							: ""}
					</div>
					<div className="px-4 pb-3 text-xs text-[var(--faint)]">
						Host updater: {data.updater}
						{data.updater_at ? ` · ${relTime(data.updater_at)}` : ""}
						{data.updater_error ? ` — ${data.updater_error}` : ""}
						{data.updater === "not_seen" ? ". Start it with compose profile update." : ""}
					</div>
				</>
			)}
		</Card>
	);
}

// Audit targets point at the resource that changed — send the reader to the
// page that owns it instead of making them hunt for the id.
function auditHref(action: string, target: string): string | null {
	if (action.startsWith("run.")) return `/runs/${target}`;
	if (action.startsWith("environment.") || action.startsWith("deployment.")) return "/environments";
	if (action.startsWith("secret.")) return "/secrets";
	if (action.startsWith("cache.")) return "/cache";
	if (action.startsWith("artifact.")) return "/artifacts";
	if (action.startsWith("runner.")) return "/runners";
	if (
		action.startsWith("member.") ||
		action.startsWith("token.") ||
		action.startsWith("github_app.") ||
		action.startsWith("settings.")
	)
		return "/settings";
	return null;
}

function AuditCard() {
	const { data, isLoading } = useQuery({
		queryKey: ["audit"],
		queryFn: () => api.get<{ events: AuditEvent[] }>("/audit?limit=30"),
	});
	return (
		<Card className="mb-4">
			<CardHeader title="Audit trail" sub="approvals, settings changes, and secrets writes" />
			{isLoading ? (
				<Spinner />
			) : !data || data.events.length === 0 ? (
				<EmptyState title="No audit events yet" hint="Approvals and settings changes land here." />
			) : (
				data.events.map((e) => {
					const href = auditHref(e.action, e.target);
					return (
						<Row key={e.id}>
							<span className="w-44 flex-none font-mono text-xs text-[var(--ink)]">{e.action}</span>
							<span className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--dim)]">
								{href ? (
									<Link to={href} className="lnk">
										{e.target || "—"}
									</Link>
								) : (
									e.target || "—"
								)}
							</span>
							<span className="w-40 flex-none truncate text-right text-xs text-[var(--faint)]">
								{e.actor || "system"}
							</span>
							<span className="w-20 flex-none text-right text-xs text-[var(--faint)]">
								{relTime(e.created_at)}
							</span>
						</Row>
					);
				})
			)}
		</Card>
	);
}

function MembersCard() {
	const qc = useQueryClient();
	const { data: me } = useQuery({
		queryKey: ["me"],
		queryFn: () => api.get<{ user: { id: string; role: string } }>("/auth/me"),
	});
	const { data, isLoading } = useQuery({
		queryKey: ["members"],
		queryFn: () => api.get<{ members: Member[] }>("/members"),
	});
	const [creating, setCreating] = useState(false);
	const create = useMutation({
		mutationFn: (body: { email: string; name: string; password: string; role: string }) =>
			api.post("/members", body),
		onSuccess: () => {
			setCreating(false);
			qc.invalidateQueries({ queryKey: ["members"] });
		},
	});
	const setRole = useMutation({
		mutationFn: ({ id, role }: { id: string; role: string }) =>
			api.patch(`/members/${id}`, { role }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["members"] }),
	});
	const del = useMutation({
		mutationFn: (id: string) => api.del(`/members/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["members"] }),
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		create.mutate({
			email: String(fd.get("email")).trim(),
			name: String(fd.get("name")).trim(),
			password: String(fd.get("password")),
			role: String(fd.get("role")),
		});
		e.currentTarget.reset();
	};

	return (
		<Card className="mb-4">
			<CardHeader
				title="Members"
				sub="org accounts — admins manage the fleet, members run pipelines"
				actions={
					<Button size="sm" variant="primary" onClick={() => setCreating((v) => !v)}>
						Add member
					</Button>
				}
			/>
			{creating && (
				<form
					onSubmit={submit}
					className="flex flex-wrap items-center gap-3 border-b border-[var(--line)] px-4 py-3"
				>
					<Input name="email" type="email" placeholder="email" required className="w-56" />
					<Input name="name" placeholder="name" className="w-40" />
					<Input
						name="password"
						type="password"
						placeholder="password (8+)"
						required
						minLength={8}
						className="w-44"
					/>
					<select name="role" className="fsel" defaultValue="member" aria-label="Role">
						<option value="member">member</option>
						<option value="admin">admin</option>
						{me?.user.role === "owner" && <option value="owner">owner</option>}
					</select>
					<Button type="submit" variant="primary" size="sm" disabled={create.isPending}>
						Create
					</Button>
					{create.error && (
						<span className="text-xs text-[var(--err)]">{create.error.message}</span>
					)}
				</form>
			)}
			{isLoading ? (
				<Spinner />
			) : !data || data.members.length === 0 ? (
				<EmptyState title="No members" hint="The bootstrap admin is the only account." />
			) : (
				data.members.map((m) => (
					<Row key={m.id}>
						<span className="min-w-0 flex-1 truncate font-medium">
							{m.email}
							{m.id === me?.user.id && <span className="text-[var(--faint)]"> · you</span>}
						</span>
						<span className="w-32 flex-none truncate text-xs text-[var(--dim)]">
							{m.name || "—"}
						</span>
						<select
							className="fsel"
							aria-label={`Role for ${m.email}`}
							value={m.role}
							disabled={setRole.isPending}
							onChange={(e) => setRole.mutate({ id: m.id, role: e.target.value })}
						>
							<option value="member">member</option>
							<option value="admin">admin</option>
							<option value="owner">owner</option>
						</select>
						{m.id !== me?.user.id && (
							<Button
								size="sm"
								variant="ghost"
								disabled={del.isPending}
								onClick={() => {
									if (
										confirm(
											`Remove ${m.email} from the org? Their sessions and tokens are revoked.`,
										)
									)
										del.mutate(m.id);
								}}
							>
								<Trash2 size={13} />
							</Button>
						)}
					</Row>
				))
			)}
			{setRole.error && (
				<div className="px-4 py-2 text-xs text-[var(--err)]">{setRole.error.message}</div>
			)}
			{del.error && <div className="px-4 py-2 text-xs text-[var(--err)]">{del.error.message}</div>}
		</Card>
	);
}

function NotifyCard() {
	const qc = useQueryClient();
	const { data } = useQuery({
		queryKey: ["settings"],
		queryFn: () => api.get<{ notify_url: string; notify_secret_set: boolean }>("/settings"),
	});
	const save = useMutation({
		mutationFn: (body: { notify_url: string; notify_secret?: string }) =>
			api.put("/settings", body),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
	});
	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const secret = String(fd.get("secret"));
		save.mutate({
			notify_url: String(fd.get("url")).trim(),
			...(secret ? { notify_secret: secret } : {}),
		});
	};
	return (
		<Card className="mb-4">
			<CardHeader
				title="Notifications"
				sub="POST run.completed to a webhook when a run finishes — HMAC-signed if a secret is set"
			/>
			<form onSubmit={submit} className="flex flex-wrap items-center gap-3 px-4 py-3">
				<Input
					name="url"
					type="url"
					placeholder="https://hooks.example.com/kern (empty = off)"
					defaultValue={data?.notify_url ?? ""}
					className="min-w-0 flex-1"
				/>
				<Input
					name="secret"
					type="password"
					placeholder={
						data?.notify_secret_set ? "secret set — enter to rotate" : "signing secret (optional)"
					}
					className="w-56"
				/>
				<Button type="submit" variant="primary" size="sm" disabled={save.isPending}>
					Save
				</Button>
				{save.error && <span className="text-xs text-[var(--err)]">{save.error.message}</span>}
				{save.isSuccess && <span className="text-xs text-[var(--ok)]">saved</span>}
			</form>
		</Card>
	);
}

function TokensCard() {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["tokens"],
		queryFn: () => api.get<{ tokens: APIToken[] }>("/auth/tokens"),
	});
	const [creating, setCreating] = useState(false);
	// The plaintext token is returned exactly once — hold it until dismissed.
	const [minted, setMinted] = useState<string | null>(null);
	const create = useMutation({
		mutationFn: (body: { name: string; scopes: string[]; days: number }) =>
			api.post<{ token: string; record: APIToken }>("/auth/tokens", body),
		onSuccess: (res) => {
			setMinted(res.token);
			setCreating(false);
			qc.invalidateQueries({ queryKey: ["tokens"] });
		},
	});
	const del = useMutation({
		mutationFn: (id: string) => api.del(`/auth/tokens/${id}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		const scopes = ["read", "write", "admin"].filter((s) => fd.get(`scope_${s}`));
		create.mutate({
			name: String(fd.get("name")).trim(),
			scopes: scopes.length ? scopes : ["read"],
			days: parseInt(String(fd.get("days")), 10) || 0,
		});
	};

	return (
		<Card className="mb-4">
			<CardHeader
				title="API tokens"
				sub="Bearer auth for the kern CLI and scripts — shown once at creation"
				actions={
					<Button size="sm" variant="primary" onClick={() => setCreating((c) => !c)}>
						New token
					</Button>
				}
			/>
			{minted && (
				<div className="flex items-center gap-2 border-b border-[var(--line)] bg-[var(--ok)]/5 px-4 py-2.5">
					<span className="text-xs text-[var(--ok)]">
						Token created — copy it now, it will not be shown again:
					</span>
					<code className="min-w-0 flex-1 truncate font-mono text-xs">{minted}</code>
					<CopyButton text={minted} />
					<Button size="sm" variant="ghost" onClick={() => setMinted(null)}>
						Dismiss
					</Button>
				</div>
			)}
			{creating && (
				<form
					onSubmit={submit}
					className="flex flex-wrap items-center gap-3 border-b border-[var(--line)] px-4 py-3"
				>
					<Input name="name" placeholder="token name" required className="w-48" />
					{["read", "write", "admin"].map((s) => (
						<label
							key={s}
							className="flex cursor-pointer items-center gap-1.5 text-xs text-[var(--dim)]"
						>
							<input
								type="checkbox"
								name={`scope_${s}`}
								defaultChecked={s === "read"}
								className="accent-[#f2f2f2]"
							/>
							{s}
						</label>
					))}
					<Input
						name="days"
						type="number"
						min={0}
						placeholder="days (0 = never)"
						className="w-32"
					/>
					<Button type="submit" variant="primary" size="sm" disabled={create.isPending}>
						Create
					</Button>
					{create.error && (
						<span className="text-xs text-[var(--err)]">{create.error.message}</span>
					)}
				</form>
			)}
			{isLoading ? (
				<Spinner />
			) : !data || data.tokens.length === 0 ? (
				<EmptyState
					title="No tokens"
					hint="Create one to authenticate the kern CLI or external scripts."
				/>
			) : (
				data.tokens.map((t) => (
					<Row key={t.id}>
						<span className="min-w-0 flex-1 truncate font-medium">{t.name}</span>
						<span className="font-mono text-xs text-[var(--faint)]">{t.prefix}…</span>
						<span className="text-xs text-[var(--dim)]">{t.scopes.join(", ")}</span>
						<span className="w-24 text-right text-xs text-[var(--faint)]">
							{t.expires_at ? `expires ${relTime(t.expires_at)}` : "no expiry"}
						</span>
						<span className="w-24 text-right text-xs text-[var(--faint)]">
							{t.last_used_at ? `used ${relTime(t.last_used_at)}` : "never used"}
						</span>
						<Button
							size="sm"
							variant="ghost"
							disabled={del.isPending}
							onClick={() => {
								if (confirm(`Delete token ${t.name}?`)) del.mutate(t.id);
							}}
						>
							<Trash2 size={13} />
						</Button>
					</Row>
				))
			)}
		</Card>
	);
}

function InstanceCard() {
	const { data } = useQuery({
		queryKey: ["meta"],
		queryFn: () => api.get<{ name: string; version: string; env: string }>("/meta"),
	});
	return (
		<Card className="p-4">
			<div className="grid grid-cols-2 gap-x-8 md:grid-cols-3">
				<KV k="Instance" v={data?.name ?? "KERN"} />
				<KV k="Version" v={data?.version ?? "—"} mono />
				<KV k="Environment" v={data?.env ?? "—"} mono />
			</div>
		</Card>
	);
}

export function CopyButton({ text }: { text: string }) {
	const [copied, setCopied] = useState(false);
	return (
		<button
			onClick={async () => {
				await navigator.clipboard.writeText(text);
				setCopied(true);
				setTimeout(() => setCopied(false), 1500);
			}}
			className="cursor-pointer rounded p-1 text-[var(--dim)] hover:bg-[var(--raise2)] hover:text-[var(--ink)]"
			title="Copy"
		>
			{copied ? <Check size={13} className="text-[var(--ok)]" /> : <Copy size={13} />}
		</button>
	);
}
