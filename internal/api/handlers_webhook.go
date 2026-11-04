package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

// processGithubDelivery acts on a persisted webhook delivery. Today push events
// trigger matching workflows; other events are recorded and ignored.
func (s *Server) processGithubDelivery(c *gin.Context, delivery, event string, body []byte) (status, detail string) {
	if event != "push" {
		return "ignored", "event type " + event + " is recorded but not acted on"
	}
	var p struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		HeadCommit struct {
			Message string `json:"message"`
		} `json:"head_commit"`
		Pusher struct {
			Name string `json:"name"`
		} `json:"pusher"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		Repository struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "failed", "payload is not a push event: " + err.Error()
	}
	if p.After == "" || p.After == "0000000000000000000000000000000000000000" {
		return "ignored", "branch deleted; nothing to run"
	}
	projects, err := s.Store.ProjectsByRepoFullName(c.Request.Context(), p.Repository.FullName)
	if err != nil {
		return "failed", err.Error()
	}
	if len(projects) == 0 {
		return "ignored", "no project bound to " + p.Repository.FullName
	}
	actor := p.Pusher.Name
	if actor == "" {
		actor = p.Sender.Login
	}
	var started []string
	var skipped []string
	for _, proj := range projects {
		wfs, err := s.Store.Workflows(c.Request.Context(), proj.ID)
		if err != nil {
			return "failed", err.Error()
		}
		for _, wf := range wfs {
			_, version, err := s.Store.LatestWorkflowVersion(c.Request.Context(), proj.ID, wf.Path)
			if err != nil || !version.Valid {
				continue
			}
			triggered, why := pushMatches(version.Parsed, p.Ref)
			if !triggered {
				if why != "" {
					skipped = append(skipped, wf.Name+" ("+why+")")
				}
				continue
			}
			repoURL := proj.RepoURL
			if repoURL == "" {
				repoURL = p.Repository.CloneURL
			}
			run, err := s.Orch.StartRun(c.Request.Context(), store.CreateRunParams{
				ProjectID:         proj.ID,
				WorkflowID:        wf.ID,
				WorkflowVersionID: version.ID,
				Event:             "push",
				Ref:               p.Ref,
				SHA:               p.After,
				CommitMessage:     p.HeadCommit.Message,
				Actor:             actor,
				TrustLevel:        "trusted",
				Source: map[string]any{
					"repo_url":       repoURL,
					"repo_full_name": p.Repository.FullName,
					"local_path":     proj.LocalPath,
					"default_branch": proj.DefaultBranch,
					"delivery":       delivery,
				},
			})
			if err != nil {
				skipped = append(skipped, wf.Name+" (start failed: "+err.Error()+")")
				continue
			}
			started = append(started, fmt.Sprintf("%s #%d", wf.Name, run.RunNumber))
		}
	}
	if len(started) == 0 && len(skipped) == 0 {
		return "ignored", "no workflows subscribed to push"
	}
	return "processed", fmt.Sprintf("started: %s; skipped: %s",
		strings.Join(started, ", "), strings.Join(skipped, ", "))
}

// pushMatches reports whether a stored workflow plan subscribes to a push on
// ref. It honours branches/branches-ignore filters; any other non-empty filter
// (tags, paths, types…) is a declared gap and the workflow does not trigger,
// with the reason returned for the delivery record.
func pushMatches(parsed json.RawMessage, ref string) (bool, string) {
	var plan workflows.Plan
	if json.Unmarshal(parsed, &plan) != nil {
		return false, ""
	}
	subscribed := false
	for _, e := range plan.Events {
		if e == "push" {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers["push"].(map[string]any)
	for key, v := range cfg {
		switch key {
		case "branches", "branches-ignore":
		default:
			if !emptyFilter(v) {
				return false, "unsupported push filter " + key
			}
		}
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if patterns := filterList(cfg["branches"]); len(patterns) > 0 && !matchPatterns(patterns, branch) {
		return false, "branch " + branch + " not in branches filter"
	}
	if patterns := filterList(cfg["branches-ignore"]); len(patterns) > 0 && matchPatterns(patterns, branch) {
		return false, "branch " + branch + " is ignored"
	}
	return true, ""
}

func emptyFilter(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}

func filterList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// matchPatterns applies GitHub's ordered pattern semantics: later matches win,
// a leading '!' negates the match.
func matchPatterns(patterns []string, name string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if globMatch(strings.TrimPrefix(p, "!"), name) {
			matched = !neg
		}
	}
	return matched
}

// globMatch implements the GitHub subset of fnmatch used for ref filters:
// '*' and '**' both match across '/', '?' matches one character.
func globMatch(pattern, name string) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*`, ".*")
	re = strings.ReplaceAll(re, `\?`, ".")
	ok, _ := regexp.MatchString("^"+re+"$", name)
	return ok
}
