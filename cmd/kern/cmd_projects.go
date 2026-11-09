package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (c *cli) cmdProjects(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern projects list|show|create|sync|workflows")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		var out struct {
			Projects []project `json:"projects"`
		}
		if err := c.api().get(ctx, "/projects", nil, &out); err != nil {
			return err
		}
		c.orJSON(out.Projects, func() {
			rows := make([][]string, 0, len(out.Projects))
			for _, p := range out.Projects {
				src := p.RepoURL
				if src == "" {
					src = p.LocalPath
				}
				rows = append(rows, []string{p.Slug, p.Name, src, p.DefaultBranch})
			}
			table([]string{"SLUG", "NAME", "SOURCE", "BRANCH"}, rows)
		})
	case "show", "get":
		slug, err := strarg(rest, 0, "project slug")
		if err != nil {
			return err
		}
		var out struct {
			Project   project    `json:"project"`
			Workflows []workflow `json:"workflows"`
		}
		if err := c.api().get(ctx, "/projects/"+slug, nil, &out); err != nil {
			return err
		}
		if c.json {
			printJSON(out)
			return nil
		}
		p := out.Project
		fmt.Printf("%s (%s)\n  source: %s%s  branch: %s\n", p.Name, p.Slug,
			p.RepoURL, p.LocalPath, p.DefaultBranch)
		rows := make([][]string, 0, len(out.Workflows))
		for _, w := range out.Workflows {
			rows = append(rows, []string{w.ID, w.Name, w.Path})
		}
		table([]string{"ID", "NAME", "PATH"}, rows)
	case "create":
		opts, _ := parseFlags(rest, "slug", "name", "repo-url", "repo-full-name", "local-path", "branch")
		if opts["slug"] == "" {
			return fmt.Errorf("--slug is required")
		}
		if opts["repo-url"] == "" && opts["local-path"] == "" {
			return fmt.Errorf("--repo-url or --local-path is required")
		}
		var out struct {
			Project project `json:"project"`
		}
		err := c.api().post(ctx, "/projects", map[string]any{
			"slug": opts["slug"], "name": opts["name"], "repo_url": opts["repo-url"],
			"repo_full_name": opts["repo-full-name"], "local_path": opts["local-path"],
			"default_branch": opts["branch"],
		}, &out)
		if err != nil {
			return err
		}
		if c.json {
			printJSON(out.Project)
		} else {
			fmt.Printf("project %s created\n", out.Project.Slug)
		}
	case "sync":
		slug, err := strarg(rest, 0, "project slug")
		if err != nil {
			return err
		}
		var out struct {
			SHA       string `json:"sha"`
			Workflows []struct {
				Path        string `json:"path"`
				Name        string `json:"name"`
				Valid       bool   `json:"valid"`
				Diagnostics []struct {
					Level   string `json:"level"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"diagnostics"`
			} `json:"workflows"`
		}
		if err := c.api().post(ctx, "/projects/"+slug+"/sync", map[string]any{}, &out); err != nil {
			return err
		}
		if c.json {
			printJSON(out)
			return nil
		}
		fmt.Printf("synced at %s\n", out.SHA)
		rows := make([][]string, 0, len(out.Workflows))
		for _, w := range out.Workflows {
			status := "ok"
			if !w.Valid {
				status = "invalid"
			}
			rows = append(rows, []string{w.Path, w.Name, status, fmt.Sprintf("%d", len(w.Diagnostics))})
			for _, d := range w.Diagnostics {
				rows = append(rows, []string{"", "", d.Level + ": " + d.Code, d.Message})
			}
		}
		table([]string{"PATH", "NAME", "STATUS", "DIAGS"}, rows)
	case "workflows":
		slug, err := strarg(rest, 0, "project slug")
		if err != nil {
			return err
		}
		var out struct {
			Workflows []workflow `json:"workflows"`
		}
		if err := c.api().get(ctx, "/projects/"+slug+"/workflows", nil, &out); err != nil {
			return err
		}
		c.orJSON(out.Workflows, func() {
			rows := make([][]string, 0, len(out.Workflows))
			for _, w := range out.Workflows {
				rows = append(rows, []string{w.ID, w.Name, w.Path})
			}
			table([]string{"ID", "NAME", "PATH"}, rows)
		})
	default:
		return fmt.Errorf("unknown projects subcommand %q", sub)
	}
	return nil
}

func (c *cli) cmdRuns(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern runs list|show|trigger|cancel|approve|rerun|logs|wait")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		opts, pos := parseFlags(rest, "status", "limit", "offset", "project", "ref", "pr", "workflow")
		project := first(opts["project"], c.project)
		q := url.Values{}
		for _, k := range []string{"status", "limit", "offset", "ref", "pr", "workflow"} {
			if v := opts[k]; v != "" {
				q.Set(k, v)
			}
		}
		if len(pos) > 0 {
			project = pos[0]
		}
		if project != "" {
			q.Set("project", project)
		}
		var out struct {
			Runs  []run `json:"runs"`
			Total int   `json:"total"`
		}
		if err := c.api().get(ctx, "/runs", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Runs, func() {
			rows := make([][]string, 0, len(out.Runs))
			for _, r := range out.Runs {
				rows = append(rows, []string{shortID(r.ID), fmt.Sprintf("%d", r.RunNumber),
					r.ProjectSlug, r.WorkflowName, r.Status, r.Conclusion, r.Event, relT(r.CreatedAt)})
			}
			table([]string{"ID", "#", "PROJECT", "WORKFLOW", "STATUS", "CONCL", "EVENT", "AGE"}, rows)
		})
	case "show":
		id, err := strarg(rest, 0, "run id")
		if err != nil {
			return err
		}
		return c.showRun(ctx, id)
	case "wait":
		opts, pos := parseFlags(rest, "interval", "timeout")
		id, err := strarg(pos, 0, "run id")
		if err != nil {
			return err
		}
		return c.waitRun(ctx, id, opts["interval"], opts["timeout"])
	case "trigger":
		opts, pos := parseFlags(rest, "event", "ref", "sha", "inputs")
		var slug, wf string
		switch len(pos) {
		case 1:
			slug, wf = c.project, pos[0]
		case 2:
			slug, wf = pos[0], pos[1]
		default:
			return fmt.Errorf("usage: kern runs trigger [<project>] <workflow> [--event] [--ref] [--sha] [--inputs '{\"k\":\"v\"}']")
		}
		if slug == "" {
			return fmt.Errorf("project required — positional arg, --project or kern config set project")
		}
		workflowID, err := c.resolveWorkflow(ctx, slug, wf)
		if err != nil {
			return err
		}
		body := map[string]any{
			"event": opts["event"], "ref": opts["ref"], "sha": opts["sha"],
		}
		if raw := opts["inputs"]; raw != "" {
			var inputs map[string]any
			if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
				return fmt.Errorf("--inputs must be a JSON object: %w", err)
			}
			body["inputs"] = inputs
		}
		var out struct {
			Run run `json:"run"`
		}
		err = c.api().post(ctx, "/projects/"+slug+"/workflows/"+workflowID+"/runs", body, &out)
		if err != nil {
			return err
		}
		if c.json {
			printJSON(out.Run)
		} else {
			fmt.Printf("run %s (#%d) triggered\n", out.Run.ID, out.Run.RunNumber)
		}
	case "cancel", "approve", "rerun":
		id, err := strarg(rest, 0, "run id")
		if err != nil {
			return err
		}
		var out struct {
			Run run `json:"run"`
		}
		if err := c.api().post(ctx, "/runs/"+id+"/"+sub, map[string]any{}, &out); err != nil {
			return err
		}
		if c.json {
			printJSON(out.Run)
		} else {
			fmt.Printf("run %s → %s\n", shortID(out.Run.ID), out.Run.Status)
		}
	case "logs":
		id, err := strarg(rest, 0, "run id")
		if err != nil {
			return err
		}
		// -f is the depot/gh spelling of --follow.
		norm := make([]string, 0, len(rest)-1)
		for _, a := range rest[1:] {
			if a == "-f" {
				a = "--follow"
			}
			norm = append(norm, a)
		}
		opts, _ := parseFlagsEx(norm, []string{"follow"}, "job", "step", "follow")
		follow := opts["follow"]
		if follow == "false" {
			follow = ""
		}
		return c.runLogs(ctx, id, opts["job"], opts["step"], follow)
	case "remediate":
		opts, pos := parseFlags(rest, "step")
		jobID, err := strarg(pos, 0, "job id")
		if err != nil {
			return err
		}
		n := atoi(opts["step"])
		if n <= 0 {
			return fmt.Errorf("--step N is required")
		}
		var out struct {
			Requeued bool `json:"requeued"`
		}
		if err := c.api().post(ctx, "/jobs/"+jobID+"/remediate",
			map[string]any{"step": n}, &out); err != nil {
			return err
		}
		fmt.Println("remediation approved; job requeued")
	default:
		return fmt.Errorf("unknown runs subcommand %q", sub)
	}
	return nil
}

func (c *cli) resolveWorkflow(ctx context.Context, slug, want string) (string, error) {
	if _, err := uuid.Parse(want); err == nil {
		return want, nil
	}
	var out struct {
		Workflows []workflow `json:"workflows"`
	}
	if err := c.api().get(ctx, "/projects/"+slug+"/workflows", nil, &out); err != nil {
		return "", err
	}
	var matches []workflow
	for _, w := range out.Workflows {
		if w.Path == want || w.Name == want || filepath.Base(w.Path) == want {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no workflow matching %q in project %s", want, slug)
	case 1:
		return matches[0].ID, nil
	}
	names := make([]string, 0, len(matches))
	for _, w := range matches {
		names = append(names, w.Path)
	}
	return "", fmt.Errorf("workflow %q is ambiguous: %s", want, strings.Join(names, ", "))
}

// waitRun polls a run until it reaches a terminal state and prints the
// outcome plus any failed jobs — the monitoring primitive scripts and
// agents use to gate on CI completion. --interval (default 5s) and
// --timeout (default 30m, 0 = forever) tune the poll.
func (c *cli) waitRun(ctx context.Context, id, intervalStr, timeoutStr string) error {
	interval := 5 * time.Second
	if intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			return fmt.Errorf("--interval: %w", err)
		}
		interval = d
	}
	var deadline time.Time
	if timeoutStr != "" && timeoutStr != "0" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("--timeout: %w", err)
		}
		deadline = time.Now().Add(d)
	}
	for {
		var out runDetail
		if err := c.api().get(ctx, "/runs/"+id, nil, &out); err != nil {
			return err
		}
		r := out.Run
		if r.Status == "completed" || r.Status == "failed" || r.Status == "cancelled" {
			if c.json {
				printJSON(out)
				return nil
			}
			fmt.Printf("run %s #%d — %s %s (%s)\n", shortID(r.ID), r.RunNumber, r.Status, r.Conclusion, r.Event)
			for _, j := range out.Jobs {
				if j.Conclusion == "failure" || j.Conclusion == "cancelled" {
					fmt.Printf("  %s: %s\n", j.Name, j.Conclusion)
				}
			}
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for run %s (still %s)", shortID(r.ID), r.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (c *cli) showRun(ctx context.Context, id string) error {
	var out runDetail
	if err := c.api().get(ctx, "/runs/"+id, nil, &out); err != nil {
		return err
	}
	if c.json {
		printJSON(out)
		return nil
	}
	r := out.Run
	fmt.Printf("run %s #%d — %s %s (%s %s)\n  project %s, workflow %s, sha %s\n",
		shortID(r.ID), r.RunNumber, r.Status, r.Conclusion, r.Event, r.Ref,
		r.ProjectSlug, r.WorkflowName, short(r.SHA, 8))
	for _, j := range out.Jobs {
		concl := j.Conclusion
		if concl == "" {
			concl = "-"
		}
		line := fmt.Sprintf("  %-28s %-11s %s", j.Name, j.Status, concl)
		if j.RunnerName != "" {
			line += "  @" + j.RunnerName
		}
		if j.Attempt > 1 {
			line += fmt.Sprintf("  attempt %d", j.Attempt)
		}
		fmt.Println(line)
		if j.QueueReason != "" {
			fmt.Printf("      → queued: %s (runs-on: %s)\n", j.QueueReason, strings.Join(j.RunsOn, ", "))
		}
		for _, st := range j.Steps {
			mark := " "
			switch st.Conclusion {
			case "success":
				mark = "✓"
			case "failure":
				mark = "✗"
			case "skipped":
				mark = "-"
			case "cancelled":
				mark = "!"
			}
			fmt.Printf("    %s %-36s %s\n", mark, st.Name, st.Status)
			if len(st.Diagnosis) > 0 {
				var d struct {
					Kind        string `json:"kind"`
					Summary     string `json:"summary"`
					Suggestion  string `json:"suggestion"`
					Fixable     bool   `json:"fixable"`
					Remediation *struct {
						Applied  bool     `json:"applied"`
						Approved bool     `json:"approved"`
						Result   string   `json:"result"`
						Fix      []string `json:"fix"`
					} `json:"remediation"`
				}
				if json.Unmarshal(st.Diagnosis, &d) == nil && d.Kind != "" {
					fmt.Printf("      → %s: %s\n        fix: %s\n", d.Kind, d.Summary, d.Suggestion)
					if d.Remediation != nil {
						fmt.Printf("        remediation: %s (applied=%v approved=%v)\n",
							d.Remediation.Result, d.Remediation.Applied, d.Remediation.Approved)
					}
				}
			}
		}
	}
	return nil
}

// runLogs pages through step logs; --follow polls until every selected step is
// terminal. Sequential per-step streaming keeps ordering readable.
func (c *cli) runLogs(ctx context.Context, runID, jobFilter, stepFilter, follow string) error {
	var out runDetail
	if err := c.api().get(ctx, "/runs/"+runID, nil, &out); err != nil {
		return err
	}
	var steps []step
	for _, j := range out.Jobs {
		if jobFilter != "" && j.Key != jobFilter && j.JobKey != jobFilter && j.Name != jobFilter {
			continue
		}
		for _, st := range j.Steps {
			if stepFilter != "" && fmt.Sprintf("%d", st.Number) != stepFilter {
				continue
			}
			steps = append(steps, st)
		}
	}
	if len(steps) == 0 {
		return fmt.Errorf("no steps matched")
	}
	for _, st := range steps {
		fmt.Printf("== %s\n", st.Name)
		offset := int64(0)
		for {
			var chunk stepLog
			q := url.Values{"offset": {fmt.Sprintf("%d", offset)}}
			if err := c.api().get(ctx, "/steps/"+st.ID+"/logs", q, &chunk); err != nil {
				return err
			}
			fmt.Print(chunk.Text)
			offset = chunk.NextOffset
			terminal := chunk.Status != "running" && chunk.Status != "queued"
			if follow == "" || (terminal && offset >= chunk.Size) {
				break
			}
			time.Sleep(800 * time.Millisecond)
		}
	}
	return nil
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func shortID(id string) string { return short(id, 8) }

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
