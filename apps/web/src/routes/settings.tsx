import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Check, Copy, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { APIToken } from "../lib/types";
import { Button, Card, CardHeader, EmptyState, Input, KV, PageHeader, Spinner } from "../components/ui";
import { Row } from "../components/layout";
import { relTime } from "../lib/format";

export function SettingsPage() {
	return (
		<>
			<PageHeader title="Settings" sub="Access tokens and instance information" />
			<TokensCard />
			<InstanceCard />
		</>
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
				<div className="flex items-center gap-2 border-b border-[var(--border)] bg-[var(--ok)]/5 px-4 py-2.5">
					<span className="text-xs text-[var(--ok)]">Token created — copy it now, it will not be shown again:</span>
					<code className="min-w-0 flex-1 truncate font-mono text-xs">{minted}</code>
					<CopyButton text={minted} />
					<Button size="sm" variant="ghost" onClick={() => setMinted(null)}>Dismiss</Button>
				</div>
			)}
			{creating && (
				<form onSubmit={submit} className="flex flex-wrap items-center gap-3 border-b border-[var(--border)] px-4 py-3">
					<Input name="name" placeholder="token name" required className="w-48" />
					{["read", "write", "admin"].map((s) => (
						<label key={s} className="flex cursor-pointer items-center gap-1.5 text-xs text-[var(--text-muted)]">
							<input type="checkbox" name={`scope_${s}`} defaultChecked={s === "read"} className="accent-[var(--accent)]" />
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
						<span className="font-mono text-xs text-[var(--text-faint)]">{t.prefix}…</span>
						<span className="text-xs text-[var(--text-muted)]">{t.scopes.join(", ")}</span>
						<span className="w-24 text-right text-xs text-[var(--text-faint)]">
							{t.expires_at ? `expires ${relTime(t.expires_at)}` : "no expiry"}
						</span>
						<span className="w-24 text-right text-xs text-[var(--text-faint)]">
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
			className="cursor-pointer rounded p-1 text-[var(--text-muted)] hover:bg-[var(--bg-subtle)] hover:text-[var(--text)]"
			title="Copy"
		>
			{copied ? <Check size={13} className="text-[var(--ok)]" /> : <Copy size={13} />}
		</button>
	);
}
