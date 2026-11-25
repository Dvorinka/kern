package api

// MCP resources & prompts (2025-06-18): read-only kern:// views agents can
// pull into context, plus reusable prompt templates for the common CI
// loops. Resources are enforced through the same project-grant boundary as
// tools — kern://runs/* resolves the run's project, kern://projects/{slug}
// checks the slug.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type mcpResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type mcpPrompt struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Arguments   []mcpPromptArg `json:"arguments,omitempty"`
}

type mcpPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// mcpResourceList serves resources/list and resources/templates/list.
func (s *Server) mcpResources(c *gin.Context, req rpcRequest) *rpcResponse {
	r := rpcOK(req.ID, map[string]any{"resources": []mcpResource{
		{URI: "kern://fleet/status", Name: "Fleet status",
			Description: "Runners with load, labels and active jobs.",
			MimeType:    "application/json"},
	}})
	return &r
}

func (s *Server) mcpResourceTemplates(c *gin.Context, req rpcRequest) *rpcResponse {
	r := rpcOK(req.ID, map[string]any{"resourceTemplates": []mcpResourceTemplate{
		{URITemplate: "kern://projects/{slug}/workflows", Name: "Project workflows",
			Description: "Compiled workflow inventory for a project.",
			MimeType:    "application/json"},
		{URITemplate: "kern://runs/{run_id}/diagnose", Name: "Run diagnosis bundle",
			Description: "Run, jobs/steps, failed-step log tails, runner map.",
			MimeType:    "application/json"},
		{URITemplate: "kern://runs/{run_id}/jobs/{job_id}/steps/{n}/log", Name: "Step log",
			Description: "Full step log text.",
			MimeType:    "text/plain"},
	}})
	return &r
}

var (
	mcpReProjectWorkflows = regexp.MustCompile(`^kern://projects/([a-z0-9-]+)/workflows$`)
	mcpReRunDiagnose      = regexp.MustCompile(`^kern://runs/([0-9a-fA-F-]+)/diagnose$`)
	mcpReStepLog          = regexp.MustCompile(`^kern://runs/([0-9a-fA-F-]+)/jobs/([0-9a-fA-F-]+)/steps/(\d+)/log$`)
)

func mcpResourceContent(uri, mime, text string) map[string]any {
	return map[string]any{"contents": []map[string]any{
		{"uri": uri, "mimeType": mime, "text": text},
	}}
}

// mcpResourceRead serves resources/read.
func (s *Server) mcpResourceRead(c *gin.Context, req rpcRequest) *rpcResponse {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.URI == "" {
		r := rpcErr(req.ID, -32602, "invalid params")
		return &r
	}
	user, _ := currentUser(c)
	ctx := c.Request.Context()
	granted := tokenProjects(c)
	projectAllowed := func(slug string) bool {
		return len(granted) == 0 || grantHas(granted, slug)
	}
	runAllowed := func(id uuid.UUID) bool {
		if len(granted) == 0 {
			return true
		}
		run, err := s.Store.Run(ctx, id)
		if err != nil {
			return false
		}
		pr, err := s.Store.ProjectByID(ctx, run.ProjectID)
		return err == nil && grantHas(granted, pr.Slug)
	}
	failRes := func(msg string) *rpcResponse {
		r := rpcErr(req.ID, -32002, msg)
		return &r
	}
	text := func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	}
	uri := p.URI
	switch {
	case uri == "kern://fleet/status":
		if len(granted) > 0 {
			return failRes("fleet status is not available to project-scoped tokens")
		}
		runners, err := s.Store.Runners(ctx, user.OrgID)
		if err != nil {
			return failRes(err.Error())
		}
		t, err := text(runners)
		if err != nil {
			return failRes("encode error")
		}
		r := rpcOK(req.ID, mcpResourceContent(uri, "application/json", t))
		return &r
	case mcpReProjectWorkflows.MatchString(uri):
		slug := mcpReProjectWorkflows.FindStringSubmatch(uri)[1]
		if !projectAllowed(slug) {
			return failRes("project outside this token's grant")
		}
		pr, err := s.Store.Project(ctx, user.OrgID, slug)
		if err != nil {
			return failRes("project not found")
		}
		wfs, err := s.Store.Workflows(ctx, pr.ID)
		if err != nil {
			return failRes(err.Error())
		}
		t, err := text(wfs)
		if err != nil {
			return failRes("encode error")
		}
		r := rpcOK(req.ID, mcpResourceContent(uri, "application/json", t))
		return &r
	case mcpReRunDiagnose.MatchString(uri):
		id, _ := uuid.Parse(mcpReRunDiagnose.FindStringSubmatch(uri)[1])
		if !runAllowed(id) {
			return failRes("run outside this token's grant")
		}
		bundle, err := s.mcpDiagnoseRun(ctx, user, id, 8192, 5, 16384)
		if err != nil {
			return failRes(err.Error())
		}
		t, err := text(bundle)
		if err != nil {
			return failRes("encode error")
		}
		r := rpcOK(req.ID, mcpResourceContent(uri, "application/json", t))
		return &r
	case mcpReStepLog.MatchString(uri):
		m := mcpReStepLog.FindStringSubmatch(uri)
		runID, _ := uuid.Parse(m[1])
		jobID, _ := uuid.Parse(m[2])
		if !runAllowed(runID) {
			return failRes("run outside this token's grant")
		}
		n := 0
		fmt.Sscanf(m[3], "%d", &n)
		ref, _, err := s.Store.StepLogRef(ctx, jobID, n)
		if err != nil {
			return failRes(err.Error())
		}
		if ref == "" {
			return failRes("step has no log")
		}
		data, _, err := s.Blobs.ReadFrom(ref, 0, 1<<20)
		if err != nil {
			return failRes(err.Error())
		}
		r := rpcOK(req.ID, mcpResourceContent(uri, "text/plain", string(data)))
		return &r
	default:
		return failRes("unknown resource " + uri)
	}
}

func mcpPrompts() []mcpPrompt {
	return []mcpPrompt{
		{Name: "triage_failed_run",
			Description: "Pull the diagnose bundle, classify the failure (code/infra/flake/config), propose the smallest fix.",
			Arguments: []mcpPromptArg{
				{Name: "run_id", Description: "run UUID to triage", Required: true},
			}},
		{Name: "fix_pr_ci",
			Description: "Diagnose the failing CI run for a pull request and produce a concrete patch plan.",
			Arguments: []mcpPromptArg{
				{Name: "run_id", Description: "failing run UUID", Required: true},
				{Name: "pr", Description: "pull request number", Required: false},
			}},
		{Name: "optimize_repo_ci",
			Description: "Analyze a project's runs for queue time, flaky steps and cache misses; recommend speedups.",
			Arguments: []mcpPromptArg{
				{Name: "project", Description: "project slug", Required: true},
			}},
		{Name: "quarantine_flaky_runner",
			Description: "Find runner-attributable failures, drain the suspect runner, file a repair summary.",
			Arguments: []mcpPromptArg{
				{Name: "runner", Description: "runner name or id", Required: false},
			}},
		{Name: "offload_heavy_build",
			Description: "Ship a heavy build/test (Gradle, Android, Kotlin, Rust, docker images, big suites) to the fleet instead of the local machine; pull results back.",
			Arguments: []mcpPromptArg{
				{Name: "project", Description: "project slug", Required: true},
				{Name: "task", Description: "what to build/test, e.g. 'gradle assembleDebug'", Required: false},
			}},
	}
}

// mcpPromptGet serves prompts/get — a ready-to-send message list the client
// merges with its own context.
func (s *Server) mcpPromptGet(c *gin.Context, req rpcRequest) *rpcResponse {
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		r := rpcErr(req.ID, -32602, "invalid params")
		return &r
	}
	sub := func(t string) string {
		for k, v := range p.Arguments {
			t = strings.ReplaceAll(t, "{{"+k+"}}", v)
		}
		return t
	}
	var text string
	switch p.Name {
	case "triage_failed_run":
		text = sub(`Triage kern run {{run_id}}:
1. Call kern_diagnose_run for the failure bundle.
2. From each failed step's log_tail, extract the first error and the failing command.
3. Classify: code (test/compile failure in repo), infra (runner, docker, network), flake (known-flaky pattern, timeout variance), config (missing secret/var/label).
4. State the classification with confidence, the evidence lines, and the smallest fix — a concrete file/command change, a retry, or a config edit.`)
	case "fix_pr_ci":
		text = sub(`Fix CI for run {{run_id}} (PR {{pr}}):
1. kern_diagnose_run for the failure bundle.
2. Fetch the PR diff; map each failing step to the changed files it exercises.
3. Produce a patch plan: exact files and edits that fix the failures, ordered by blast radius.
4. After the plan, kern_rerun_run with failed=true to verify in place.`)
	case "optimize_repo_ci":
		text = sub(`Optimize CI for project {{project}}:
1. kern_list_runs (project={{project}}, limit=20); kern_get_run on the slowest recent runs.
2. Identify the critical path: longest jobs, queue time before pickup, cache misses in setup steps, flaky retried steps.
3. Recommend: test sharding factor, cache key fixes, runner label/pool changes, step reordering or removal.`)
	case "offload_heavy_build":
		text = sub(`Offload a heavy task to the kern fleet for project {{project}} (task: {{task}}):

The fleet builds it; the local machine only streams logs. Pick the path by what is installed:

PATH A — kern CLI present (preferred, one command):
1. Write the workflow yaml for the task (a single job; runs-on labels route it: android lane = hp-win11/android labels, linux-arm64 for arm, default x64 linux).
2. Run: kern exec --project {{project}} --workflow-file <file> --dir <repo> [--pull <outdir>]
   It bundles the tree (git-aware), uploads content-addressed, triggers, streams logs, pulls artifacts, exits with the run conclusion.

PATH B — MCP + any HTTP client:
1. Bundle locally: git ls-files -z -c -o --exclude-standard | tar czf /tmp/bundle.tar.gz --null -T -  (or plain tar of the tree)
2. sha256sum the bundle; PUT it with your bearer token:
   curl -X PUT -H "Authorization: Bearer $TOKEN" --data-binary @/tmp/bundle.tar.gz "$KERN/api/v1/projects/{{project}}/source?sha256=<digest>"
   (GET .../source/resolve?sha256=<digest> first — a hit skips the upload)
3. kern_trigger_run with project={{project}}, workflow_yaml=<yaml>, workflow_path=.github/workflows/offload.yml, bundle_ref=<returned ref>
4. kern_wait_run / kern_get_run to completion; kern_step_log for output.
5. Artifacts: kern_list_artifacts, then curl -H "Authorization: Bearer $TOKEN" "$KERN/api/v1/artifacts/<id>/download" -o out.tar.gz

Rules: the workflow runs in a container on the runner — never assume local paths. Secrets resolve server-side. Declare needs correctly for multi-job fan-out. Cache heavy dependency dirs with actions/cache so repeat offloads get faster.`)
	case "quarantine_flaky_runner":
		text = sub(`Quarantine flaky runner {{runner}}:
1. kern_list_runners for fleet state; kern_list_runs + kern_get_run on recent failures to find runner-attributable faults (OOM, docker errors, missing tools).
2. kern_set_runner_drain the suspect runner(s).
3. Write a repair summary: what failed, evidence lines, suggested fix.`)
	default:
		r := rpcErr(req.ID, -32602, "unknown prompt "+p.Name)
		return &r
	}
	r := rpcOK(req.ID, map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": map[string]any{"type": "text", "text": text}},
		},
	})
	return &r
}
