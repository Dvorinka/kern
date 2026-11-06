import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Trash2 } from "lucide-react";
import { api } from "../lib/api";
import type { SecretSummary } from "../lib/types";
import {
	Button,
	Card,
	EmptyState,
	ErrorNote,
	Input,
	PageHeader,
	SkeletonRows,
} from "../components/ui";
import { Row } from "../components/layout";
import { relTime } from "../lib/format";

export function SecretsPage() {
	const qc = useQueryClient();
	const { data, isLoading, error, refetch } = useQuery({
		queryKey: ["secrets"],
		queryFn: () => api.get<{ secrets: SecretSummary[] }>("/secrets"),
	});
	const [adding, setAdding] = useState(false);
	const put = useMutation({
		mutationFn: ({ name, value }: { name: string; value: string }) =>
			api.put(`/secrets/${encodeURIComponent(name)}`, { value }),
		onSuccess: () => {
			setAdding(false);
			qc.invalidateQueries({ queryKey: ["secrets"] });
		},
	});
	const del = useMutation({
		mutationFn: (name: string) => api.del(`/secrets/${encodeURIComponent(name)}`),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["secrets"] }),
	});

	const submit = (e: FormEvent<HTMLFormElement>) => {
		e.preventDefault();
		const fd = new FormData(e.currentTarget);
		put.mutate({ name: String(fd.get("name")), value: String(fd.get("value")) });
	};

	return (
		<>
			<PageHeader
				title="Secrets"
				sub="Encrypted at rest; delivered only to trusted runs and masked in logs"
				actions={
					<Button size="sm" variant="primary" onClick={() => setAdding((a) => !a)}>
						Add secret
					</Button>
				}
			/>
			{adding && (
				<Card className="mb-4 p-4">
					<form onSubmit={submit} className="flex gap-2">
						<Input
							name="name"
							placeholder="NAME (no spaces)"
							required
							pattern="[^\s=]+"
							className="w-56"
						/>
						<Input name="value" type="password" placeholder="value" required className="flex-1" />
						<Button type="submit" variant="primary" size="sm" disabled={put.isPending}>
							Save
						</Button>
					</form>
					{put.error && <p className="mt-2 text-xs text-[var(--err)]">{put.error.message}</p>}
				</Card>
			)}
			<Card>
				{isLoading ? (
					<SkeletonRows />
				) : error ? (
					<ErrorNote error={error} onRetry={() => refetch()} />
				) : !data || data.secrets.length === 0 ? (
					<EmptyState
						title="No secrets"
						hint="Org-level secrets here; project-scoped secrets arrive with project scoping in the UI."
					/>
				) : (
					data.secrets.map((s) => (
						<Row key={s.id}>
							<span className="font-mono min-w-0 flex-1 truncate text-[13px]">{s.name}</span>
							<span className="text-xs text-[var(--faint)]">updated {relTime(s.updated_at)}</span>
							<Button
								size="sm"
								variant="ghost"
								disabled={del.isPending}
								onClick={() => {
									if (confirm(`Delete secret ${s.name}?`)) del.mutate(s.name);
								}}
							>
								<Trash2 size={13} />
							</Button>
						</Row>
					))
				)}
			</Card>
		</>
	);
}
