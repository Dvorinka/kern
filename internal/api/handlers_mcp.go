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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
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
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) mcp(c *gin.Context) {
	var req rpcRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, rpcErr(nil, -32700, "parse error"))
		return
	}
	// Notifications carry no id — the spec wants 202 with no body.
	if len(req.ID) == 0 {
		c.Status(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		c.JSON(http.StatusOK, rpcOK(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"serverInfo":      map[string]any{"name": "kern", "version": "1"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}))
	case "ping":
		c.JSON(http.StatusOK, rpcOK(req.ID, map[string]any{}))
	case "tools/list":
		c.JSON(http.StatusOK, rpcOK(req.ID, map[string]any{"tools": mcpTools()}))
	case "tools/call":
		s.mcpCall(c, req)
	default:
		c.JSON(http.StatusOK, rpcErr(req.ID, -32601, "method not found"))
	}
}

func props(defs map[string]map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": defs}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func mcpTools() []mcpTool {
	return []mcpTool{
		{Name: "kern_list_projects", Description: "List CI projects in the org.",
			InputSchema: props(map[string]map[string]any{})},
		{Name: "kern_list_runs", Description: "List workflow runs, newest first.",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"project": str("project slug to filter by"),
					"status":  str("queued | in_progress | completed | cancelled"),
					"limit":   num("max runs, default 20"),
				})
				return p
			}()},
		{Name: "kern_get_run", Description: "Run detail with every job and its steps (status, conclusion, diagnosis).",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_step_log", Description: "Step log bytes. Default: tail of the log; pass offset to page forward instead.",
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
			InputSchema: props(map[string]map[string]any{})},
		{Name: "kern_set_runner_drain", Description: "Drain or undrain a runner (id or name). Draining runners take no new jobs.",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"runner":   str("runner UUID or name"),
					"draining": map[string]any{"type": "boolean", "description": "true to drain, false to undrain"},
				})
				p["required"] = []string{"runner", "draining"}
				return p
			}()},
		{Name: "kern_get_project", Description: "Project detail with its workflows.",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"project": str("project slug")})
				p["required"] = []string{"project"}
				return p
			}()},
		{Name: "kern_trigger_run", Description: "Start a workflow run (workflow_dispatch by default).",
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
		{Name: "kern_rerun_run", Description: "Re-run a completed or failed run from its original inputs.",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_list_secrets", Description: "Secret names and scopes for the org — never values. Optionally filtered by project slug and/or environment name.",
			InputSchema: props(map[string]map[string]any{
				"project":     str("project slug"),
				"environment": str("environment name (requires project)"),
			})},
		{Name: "kern_approve_run", Description: "Release a fork-gated run (awaiting_approval -> queued).",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{"run_id": str("run UUID")})
				p["required"] = []string{"run_id"}
				return p
			}()},
		{Name: "kern_cancel_run", Description: "Cancel a queued or in-progress run.",
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

func mcpArgString(params json.RawMessage, key string) (string, bool) {
	var call struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &call) != nil || call.Arguments == nil {
		return "", false
	}
	raw, ok := call.Arguments[key]
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

func mcpArgInt(params json.RawMessage, key string, def int) int {
	var call struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &call) != nil {
		return def
	}
	raw, ok := call.Arguments[key]
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

func (s *Server) mcpCall(c *gin.Context, req rpcRequest) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		c.JSON(http.StatusOK, rpcErr(req.ID, -32602, "invalid params"))
		return
	}
	// Repack arguments under the key helpers expect.
	params := req.Params
	user, _ := currentUser(c)
	ctx := c.Request.Context()
	var out any
	var err error
	switch call.Name {
	case "kern_list_projects":
		var projects []store.Project
		projects, err = s.Store.Projects(ctx, user.OrgID)
		out = projects
	case "kern_list_runs":
		f := store.RunFilter{Status: mcpArgStringOr(params, "status"), Limit: mcpArgInt(params, "limit", 20)}
		if slug := mcpArgStringOr(params, "project"); slug != "" {
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
		id, perr := uuid.Parse(mcpArgStringOr(params, "run_id"))
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
				steps, _ := s.Store.StepsForJob(ctx, j.ID)
				views = append(views, jobView{Job: j, Steps: steps})
			}
			out = map[string]any{"run": run, "jobs": views}
		}
	case "kern_step_log":
		jobID, perr := uuid.Parse(mcpArgStringOr(params, "job_id"))
		if perr != nil {
			err = fmt.Errorf("job_id must be a UUID")
			break
		}
		n := mcpArgInt(params, "step", 0)
		tail := mcpArgInt(params, "tail", 8192)
		var ref string
		var size int64
		ref, size, err = s.Store.StepLogRef(ctx, jobID, n)
		if err == nil && ref == "" {
			err = fmt.Errorf("step has no log")
		}
		if err == nil {
			offset := int64(0)
			if o := mcpArgInt(params, "offset", -1); o >= 0 {
				offset = int64(o)
			} else if size > int64(tail) {
				offset = size - int64(tail)
			}
			var data []byte
			data, _, err = s.Blobs.ReadFrom(ref, offset, tail)
			if err == nil {
				out = map[string]any{"step": n, "offset": offset, "bytes": len(data), "log": string(data)}
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
				jobs, _ := s.Store.RunnerActiveJobs(ctx, r.ID)
				views = append(views, runnerView{Runner: r, ActiveJobs: jobs})
			}
			out = views
		}
	case "kern_get_project":
		slug := mcpArgStringOr(params, "project")
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
		slug := mcpArgStringOr(params, "project")
		want := mcpArgStringOr(params, "workflow")
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
		event := mcpArgStringOr(params, "event")
		if event == "" {
			event = "workflow_dispatch"
		}
		ref := mcpArgStringOr(params, "ref")
		if ref == "" {
			ref = "refs/heads/" + p.DefaultBranch
		}
		trust := mcpArgStringOr(params, "trust_level")
		if trust == "" {
			trust = "trusted"
		}
		validTrust := map[string]bool{
			"trusted": true, "protected": true, "pull_request": true,
			"fork": true, "deployment": true,
		}
		if !validTrust[trust] {
			err = fmt.Errorf("unknown trust level %q", trust)
			break
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
		if raw, ok := mcpArgRaw(params, "inputs"); ok {
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
		var run store.Run
		run, err = s.Orch.StartRun(ctx, store.CreateRunParams{
			ProjectID:         p.ID,
			WorkflowID:        wf.ID,
			WorkflowVersionID: version.ID,
			Event:             event,
			Ref:               ref,
			SHA:               firstNonEmpty(mcpArgStringOr(params, "sha"), version.CommitSHA),
			Actor:             user.Email,
			TrustLevel:        trust,
			Source:            source,
			Inputs:            inputs,
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
		id, perr := uuid.Parse(mcpArgStringOr(params, "run_id"))
		if perr != nil {
			err = fmt.Errorf("run_id must be a UUID")
			break
		}
		orig, rerr := s.Store.Run(ctx, id)
		if rerr != nil {
			err = rerr
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
		})
		if err == nil {
			s.Store.Audit(ctx, user.OrgID, &user.ID, "run.rerun", orig.ID.String())
			out = run
		}
	case "kern_list_secrets":
		// Names and scopes only — secret values never leave the store.
		var projectID, envID *uuid.UUID
		if slug := mcpArgStringOr(params, "project"); slug != "" {
			p, perr := s.Store.Project(ctx, user.OrgID, slug)
			if perr != nil {
				err = perr
				break
			}
			projectID = &p.ID
		}
		if envName := mcpArgStringOr(params, "environment"); envName != "" {
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
		name := mcpArgStringOr(params, "runner")
		draining, dok := mcpArgBool(params, "draining")
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
		id, perr := uuid.Parse(mcpArgStringOr(params, "run_id"))
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
	default:
		c.JSON(http.StatusOK, rpcErr(req.ID, -32602, "unknown tool "+call.Name))
		return
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "no rows") {
			msg = "not found"
		}
		c.JSON(http.StatusOK, rpcOK(req.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": msg}},
		}))
		return
	}
	var env map[string]any
	if m, ok := out.(map[string]any); ok && m["content"] != nil {
		env = m
	} else {
		env, err = mcpJSON(out)
		if err != nil {
			c.JSON(http.StatusOK, rpcErr(req.ID, -32603, "encode result"))
			return
		}
	}
	c.JSON(http.StatusOK, rpcOK(req.ID, env))
}

func mcpArgStringOr(params json.RawMessage, key string) string {
	s, _ := mcpArgString(params, key)
	return s
}

// mcpArgRaw returns the raw JSON value of one tool argument.
func mcpArgRaw(params json.RawMessage, key string) (json.RawMessage, bool) {
	var call struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &call) != nil || call.Arguments == nil {
		return nil, false
	}
	raw, ok := call.Arguments[key]
	return raw, ok
}

func mcpArgBool(params json.RawMessage, key string) (bool, bool) {
	raw, ok := mcpArgRaw(params, key)
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
