package api

// MCP endpoint (Model Context Protocol, streamable-HTTP transport, stateless
// mode): agents connect with their API token and call tools that wrap the
// same store operations the console uses. Each POST is one JSON-RPC message;
// notifications get 202, everything else gets a JSON-RPC response body.
//
// Client setup: { "url": "<kern>/api/v1/mcp", "headers": {
// "Authorization": "Bearer <api token>" } } — tokens come from Settings or
// `kern login`.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/version"
	"github.com/dvorinka/kern/internal/workflows"
)

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcOK(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcErr(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// mcpTool is one callable tool: JSON Schema for inputs plus the implementation.
// Annotations let agent clients auto-approve safe calls (readOnlyHint) and
// flag irreversible ones (destructiveHint) per the 2025-06-18 spec.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// mcpAnn builds a ToolAnnotations value. Kern's surface is closed — every
// tool stays inside this deployment (openWorldHint false).
func mcpAnn(readOnly, destructive, idempotent bool) map[string]any {
	return map[string]any{
		"readOnlyHint":    readOnly,
		"destructiveHint": destructive,
		"idempotentHint":  idempotent,
		"openWorldHint":   false,
	}
}

// mcpVersions the server negotiates. Clients pin one after initialize via
// the MCP-Protocol-Version header.
var mcpVersions = map[string]bool{"2025-03-26": true, "2025-06-18": true}

func (s *Server) mcp(c *gin.Context) {
	// Origin must target this host when present — the streamable-HTTP spec
	// requires it to stop browser pages on other origins (DNS rebinding)
	// from driving the endpoint. CLI clients send no Origin.
	if origin := c.GetHeader("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, c.Request.Host) {
			fail(c, http.StatusForbidden, "origin_denied", "untrusted Origin")
			return
		}
	}
	if v := c.GetHeader("MCP-Protocol-Version"); v != "" && !mcpVersions[v] {
		fail(c, http.StatusBadRequest, "mcp_version", "unsupported MCP-Protocol-Version")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, rpcErr(nil, -32700, "request too large"))
		return
	}
	trim := bytes.TrimSpace(body)
	if len(trim) == 0 {
		c.JSON(http.StatusBadRequest, rpcErr(nil, -32600, "invalid request"))
		return
	}
	var reqs []rpcRequest
	batch := trim[0] == '['
	if batch {
		if err := json.Unmarshal(trim, &reqs); err != nil || len(reqs) == 0 {
			c.JSON(http.StatusBadRequest, rpcErr(nil, -32700, "parse error"))
			return
		}
	} else {
		var req rpcRequest
		if err := json.Unmarshal(trim, &req); err != nil {
			c.JSON(http.StatusBadRequest, rpcErr(nil, -32700, "parse error"))
			return
		}
		reqs = []rpcRequest{req}
	}
	var resps []rpcResponse
	for _, req := range reqs {
		if r := s.mcpHandle(c, req); r != nil {
			resps = append(resps, *r)
		}
	}
	// Every message was a notification — the spec wants 202 with no body.
	if len(resps) == 0 {
		c.Status(http.StatusAccepted)
		return
	}
	if batch {
		c.JSON(http.StatusOK, resps)
		return
	}
	c.JSON(http.StatusOK, resps[0])
}

// mcpHandle executes one JSON-RPC message. A nil result means the message
// was a notification and produces no response body.
func (s *Server) mcpHandle(c *gin.Context, req rpcRequest) *rpcResponse {
	// Notifications carry no id and get no response.
	if len(req.ID) == 0 {
		return nil
	}
	if req.JSONRPC != "2.0" {
		r := rpcErr(req.ID, -32600, `invalid request: jsonrpc must be "2.0"`)
		return &r
	}
	var resp rpcResponse
	switch req.Method {
	case "initialize":
		resp = rpcOK(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"serverInfo":      map[string]any{"name": "kern", "version": version.Version},
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
				"prompts":   map[string]any{},
			},
		})
	case "ping":
		resp = rpcOK(req.ID, map[string]any{})
	case "tools/list":
		resp = rpcOK(req.ID, map[string]any{"tools": mcpTools()})
	case "resources/list":
		return s.mcpResources(c, req)
	case "resources/templates/list":
		return s.mcpResourceTemplates(c, req)
	case "resources/read":
		return s.mcpResourceRead(c, req)
	case "prompts/list":
		resp = rpcOK(req.ID, map[string]any{"prompts": mcpPrompts()})
	case "prompts/get":
		return s.mcpPromptGet(c, req)
	case "tools/call":
		return s.mcpCall(c, req)
	default:
		resp = rpcErr(req.ID, -32601, "method not found")
	}
	return &resp
}

// mcpMethodNotAllowed answers GET and DELETE on /mcp. GET would open an SSE
// stream under the streamable transport — this server is stateless and
// offers none, so 405 is the spec-compliant answer. DELETE tears down a
// session this server never mints.
func (s *Server) mcpMethodNotAllowed(c *gin.Context) {
	c.AbortWithStatus(http.StatusMethodNotAllowed)
}

func props(defs map[string]map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": defs}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func mcpTools() []mcpTool {
	return []mcpTool{
		{Name: "kern_list_projects", Description: "List CI projects in the org.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: props(map[string]map[string]any{})},
		{Name: "kern_list_runs", Description: "List workflow runs, newest first.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"project": str("project slug to filter by"),
					"status":  str("queued | in_progress | completed | cancelled"),
					"limit":   num("max runs, default 20"),
				})
				return p
			}()},
		{Name: "kern_get_run", Description: "Run detail with every job and its steps (status, conclusion, diagnosis).",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_step_log", Description: "Step log bytes. Default: tail of the log; pass offset to page forward instead.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"job_id": str("job UUID"),
					"step":   num("step number"),
					"tail":   num("bytes to read, default 8192"),
					"offset": num("byte offset to read from; omit for the tail"),
				})
				p["required"] = []string{"job_id", "step"}
				return p
			}()},
		{Name: "kern_list_runners", Description: "Fleet runners with status, labels and active jobs.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: props(map[string]map[string]any{})},
		{Name: "kern_set_runner_drain", Description: "Drain or undrain a runner (id or name). Draining runners take no new jobs.",
			Annotations: mcpAnn(false, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"runner":   str("runner UUID or name"),
					"draining": map[string]any{"type": "boolean", "description": "true to drain, false to undrain"},
				})
				p["required"] = []string{"runner", "draining"}
				return p
			}()},
		{Name: "kern_get_project", Description: "Project detail with its workflows.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"project": str("project slug")})
				p["required"] = []string{"project"}
				return p
			}()},
		{Name: "kern_trigger_run", Description: "Start a workflow run (workflow_dispatch by default).",
			Annotations: mcpAnn(false, false, false),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"project":  str("project slug"),
					"workflow": str("workflow UUID, path or name"),
					"event":    str("event name, default workflow_dispatch"),
					"ref":      str("git ref, default the project's default branch"),
					"sha":      str("commit SHA, default the workflow version's SHA"),
					"inputs":   map[string]any{"type": "object", "description": "workflow_dispatch inputs as a JSON object"},
				})
				p["required"] = []string{"project", "workflow"}
				return p
			}()},
		{Name: "kern_rerun_run", Description: "Re-run a completed or failed run from its original inputs. failed=true or jobs=[name] re-run jobs in place instead of seeding a fresh run.",
			Annotations: mcpAnn(false, false, false),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"run_id": str("run UUID"),
					"failed": map[string]any{"type": "boolean", "description": "re-run only the failed jobs in place"},
					"jobs":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "job names to re-run in place"},
				})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_list_secrets", Description: "Secret names and scopes for the org — never values. Optionally filtered by project slug and/or environment name.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: props(map[string]map[string]any{
				"project":     str("project slug"),
				"environment": str("environment name (requires project)"),
			})},
		{Name: "kern_approve_run", Description: "Release a fork-gated run (awaiting_approval -> queued).",
			Annotations: mcpAnn(false, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_cancel_run", Description: "Cancel a queued or in-progress run.",
			Annotations: mcpAnn(false, true, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_diagnose_run", Description: "Single-call failure bundle: run, jobs/steps, failed-step log tails, runner map — one round trip instead of many.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"run_id":    str("run UUID"),
					"tail":      num("per-failed-step log tail bytes, default 8192"),
					"max_steps": num("max failed steps to tail, default 5"),
				})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_search_logs", Description: "Search step logs for a string across a project or run.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"q":       str("case-insensitive substring to find"),
					"project": str("project slug to scope the search"),
					"run":     str("run UUID to scope the search"),
					"limit":   num("max hits, default 100"),
				})
				p["required"] = []string{"q"}
				return p
			}()},
		{Name: "kern_wait_run", Description: "Block until a run reaches a terminal state (or timeout) and return the final state.",
			Annotations: mcpAnn(true, false, false),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"run_id":  str("run UUID"),
					"timeout": num("seconds to wait, default 120, max 600"),
				})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_list_artifacts", Description: "Artifacts produced by a run.",
			Annotations: mcpAnn(true, false, true),
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
	}
}

// mcpCallResult wraps tool output in the MCP content envelope.
func mcpText(format string, args ...any) map[string]any {
	return map[string]any{"content": []map[string]any{
		{"type": "text", "text": fmt.Sprintf(format, args...)},
	}}
}

func mcpJSON(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": []map[string]any{
		{"type": "text", "text": string(data)},
	}}, nil
}

func mcpArgString(args map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := args[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return fmt.Sprint(n), true
	}
	return "", false
}

func mcpArgInt(args map[string]json.RawMessage, key string, def int) int {
	raw, ok := args[key]
	if !ok {
		return def
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		// JSON numbers may arrive as floats (5.0); accept those too.
		var f float64
		if json.Unmarshal(raw, &f) != nil {
			return def
		}
		return int(f)
	}
	return n
}

func (s *Server) mcpCall(c *gin.Context, req rpcRequest) *rpcResponse {
	var call struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		r := rpcErr(req.ID, -32602, "invalid params")
		return &r
	}
	// Project-scoped tokens reach MCP, but only inside their grant.
	if gerr := s.mcpToolAllowed(c, call.Name, call.Arguments); gerr != nil {
		r := rpcOK(req.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": gerr.Error()}},
		})
		return &r
	}
	user, _ := currentUser(c)
	ctx := c.Request.Context()
	var out any
	var err error
	switch call.Name {
	case "kern_list_projects":
		var projects []store.Project
		projects, err = s.Store.Projects(ctx, user.OrgID)
		if granted := tokenProjects(c); err == nil && len(granted) > 0 {
			filtered := projects[:0]
			for _, p := range projects {
				if grantHas(granted, p.Slug) {
					filtered = append(filtered, p)
				}
			}
			projects = filtered
		}
		out = projects
	case "kern_list_runs":
		f := store.RunFilter{Status: mcpArgStringOr(call.Arguments, "status"), Limit: mcpArgInt(call.Arguments, "limit", 20)}
		if slug := mcpArgStringOr(call.Arguments, "project"); slug != "" {
			var p store.Project
			p, err = s.Store.Project(ctx, user.OrgID, slug)
			if err == nil {
				f.ProjectID = &p.ID
			}
		}
		if err == nil {
			var runs []store.Run
			runs, _, err = s.Store.Runs(ctx, f)
			out = runs
		}
	case "kern_get_run":
		id, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		var run store.Run
		run, err = s.Store.Run(ctx, id)
		if err == nil {
			var jobs []store.Job
			jobs, err = s.Store.JobsForRun(ctx, run.ID)
			type jobView struct {
				store.Job
				Steps []store.Step `json:"steps"`
			}
			views := []jobView{}
			for _, j := range jobs {
				var steps []store.Step
				steps, err = s.Store.StepsForJob(ctx, j.ID)
				if err != nil {
					break
				}
				views = append(views, jobView{Job: j, Steps: steps})
			}
			if err == nil {
				out = map[string]any{"run": run, "jobs": views}
			}
		}
	case "kern_step_log":
		jobID, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "job_id"))
		if perr != nil {
			err = fmt.Errorf("job_id must be a UUID")
			break
		}
		n := mcpArgInt(call.Arguments, "step", 0)
		tail := mcpArgInt(call.Arguments, "tail", 8192)
		if tail <= 0 {
			tail = 8192
		}
		var ref string
		var size int64
		ref, size, err = s.Store.StepLogRef(ctx, jobID, n)
		if err == nil && ref == "" {
			// Skipped/cancelled steps never posted a log — report the
			// conclusion as the log body instead of an isError result.
			var steps []store.Step
			steps, err = s.Store.StepsForJob(ctx, jobID)
			if err != nil {
				break
			}
			conclusion := ""
			for _, st := range steps {
				if st.Number == n {
					conclusion = st.Conclusion
				}
			}
			if conclusion == "skipped" || conclusion == "cancelled" {
				text := "KERN: step " + conclusion + "\n"
				out = map[string]any{"step": n, "offset": 0, "bytes": len(text),
					"total_size": len(text), "next_offset": 0, "log": text}
			} else {
				err = fmt.Errorf("step has no log")
			}
		}
		if err == nil && out == nil {
			offset := int64(0)
			if o := mcpArgInt(call.Arguments, "offset", -1); o >= 0 {
				offset = int64(o)
			} else if size > int64(tail) {
				offset = size - int64(tail)
			}
			var data []byte
			data, _, err = s.Blobs.ReadFrom(ref, offset, tail)
			if err == nil {
				out = map[string]any{"step": n, "offset": offset, "bytes": len(data),
					"total_size": size, "next_offset": offset + int64(len(data)),
					"log": string(data)}
			}
		}
	case "kern_list_runners":
		var runners []store.Runner
		runners, err = s.Store.Runners(ctx, user.OrgID)
		if err == nil {
			type runnerView struct {
				store.Runner
				ActiveJobs []store.Job `json:"active_jobs"`
			}
			views := []runnerView{}
			for _, r := range runners {
				var jobs []store.Job
				jobs, err = s.Store.RunnerActiveJobs(ctx, r.ID)
				if err != nil {
					break
				}
				views = append(views, runnerView{Runner: r, ActiveJobs: jobs})
			}
			if err == nil {
				out = views
			}
		}
	case "kern_get_project":
		slug := mcpArgStringOr(call.Arguments, "project")
		if slug == "" {
			err = fmt.Errorf("project is required")
			break
		}
		var p store.Project
		p, err = s.Store.Project(ctx, user.OrgID, slug)
		if err == nil {
			var wfs []store.Workflow
			wfs, err = s.Store.Workflows(ctx, p.ID)
			if err == nil {
				out = map[string]any{"project": p, "workflows": wfs}
			}
		}
	case "kern_trigger_run":
		// Mirror POST /projects/:project/workflows/:id/runs (write scope).
		if !scopeAllows(c, "write") {
			err = fmt.Errorf("token requires the write scope")
			break
		}
		slug := mcpArgStringOr(call.Arguments, "project")
		want := mcpArgStringOr(call.Arguments, "workflow")
		if slug == "" || want == "" {
			err = fmt.Errorf("project and workflow are required")
			break
		}
		var p store.Project
		p, err = s.Store.Project(ctx, user.OrgID, slug)
		if err != nil {
			break
		}
		var wf store.Workflow
		wf, err = s.mcpResolveWorkflow(ctx, p.ID, want)
		if err != nil {
			break
		}
		event := mcpArgStringOr(call.Arguments, "event")
		if event == "" {
			event = "workflow_dispatch"
		}
		ref := mcpArgStringOr(call.Arguments, "ref")
		if ref == "" {
			ref = "refs/heads/" + p.DefaultBranch
		}
		var version store.WorkflowVersion
		wf, version, err = s.Store.LatestWorkflowVersion(ctx, p.ID, wf.Path)
		if err != nil {
			break
		}
		if !version.Valid {
			err = fmt.Errorf("stored workflow version is invalid; fix the diagnostics and sync again")
			break
		}
		var inputs map[string]any
		if raw, ok := mcpArgRaw(call.Arguments, "inputs"); ok {
			if uerr := json.Unmarshal(raw, &inputs); uerr != nil {
				err = fmt.Errorf("inputs must be a JSON object")
				break
			}
		}
		source := map[string]any{
			"repo_url":       p.RepoURL,
			"repo_full_name": p.RepoFullName,
			"local_path":     p.LocalPath,
			"default_branch": p.DefaultBranch,
		}
		// HTTP parity: resolve the ref's current head and compile the
		// workflow as it exists there — the stored version goes stale
		// between syncs.
		sha := s.resolveTriggerSHA(ctx, p, ref, mcpArgStringOr(call.Arguments, "sha"))
		if p.RepoFullName != "" && sha != "" {
			if v, _, verr := s.workflowAtSHA(c, p.RepoFullName, p.ID, wf, sha); verr == nil {
				version = v
			}
		}
		// A manual dispatch against a PR ref carries the same pull_request
		// payload fields the webhook path synthesizes.
		if (event == "pull_request" || event == "pull_request_target") && p.RepoFullName != "" {
			if num, ok := prNumberFromRef(ref); ok {
				if gh, gerr := s.githubClient(ctx, p.RepoFullName); gerr == nil {
					if pr, perr := gh.PullRequest(ctx, p.RepoFullName, num); perr == nil {
						source["pr_number"] = pr.Number
						source["head_ref"] = pr.Head.Ref
						source["head_sha"] = pr.Head.SHA
						source["head_repo"] = pr.Head.RepoFullName
						source["base_ref"] = pr.Base.Ref
						source["base_sha"] = pr.Base.SHA
					}
				}
			}
		}
		// Label-pool tokens may only dispatch workflows that fit their pool.
		if labels := tokenLabels(c); len(labels) > 0 {
			var plan workflows.Plan
			if len(version.Parsed) == 0 || json.Unmarshal(version.Parsed, &plan) != nil {
				err = fmt.Errorf("this token is limited to a runner pool and the workflow plan could not be checked")
				break
			}
			if msg := runsOnDenial(labels, plan.Jobs); msg != "" {
				err = fmt.Errorf("%s", msg)
				break
			}
		}
		var run store.Run
		run, err = s.Orch.StartRun(ctx, store.CreateRunParams{
			ProjectID:         p.ID,
			WorkflowID:        wf.ID,
			WorkflowVersionID: version.ID,
			Event:             event,
			Ref:               ref,
			SHA:               firstNonEmpty(sha, version.CommitSHA),
			Actor:             user.Email,
			TrustLevel:        "trusted",
			Source:            source,
			Inputs:            inputs,
			DispatchTokenID:   dispatchTokenID(c),
		})
		if err == nil {
			s.Store.Audit(ctx, user.OrgID, &user.ID, "run.trigger", run.ID.String())
			out = run
		}
	case "kern_rerun_run":
		// Mirror POST /runs/:id/rerun (write scope).
		if !scopeAllows(c, "write") {
			err = fmt.Errorf("token requires the write scope")
			break
		}
		id, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		orig, rerr := s.Store.Run(ctx, id)
		if rerr != nil {
			err = rerr
			break
		}
		// failed=true or jobs=[name] re-run the finished run's jobs in
		// place — gh rerun --failed semantics.
		failed, _ := mcpArgBool(call.Arguments, "failed")
		var jobNames []string
		if raw, ok := mcpArgRaw(call.Arguments, "jobs"); ok {
			_ = json.Unmarshal(raw, &jobNames)
		}
		if failed || len(jobNames) > 0 {
			n, rjerr := s.Store.RerunJobs(ctx, orig.ID, jobNames)
			if rjerr != nil {
				err = rjerr
				break
			}
			s.Store.Audit(ctx, user.OrgID, &user.ID, "run.rerun_jobs", orig.ID.String())
			var run store.Run
			run, err = s.Store.Run(ctx, orig.ID)
			if err == nil {
				out = map[string]any{"run": run, "jobs": n}
			}
			break
		}
		var source map[string]any
		_ = json.Unmarshal(orig.Source, &source)
		var run store.Run
		run, err = s.Orch.StartRun(ctx, store.CreateRunParams{
			ProjectID:         orig.ProjectID,
			WorkflowID:        orig.WorkflowID,
			WorkflowVersionID: orig.WorkflowVersionID,
			Event:             orig.Event,
			Ref:               orig.Ref,
			SHA:               orig.SHA,
			CommitMessage:     orig.CommitMessage,
			Actor:             user.Email,
			TrustLevel:        orig.TrustLevel,
			Source:            source,
			ConcurrencyGroup:  orig.ConcurrencyGroup,
			DispatchTokenID:   dispatchTokenID(c),
		})
		if err == nil {
			// Re-triggering a cancelled run retires the cancelled entry
			// from the default lists; the row stays reachable by id.
			if orig.Status == "cancelled" || orig.Conclusion == "cancelled" {
				_, _ = s.Store.ClearRun(ctx, orig.ID)
			}
			s.Store.Audit(ctx, user.OrgID, &user.ID, "run.rerun", orig.ID.String())
			out = run
		}
	case "kern_list_secrets":
		// Names and scopes only — secret values never leave the store.
		var projectID, envID *uuid.UUID
		if slug := mcpArgStringOr(call.Arguments, "project"); slug != "" {
			p, perr := s.Store.Project(ctx, user.OrgID, slug)
			if perr != nil {
				err = perr
				break
			}
			projectID = &p.ID
		}
		if envName := mcpArgStringOr(call.Arguments, "environment"); envName != "" {
			if projectID == nil {
				err = fmt.Errorf("environment scope requires project")
				break
			}
			env, eerr := s.Store.Environment(ctx, *projectID, envName)
			if eerr != nil {
				err = eerr
				break
			}
			envID = &env.ID
		}
		var rows []store.Secret
		rows, err = s.Store.Secrets(ctx, user.OrgID, projectID, envID)
		if err == nil {
			out = map[string]any{"secrets": secretSummaries(rows)}
		}
	case "kern_set_runner_drain":
		// Mirror POST /runners/:id/drain — admin scope plus admin role.
		if !scopeAllows(c, "admin") {
			err = fmt.Errorf("token requires the admin scope")
			break
		}
		if roleRank(user.Role) < roleRank("admin") {
			err = fmt.Errorf("requires the admin role")
			break
		}
		name := mcpArgStringOr(call.Arguments, "runner")
		draining, dok := mcpArgBool(call.Arguments, "draining")
		if name == "" || !dok {
			err = fmt.Errorf("runner and draining are required")
			break
		}
		runners, rerr := s.Store.Runners(ctx, user.OrgID)
		if rerr != nil {
			err = rerr
			break
		}
		var target *store.Runner
		for i := range runners {
			if runners[i].ID.String() == name || runners[i].Name == name {
				target = &runners[i]
				break
			}
		}
		if target == nil {
			err = fmt.Errorf("runner %q not found", name)
			break
		}
		if derr := s.Store.SetRunnerDraining(ctx, user.OrgID, target.ID, draining); derr != nil {
			err = derr
			break
		}
		s.Store.Audit(ctx, user.OrgID, &user.ID, "runner.drain",
			fmt.Sprintf("%s draining=%t", target.ID, draining))
		out = map[string]any{"runner": target.Name, "id": target.ID, "draining": draining}
	case "kern_approve_run", "kern_cancel_run":
		// Mirror the HTTP gates: cancel needs write scope, approve needs
		// admin scope plus the admin role (same as POST /runs/:id/approve).
		need := "write"
		if call.Name == "kern_approve_run" {
			need = "admin"
		}
		if !scopeAllows(c, need) {
			err = fmt.Errorf("token requires the %s scope", need)
			break
		}
		id, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		if call.Name == "kern_approve_run" {
			if roleRank(user.Role) < roleRank("admin") {
				err = fmt.Errorf("requires the admin role")
				break
			}
			err = s.Store.ApproveRunHold(ctx, id)
			if err == nil {
				s.Store.Audit(ctx, user.OrgID, &user.ID, "run.approve", id.String())
				out = mcpText("run %s approved", id)
			}
		} else {
			var run store.Run
			run, err = s.Store.CancelRun(ctx, id)
			if err == nil {
				s.Store.Audit(ctx, user.OrgID, &user.ID, "run.cancel", id.String())
				out = run
			}
		}
	case "kern_diagnose_run":
		runID, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		out, err = s.mcpDiagnoseRun(ctx, user, runID,
			mcpArgInt(call.Arguments, "tail", 8192),
			mcpArgInt(call.Arguments, "max_steps", 5))
	case "kern_search_logs":
		q := strings.TrimSpace(mcpArgStringOr(call.Arguments, "q"))
		if q == "" {
			err = fmt.Errorf("q is required")
			break
		}
		var runID uuid.UUID
		if v := mcpArgStringOr(call.Arguments, "run"); v != "" {
			var perr error
			runID, perr = uuid.Parse(v)
			if perr != nil {
				err = fmt.Errorf("run must be a uuid")
				break
			}
		}
		sctx, cancel := context.WithTimeout(ctx, logSearchDeadline)
		hits, stepsSeen, scanned, truncated, lerr := s.logSearchHits(sctx, user.OrgID, q,
			mcpArgStringOr(call.Arguments, "project"), runID,
			mcpArgInt(call.Arguments, "limit", logSearchDefaultHit))
		cancel()
		if lerr != nil {
			err = lerr
			break
		}
		out = map[string]any{"hits": hits, "steps_scanned": stepsSeen,
			"bytes_scanned": scanned, "truncated": truncated}
	case "kern_wait_run":
		runID, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		timeout := mcpArgInt(call.Arguments, "timeout", 120)
		if timeout <= 0 || timeout > 600 {
			timeout = 120
		}
		out, err = s.mcpWaitRun(ctx, runID, time.Duration(timeout)*time.Second)
	case "kern_list_artifacts":
		runID, perr := uuid.Parse(mcpArgStringOr(call.Arguments, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		out, err = s.Store.Artifacts(ctx, user.OrgID, &runID, 200)
	default:
		r := rpcErr(req.ID, -32602, "unknown tool "+call.Name)
		return &r
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, store.ErrNotFound) || strings.Contains(msg, "no rows") {
			msg = "not found"
		}
		r := rpcOK(req.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": msg}},
		})
		return &r
	}
	var env map[string]any
	if m, ok := out.(map[string]any); ok && m["content"] != nil {
		env = m
	} else {
		env, err = mcpJSON(out)
		if err != nil {
			r := rpcErr(req.ID, -32603, "encode result")
			return &r
		}
	}
	r := rpcOK(req.ID, env)
	return &r
}

// mcpToolAllowed confines a project-scoped token to its projects — the same
// boundary constrainGrant draws on the REST surface. Unscoped tokens pass.
func (s *Server) mcpToolAllowed(c *gin.Context, name string, args map[string]json.RawMessage) error {
	granted := tokenProjects(c)
	if len(granted) == 0 {
		return nil
	}
	ctx := c.Request.Context()
	runProjectSlug := func(runID uuid.UUID) (string, bool) {
		run, err := s.Store.Run(ctx, runID)
		if err != nil {
			return "", false
		}
		p, err := s.Store.ProjectByID(ctx, run.ProjectID)
		if err != nil {
			return "", false
		}
		return p.Slug, true
	}
	runAllowed := func(key string) bool {
		id, err := uuid.Parse(mcpArgStringOr(args, key))
		if err != nil {
			return false
		}
		slug, ok := runProjectSlug(id)
		return ok && grantHas(granted, slug)
	}
	projectDenied := fmt.Errorf("project must be one of this token's projects")
	switch name {
	case "kern_list_projects":
		// Result rows are filtered to granted slugs in the handler.
	case "kern_list_runs", "kern_get_project", "kern_trigger_run":
		if !grantHas(granted, mcpArgStringOr(args, "project")) {
			return projectDenied
		}
	case "kern_get_run", "kern_rerun_run", "kern_approve_run", "kern_cancel_run",
		"kern_diagnose_run", "kern_wait_run", "kern_list_artifacts":
		if !runAllowed("run_id") {
			return fmt.Errorf("run is outside this token's projects")
		}
	case "kern_search_logs":
		if run := mcpArgStringOr(args, "run"); run != "" {
			if !runAllowed("run") {
				return fmt.Errorf("run is outside this token's projects")
			}
		} else if !grantHas(granted, mcpArgStringOr(args, "project")) {
			return projectDenied
		}
	case "kern_step_log":
		jobID, perr := uuid.Parse(mcpArgStringOr(args, "job_id"))
		if perr != nil {
			return fmt.Errorf("job_id must be a UUID")
		}
		job, jerr := s.Store.Job(ctx, jobID)
		if jerr != nil {
			return fmt.Errorf("job is outside this token's projects")
		}
		slug, ok := runProjectSlug(job.RunID)
		if !ok || !grantHas(granted, slug) {
			return fmt.Errorf("job is outside this token's projects")
		}
	case "kern_list_secrets":
		if slug := mcpArgStringOr(args, "project"); slug == "" || !grantHas(granted, slug) {
			return projectDenied
		}
	default:
		return fmt.Errorf("tool %q is not available to project-scoped tokens", name)
	}
	return nil
}

func mcpArgStringOr(args map[string]json.RawMessage, key string) string {
	s, _ := mcpArgString(args, key)
	return s
}

// mcpArgRaw returns the raw JSON value of one tool argument.
func mcpArgRaw(args map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	raw, ok := args[key]
	return raw, ok
}

func mcpArgBool(args map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := mcpArgRaw(args, key)
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return b, true
}

// mcpResolveWorkflow accepts a workflow UUID, exact path/name, or basename —
// the same resolution the CLI applies for `kern runs trigger`.
func (s *Server) mcpResolveWorkflow(ctx context.Context, projectID uuid.UUID, want string) (store.Workflow, error) {
	if id, perr := uuid.Parse(want); perr == nil {
		wf, err := s.Store.WorkflowByID(ctx, id)
		if err != nil || wf.ProjectID != projectID {
			return store.Workflow{}, fmt.Errorf("workflow not found in this project")
		}
		return wf, nil
	}
	wfs, err := s.Store.Workflows(ctx, projectID)
	if err != nil {
		return store.Workflow{}, err
	}
	var matches []store.Workflow
	for _, w := range wfs {
		if w.Path == want || w.Name == want || path.Base(w.Path) == want {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 0:
		return store.Workflow{}, fmt.Errorf("no workflow matching %q", want)
	case 1:
		return matches[0], nil
	default:
		return store.Workflow{}, fmt.Errorf("workflow %q is ambiguous", want)
	}
}

// mcpDiagnoseRun builds the single-call failure bundle kern_diagnose_run
// returns: run state, jobs with their steps, tails of the failed steps'
// logs, and the runner map an agent needs to triage in one round trip.
func (s *Server) mcpDiagnoseRun(ctx context.Context, user store.User, runID uuid.UUID, tail, maxSteps int) (map[string]any, error) {
	if tail <= 0 || tail > 1<<20 {
		tail = 8192
	}
	if maxSteps <= 0 || maxSteps > 20 {
		maxSteps = 5
	}
	run, err := s.Store.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	jobs, err := s.Store.JobsForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	type stepView struct {
		store.Step
		LogTail string `json:"log_tail,omitempty"`
	}
	type jobView struct {
		store.Job
		Steps      []stepView `json:"steps"`
		RunnerName string     `json:"runner_name,omitempty"`
	}
	runners, _ := s.Store.Runners(ctx, user.OrgID)
	names := map[uuid.UUID]string{}
	for _, r := range runners {
		names[r.ID] = r.Name
	}
	views := make([]jobView, 0, len(jobs))
	tailed := 0
	for _, j := range jobs {
		steps, err := s.Store.StepsForJob(ctx, j.ID)
		if err != nil {
			return nil, err
		}
		sv := make([]stepView, 0, len(steps))
		for _, st := range steps {
			v := stepView{Step: st}
			if tailed < maxSteps && st.Conclusion == "failure" && st.LogRef != "" {
				size := st.LogBytes
				offset := int64(0)
				if size > int64(tail) {
					offset = size - int64(tail)
				}
				if data, _, rerr := s.Blobs.ReadFrom(st.LogRef, offset, tail); rerr == nil {
					v.LogTail = string(data)
					tailed++
				}
			}
			sv = append(sv, v)
		}
		jv := jobView{Job: j, Steps: sv}
		if j.RunnerID != nil {
			jv.RunnerName = names[*j.RunnerID]
		}
		views = append(views, jv)
	}
	return map[string]any{"run": run, "jobs": views}, nil
}

// mcpWaitRun polls until the run reaches a terminal status or the timeout
// lapses — an agent blocking on a result burns one call instead of polling.
func (s *Server) mcpWaitRun(ctx context.Context, runID uuid.UUID, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		run, err := s.Store.Run(ctx, runID)
		if err != nil {
			return nil, err
		}
		switch run.Status {
		case "completed", "cancelled":
			return map[string]any{"run": run, "terminal": true}, nil
		}
		if time.Now().After(deadline) {
			return map[string]any{"run": run, "terminal": false}, nil
		}
		select {
		case <-ctx.Done():
			return map[string]any{"run": run, "terminal": false, "note": "client disconnected"}, nil
		case <-time.After(2 * time.Second):
		}
	}
}
