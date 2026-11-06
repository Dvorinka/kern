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
	"encoding/json"
	"fmt"
	"net/http"
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
		{Name: "kern_step_log", Description: "Tail of a step's log.",
			InputSchema: func() map[string]any {
				p := props(map[string]map[string]any{
					"job_id": str("job UUID"),
					"step":   num("step number"),
					"tail":   num("bytes from the end, default 8192"),
				})
				p["required"] = []string{"job_id", "step"}
				return p
			}()},
		{Name: "kern_list_runners", Description: "Fleet runners with status, labels and active jobs.",
			InputSchema: props(map[string]map[string]any{})},
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
		Arguments map[string]int `json:"arguments"`
	}
	if json.Unmarshal(params, &call) != nil || call.Arguments == nil {
		return def
	}
	if v, ok := call.Arguments[key]; ok {
		return v
	}
	return def
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
			if size > int64(tail) {
				offset = size - int64(tail)
			}
			var data []byte
			data, _, err = s.Blobs.ReadFrom(ref, offset, tail)
			if err == nil {
				out = map[string]any{"step": n, "bytes": len(data), "log": string(data)}
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
