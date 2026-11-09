import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, Link } from "react-router-dom";
import { useState, type FormEvent } from "react";
import { FolderGit2, Play, RefreshCw } from "lucide-react";
import { api } from "../lib/api";
import { connectGitHubApp } from "../lib/github";
import type { Project, Run, Workflow } from "../lib/types";
import { Button, EmptyState, Input, KV, PageHeader, ProjectIcon, Spinner } from "../components/ui";
import { StatusSq } from "../components/status";
import { Row } from "../components/layout";
import { jobHue } from "../components/fingerprint";
import { duration, refName, relTime, seconds, shortSha } from "../lib/format";

export function ProjectsPage() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["projects"],
    queryFn: () => api.get<{ projects: Project[] }>("/projects"),
  });
  const [creating, setCreating] = useState(false);
  const create = useMutation({
    mutationFn: (body: Record<string, string>) => api.post("/projects", body),
    onSuccess: () => {
      setCreating(false);
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  const connect = useMutation({ mutationFn: connectGitHubApp });
  const icons = useProjectIcons(data?.projects ?? []);

  // per-card health comes from server-side counts scoped to the project —
  // one small read per project, same fan-out as the icons below. The old
  // global /runs window hid every project outside the newest 200.
  const runStats = useQueries({
    queries: (data?.projects ?? []).map((p) => ({
      queryKey: ["runs", "project", p.slug, "card"],
      queryFn: () =>
        api.get<{ runs: Run[]; total: number; counts?: Record<string, number> }>(
          `/runs?project=${encodeURIComponent(p.slug)}&limit=14`,
        ),
      staleTime: 15_000,
    })),
  });
  const stats = new Map<
    string,
    { total: number; ok: number; done: number; last: Run | null; hist: Run[] }
  >();
  (data?.projects ?? []).forEach((p, i) => {
    const d = runStats[i]?.data;
    if (!d) return;
    stats.set(p.slug, {
      total: d.counts?.all ?? d.total,
      ok: d.counts?.success ?? 0,
      done: d.counts?.completed ?? 0,
      last: d.runs[0] ?? null,
      hist: d.runs,
    });
  });

  return (
    <>
      <PageHeader
        mark="lint"
        title="Projects"
        sub="Repositories wired into KERN"
        actions={
          <div className="flex gap-2">
            <Button size="sm" onClick={() => connect.mutate()} disabled={connect.isPending}>
              Connect GitHub
            </Button>
            <Button size="sm" variant="primary" onClick={() => setCreating((c) => !c)}>
              New project
            </Button>
          </div>
        }
      />
      {creating && (
        <div className="panel mb-4 p-5">
          <CreateProjectForm
            busy={create.isPending}
            onSubmit={(body) => create.mutate(body)}
            error={create.error?.message}
          />
        </div>
      )}
      {isLoading ? (
        <Spinner />
      ) : !data || data.projects.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<FolderGit2 size={19} />}
            title="No projects"
            hint="Create a project with a local_path or repo_url, then sync its .github/workflows."
            action={
              <Button size="sm" variant="primary" onClick={() => setCreating(true)}>
                New project
              </Button>
            }
          />
        </div>
      ) : (
        <div className="pgrid">
          {data.projects.map((p) => {
            const s = stats.get(p.slug);
            const rate = s && s.done > 0 ? Math.round((s.ok / s.done) * 100) : null;
            return (
              <div
                key={p.id}
                className="pcard"
                role="link"
                tabIndex={0}
                onClick={() => nav(`/projects/${p.slug}`)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") nav(`/projects/${p.slug}`);
                }}
              >
                <div className="pt2">
                  <ProjTile p={p} iconPath={icons[p.slug]} />
                  <div className="min-w-0">
                    <b className="truncate">{p.name}</b>
                    <small>{p.repo_full_name || p.local_path || p.repo_url || p.slug}</small>
                  </div>
                  {s?.last && (
                    <StatusSq
                      status={s.last.conclusion === "failure" ? "failed" : s.last.status}
                      className="ml-auto"
                    />
                  )}
                </div>
                <div className="pmeta">
                  {s?.last ? (
                    <>
                      <Link
                        to={`/runs/${s.last.id}`}
                        onClick={(e) => e.stopPropagation()}
                        className="lnk truncate"
                      >
                        {s.last.commit_message?.split("\n")[0] || s.last.workflow_name}
                      </Link>
                      <span className="min-w-0 truncate text-[var(--faint)]">
                        {refName(s.last.ref)} · {shortSha(s.last.sha)} ·{" "}
                        {relTime(s.last.created_at)}
                      </span>
                    </>
                  ) : (
                    <span className="text-[var(--faint)]">no runs yet</span>
                  )}
                </div>
                <div className="prow">
                  {s?.hist.slice(0, 12).map((r) => (
                    <i
                      key={r.id}
                      className={`pdot ${r.conclusion === "failure" ? "f" : r.status === "in_progress" || r.status === "queued" ? "l" : ""}`}
                      title={`${r.workflow_name} #${r.run_number} · ${relTime(r.created_at)}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        nav(`/runs/${r.id}`);
                      }}
                    />
                  ))}
                  {s && s.hist.length > 12 && (
                    <span className="pdot-more" title={`${s.hist.length - 12} older runs`}>
                      +{s.hist.length - 12}
                    </span>
                  )}
                  <span className="prate">
                    {rate != null ? `${rate}% ok` : ""}
                    {s?.total ? ` · ${s.total} runs` : ""}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}

function ProjTile({ p, iconPath }: { p: Project; iconPath?: string | null }) {
  const img = ProjectIcon({
    slug: p.slug,
    repoFullName: p.repo_full_name,
    defaultBranch: p.default_branch,
    iconPath,
    localPath: p.local_path,
  });
  return img ?? <i className={`phm pat-${jobHue(p.slug)}`} />;
}

// icon_path lives in the project settings doc — one cached read per project
// is fine at this fleet size.
function useProjectIcons(projects: Project[]): Record<string, string | null> {
  const q = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["project-settings", p.slug],
      queryFn: () => api.get<{ icon_path?: string }>(`/projects/${p.slug}/settings`),
      staleTime: 60_000,
    })),
  });
  const out: Record<string, string | null> = {};
  projects.forEach((p, i) => {
    out[p.slug] = q[i]?.data?.icon_path || null;
  });
  return out;
}

function CreateProjectForm({
  busy,
  onSubmit,
  error,
}: {
  busy: boolean;
  onSubmit: (body: Record<string, string>) => void;
  error?: string;
}) {
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const body: Record<string, string> = {};
    for (const [k, v] of fd.entries()) {
      const s = String(v).trim();
      if (s) body[k] = s;
    }
    onSubmit(body);
  };
  return (
    <form onSubmit={submit} className="grid grid-cols-2 gap-3">
      <Input name="slug" placeholder="slug (required)" required />
      <Input name="name" placeholder="display name" />
      <Input name="local_path" placeholder="local path (/srv/repo)" />
      <Input name="repo_url" placeholder="repo url (https://…)" />
      <Input name="repo_full_name" placeholder="owner/repo" />
      <Input name="default_branch" placeholder="default branch (main)" />
      {error && <p className="col-span-2 text-xs text-[var(--err)]">{error}</p>}
      <div className="col-span-2">
        <Button type="submit" variant="primary" size="sm" disabled={busy}>
          {busy ? "Creating…" : "Create"}
        </Button>
      </div>
    </form>
  );
}

export function ProjectDetailPage() {
  const { slug } = useParams<{ slug: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const { data, isLoading, refetch } = useQuery({
    queryKey: ["project", slug],
    queryFn: () => api.get<{ project: Project; workflows: Workflow[] }>(`/projects/${slug}`),
  });
  const { data: psettings } = useQuery({
    queryKey: ["project-settings", slug],
    queryFn: () =>
      api.get<{
        self_heal: string;
        fork_gate: string;
        pr_trust?: string;
        icon_path?: string;
        notify_url?: string;
        artifact_quota_mb?: string;
        artifact_bytes?: number;
      }>(`/projects/${slug}/settings`),
  });
  const { data: iconScan } = useQuery({
    queryKey: ["project-icons", slug],
    queryFn: () =>
      api.get<{ icons: string[]; icon_path: string; source: string }>(`/projects/${slug}/icons`),
    staleTime: 60_000,
  });
  const setIcon = useMutation({
    mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { icon_path: v }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["project-settings", slug] });
      qc.invalidateQueries({ queryKey: ["project-icons", slug] });
    },
  });
  const { data: runsData } = useQuery({
    queryKey: ["runs", "project", slug, "detail"],
    queryFn: () =>
      api.get<{ runs: Run[]; total: number; counts?: Record<string, number> }>(
        `/runs?project=${encodeURIComponent(slug ?? "")}&limit=50`,
      ),
    refetchInterval: 8000,
  });
  const setHeal = useMutation({
    mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { self_heal: v }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
  });
  const setGate = useMutation({
    mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { fork_gate: v }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
  });
  const setPrTrust = useMutation({
    mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { pr_trust: v }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
  });
  const setNotify = useMutation({
    mutationFn: (v: string) => api.put(`/projects/${slug}/settings`, { notify_url: v }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
  });
  const setQuota = useMutation({
    mutationFn: (v: number) => api.put(`/projects/${slug}/settings`, { artifact_quota_mb: v }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["project-settings", slug] }),
  });
  const sync = useMutation({
    mutationFn: () => api.post(`/projects/${slug}/sync`),
    onSuccess: () => {
      refetch();
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  const trigger = useMutation({
    mutationFn: (wfID: string) =>
      api.post<{ run: { id: string } }>(`/projects/${slug}/workflows/${wfID}/runs`, {
        event: "workflow_dispatch",
      }),
    onSuccess: (res) => nav(`/runs/${res.run.id}`),
  });
  const setDisabled = useMutation({
    mutationFn: (v: { id: string; disabled: boolean }) =>
      api.patch(`/projects/${slug}/workflows/${v.id}`, { disabled: v.disabled }),
    onSuccess: () => refetch(),
  });

  // health stats come from the server counts — scoped to this project and
  // computed over its full history, not the fetched page.
  const runs = runsData?.runs ?? [];
  const counts = runsData?.counts;
  const health = {
    rate: counts?.completed ? Math.round(((counts.success ?? 0) / counts.completed) * 100) : null,
    median: counts?.median_seconds || null,
    fails: (counts?.completed ?? 0) - (counts?.success ?? 0),
    done: counts?.completed ?? 0,
    total: counts?.all ?? runsData?.total ?? 0,
  };

  if (isLoading || !data) return <Spinner />;
  const { project, workflows } = data;
  return (
    <>
      <PageHeader
        mark="lint"
        title={project.name}
        sub={project.repo_full_name || project.local_path || project.repo_url}
        actions={
          <>
            <Button size="sm" onClick={() => nav(`/runs?project=${slug}`)}>
              <Play size={12} /> Runs
            </Button>
            <Button size="sm" onClick={() => nav(`/environments?project=${slug}`)}>
              Environments
            </Button>
            <Button size="sm" onClick={() => sync.mutate()} disabled={sync.isPending}>
              <RefreshCw size={13} className={sync.isPending ? "animate-spin" : ""} /> Sync
              workflows
            </Button>
          </>
        }
      />
      <div className="panel mb-4 p-5">
        <div className="grid grid-cols-2 gap-x-8 md:grid-cols-3">
          <KV k="Slug" v={project.slug} mono />
          <KV k="Provider" v={project.provider} />
          <KV k="Default branch" v={project.default_branch} mono />
          <KV k="Repo" v={project.repo_url || "—"} mono />
          <KV k="Local path" v={project.local_path || "—"} mono />
          <KV k="Created" v={relTime(project.created_at)} />
          <KV
            k="Icon"
            v={
              <span className="flex items-center gap-2">
                <ProjTile p={project} iconPath={psettings?.icon_path} />
                <input
                  className="h-7 w-44 rounded-lg border border-[var(--ctl)] bg-transparent px-2 font-mono text-[11px]"
                  placeholder="logo.png — path in repo"
                  list={`icon-list-${slug}`}
                  defaultValue={psettings?.icon_path ?? ""}
                  onKeyDown={(e) => {
                    if (e.key === "Enter")
                      setIcon.mutate((e.target as HTMLInputElement).value.trim());
                  }}
                  onBlur={(e) => {
                    if (e.target.value.trim() !== (psettings?.icon_path ?? ""))
                      setIcon.mutate(e.target.value.trim());
                  }}
                />
                <datalist id={`icon-list-${slug}`}>
                  {(iconScan?.icons ?? []).map((i) => (
                    <option key={i} value={i} />
                  ))}
                </datalist>
                {iconScan && iconScan.icons.length > 0 && (
                  <span
                    className="text-[10px] text-[var(--faint)]"
                    title={iconScan.icons.slice(0, 12).join("\n")}
                  >
                    {iconScan.icons.length} image{iconScan.icons.length === 1 ? "" : "s"} found
                    {!psettings?.icon_path && iconScan.icons[0]
                      ? ` · auto: ${iconScan.icons[0]}`
                      : ""}
                  </span>
                )}
                {setIcon.isPending && (
                  <span className="text-[10px] text-[var(--faint)]">saving…</span>
                )}
              </span>
            }
          />
          <KV
            k="Self-heal"
            v={
              <select
                className="rounded border border-[var(--line)] bg-transparent px-2 py-0.5 text-xs"
                value={psettings?.self_heal ?? "inherit"}
                disabled={setHeal.isPending}
                onChange={(e) => setHeal.mutate(e.target.value)}
              >
                <option value="inherit">inherit</option>
                <option value="off">off</option>
                <option value="auto">auto</option>
                <option value="approve">approve</option>
              </select>
            }
          />
          <KV
            k="Fork gate"
            v={
              <select
                className="rounded border border-[var(--line)] bg-transparent px-2 py-0.5 text-xs"
                value={psettings?.fork_gate ?? "off"}
                disabled={setGate.isPending}
                onChange={(e) => setGate.mutate(e.target.value)}
              >
                <option value="off">off</option>
                <option value="all">all</option>
                <option value="first-time">first-time</option>
              </select>
            }
          />
          <KV
            k="PR trust"
            v={
              <select
                className="rounded border border-[var(--line)] bg-transparent px-2 py-0.5 text-xs"
                value={psettings?.pr_trust ?? "off"}
                disabled={setPrTrust.isPending}
                onChange={(e) => setPrTrust.mutate(e.target.value)}
              >
                <option value="off">off — sandboxed</option>
                <option value="trusted">trusted — same-repo PRs get secrets/docker</option>
              </select>
            }
          />
          <KV
            k="Notify URL"
            v={
              <span className="flex items-center gap-2">
                <input
                  className="h-7 w-52 rounded-lg border border-[var(--ctl)] bg-transparent px-2 font-mono text-[11px]"
                  placeholder="inherit org — or 'off'"
                  defaultValue={psettings?.notify_url ?? ""}
                  onKeyDown={(e) => {
                    if (e.key === "Enter")
                      setNotify.mutate((e.target as HTMLInputElement).value.trim());
                  }}
                  onBlur={(e) => {
                    if (e.target.value.trim() !== (psettings?.notify_url ?? ""))
                      setNotify.mutate(e.target.value.trim());
                  }}
                />
                {setNotify.isPending && (
                  <span className="text-[10px] text-[var(--faint)]">saving…</span>
                )}
              </span>
            }
          />
          <KV
            k="Artifact quota"
            v={
              <span className="flex items-center gap-2">
                <input
                  className="h-7 w-28 rounded-lg border border-[var(--ctl)] bg-transparent px-2 font-mono text-[11px]"
                  type="number"
                  min={0}
                  placeholder="MB — 0 = off"
                  defaultValue={psettings?.artifact_quota_mb ?? "0"}
                  onKeyDown={(e) => {
                    if (e.key === "Enter")
                      setQuota.mutate(parseInt((e.target as HTMLInputElement).value, 10) || 0);
                  }}
                  onBlur={(e) => {
                    const v = parseInt(e.target.value, 10) || 0;
                    if (String(v) !== (psettings?.artifact_quota_mb ?? "0")) setQuota.mutate(v);
                  }}
                />
                <span className="text-[10px] text-[var(--faint)]">
                  {psettings?.artifact_bytes != null &&
                    `used ${(psettings.artifact_bytes / 1048576).toFixed(1)} MB`}
                  {setQuota.isPending && "saving…"}
                </span>
              </span>
            }
          />
        </div>
      </div>

      {/* health strip — the project's own report card */}
      {health.total > 0 && (
        <div className="panel kpis mb-4">
          <div className="kpi">
            <div className="k">success</div>
            <div className="v">{health.rate != null ? `${health.rate}%` : "—"}</div>
            <div className="c">
              {health.fails} failed of {health.done} completed
            </div>
          </div>
          <div className="kpi">
            <div className="k">median run</div>
            <div className="v">{health.median != null ? seconds(health.median) : "—"}</div>
            <div className="c">across completed runs</div>
          </div>
          <div className="kpi">
            <div className="k">runs</div>
            <div className="v">{health.total}</div>
            <div className="c">
              {workflows.length} workflow{workflows.length === 1 ? "" : "s"}
            </div>
          </div>
        </div>
      )}

      {sync.data != null && (
        <SyncResult
          result={
            sync.data as {
              sha: string;
              workflows: {
                path: string;
                valid: boolean;
                diagnostics: { level: string; message: string }[] | null;
              }[];
            }
          }
        />
      )}
      {sync.error && <p className="mb-3 text-xs text-[var(--err)]">{sync.error.message}</p>}
      <div className="sec2">
        <h3>Workflows</h3>
        <span>{workflows.length} discovered</span>
      </div>
      <div className="panel mb-6">
        {workflows.length === 0 ? (
          <EmptyState
            title="No workflows"
            hint="Run sync to discover .github/workflows in the project source."
          />
        ) : (
          workflows.map((w) => (
            <Row key={w.id}>
              <span
                className={`min-w-0 flex-1 truncate font-medium ${w.disabled ? "text-[var(--faint)]" : ""}`}
              >
                {w.name}
              </span>
              {w.disabled && (
                <span className="text-[11px] uppercase tracking-wide text-[var(--warn)]">
                  disabled
                </span>
              )}
              <span className="font-mono flex-1 truncate text-xs text-[var(--faint)]">
                {w.path}
              </span>
              {w.disabled ? (
                <Button
                  size="sm"
                  onClick={() => setDisabled.mutate({ id: w.id, disabled: false })}
                  disabled={setDisabled.isPending}
                >
                  Enable
                </Button>
              ) : (
                <>
                  <Button
                    size="sm"
                    onClick={() => trigger.mutate(w.id)}
                    disabled={trigger.isPending}
                  >
                    <Play size={12} /> Run
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setDisabled.mutate({ id: w.id, disabled: true })}
                    disabled={setDisabled.isPending}
                  >
                    Disable
                  </Button>
                </>
              )}
            </Row>
          ))
        )}
      </div>

      <div className="sec2">
        <h3>Recent runs</h3>
        <span>this project's history</span>
        <Link to={`/runs?project=${slug}`} className="keygo" style={{ marginLeft: "auto" }}>
          All runs
        </Link>
      </div>
      <div className="panel">
        {runs.length === 0 ? (
          <EmptyState title="No runs" hint="Push to a wired repo or trigger a workflow above." />
        ) : (
          runs.slice(0, 10).map((r) => (
            <div
              key={r.id}
              className="rowlink flex cursor-pointer items-center gap-3 border-b border-[var(--line)] px-6 py-3 last:border-0"
              role="link"
              tabIndex={0}
              onClick={() => nav(`/runs/${r.id}`)}
              onKeyDown={(e) => {
                if (e.key === "Enter") nav(`/runs/${r.id}`);
              }}
            >
              <StatusSq status={r.conclusion === "failure" ? "failed" : r.status} />
              <span className="min-w-0 flex-1 truncate text-[13px]">
                {r.commit_message?.split("\n")[0] || `${r.workflow_name} #${r.run_number}`}
              </span>
              <span className="hidden w-28 truncate text-[11px] text-[var(--faint)] sm:inline">
                {refName(r.ref)}
              </span>
              <span className="w-16 text-right font-mono text-[11px] text-[var(--dim)]">
                {duration(r.started_at, r.completed_at)}
              </span>
              <span className="w-16 text-right text-[11px] text-[var(--faint)]">
                {relTime(r.created_at)}
              </span>
            </div>
          ))
        )}
      </div>
    </>
  );
}

function SyncResult({
  result,
}: {
  result: {
    sha: string;
    workflows: {
      path: string;
      valid: boolean;
      diagnostics: { level: string; message: string }[] | null;
    }[];
  };
}) {
  return (
    <div className="panel mb-4 p-5">
      <div className="mb-2 text-xs text-[var(--faint)]">
        Synced at <span className="font-mono">{result.sha.slice(0, 8)}</span>
      </div>
      {result.workflows.map((w) => (
        <div key={w.path} className="text-xs">
          <span className={w.valid ? "text-[var(--ok)]" : "text-[var(--err)]"}>
            {w.valid ? "valid" : "invalid"}
          </span>
          <span className="font-mono ml-2 text-[var(--dim)]">{w.path}</span>
          {(w.diagnostics ?? []).map((d, i) => (
            <div key={i} className="ml-4 text-[var(--warn)]">
              {d.level}: {d.message}
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}
