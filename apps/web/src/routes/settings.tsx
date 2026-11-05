import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Check, Copy, RefreshCw, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { APIToken, AuditEvent, UpdateInfo } from "../lib/types";
import { Button, Card, CardHeader, EmptyState, Input, KV, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { relTime } from "../lib/format";

export function SettingsPage() {
	return (
		<>
			<PageHeader title="Settings" sub="Access tokens, automation policy, and instance information" />
			<PublicCICard />
			<SelfHealCard />
			<TokensCard />
			<UpdateCard />
			<InstanceCard />
			<AuditCard />
		</>
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
					GitHub check links open without a login. Status, jobs, and step names only. Logs stay private.
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
					<b className="font-medium text-[var(--ink)]">approve</b> queues the fix for your
					approval in the bell menu · <b className="font-medium text-[var(--ink)]">auto</b>{" "}
					applies the allow-listed fix and retries once.
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
					<Button size="sm" variant="ghost" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
						<RefreshCw size={12} className={refresh.isPending ? "animate-spin" : ""} />
						Check now
					</Button>
				}
			/>
			{!data ? (
				<Spinner />
			) : !data.ok ? (
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
						Download
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
		</Card>
	);
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
				data.events.map((e) => (
					<Row key={e.id}>
						<span className="w-44 flex-none font-mono text-xs text-[var(--ink)]">{e.action}</span>
						<span className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--dim)]">
							{e.target || "—"}
						</span>
						<span className="w-40 flex-none truncate text-right text-xs text-[var(--faint)]">
							{e.actor || "system"}
						</span>
						<span className="w-20 flex-none text-right text-xs text-[var(--faint)]">
							{relTime(e.created_at)}
						</span>
					</Row>
				))
			)}
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
					<span className="text-xs text-[var(--ok)]">Token created — copy it now, it will not be shown again:</span>
					<code className="min-w-0 flex-1 truncate font-mono text-xs">{minted}</code>
					<CopyButton text={minted} />
					<Button size="sm" variant="ghost" onClick={() => setMinted(null)}>Dismiss</Button>
				</div>
			)}
			{creating && (
				<form onSubmit={submit} className="flex flex-wrap items-center gap-3 border-b border-[var(--line)] px-4 py-3">
					<Input name="name" placeholder="token name" required className="w-48" />
					{["read", "write", "admin"].map((s) => (
						<label key={s} className="flex cursor-pointer items-center gap-1.5 text-xs text-[var(--dim)]">
							<input type="checkbox" name={`scope_${s}`} defaultChecked={s === "read"} className="accent-[#f2f2f2]" />
							{s}
						</label>
					))}
					<Input name="days" type="number" min={0} placeholder="days (0 = never)" className="w-32" />
					<Button type="submit" variant="primary" size="sm" disabled={create.isPending}>
						Create
					</Button>
					{create.error && <span className="text-xs text-[var(--err)]">{create.error.message}</span>}
				</form>
			)}
			{isLoading ? (
				<Spinner />
			) : !data || data.tokens.length === 0 ? (
				<EmptyState title="No tokens" hint="Create one to authenticate the kern CLI or external scripts." />
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
