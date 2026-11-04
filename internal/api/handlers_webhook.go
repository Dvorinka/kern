package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

// ghTrigger is one webhook delivery normalised into "start runs for these
// projects/workflows". push and pull_request share the run-start loop.
type ghTrigger struct {
	event    string
	repoName string // base repo: workflow discovery + commit statuses land here
	cloneURL string // what the runner clones — the head repo for fork PRs
	sha      string
	ref      string
	message  string
	actor    string
	trust    string
	extra    map[string]any
	matches  func(*workflows.Plan) (bool, string)
}

// processGithubDelivery acts on a persisted webhook delivery. push and
// pull_request events trigger matching workflows; other events are recorded
// and ignored.
func (s *Server) processGithubDelivery(c *gin.Context, delivery, event string, body []byte) (status, detail string) {
	var t *ghTrigger
	switch event {
	case "push":
		t, status, detail = parsePushTrigger(body)
	case "pull_request":
		t, status, detail = parsePRTrigger(body)
	default:
		return "ignored", "event type " + event + " is recorded but not acted on"
	}
	if t == nil {
		return status, detail
	}
	projects, err := s.Store.ProjectsByRepoFullName(c.Request.Context(), t.repoName)
	if err != nil {
		return "failed", err.Error()
	}
	if len(projects) == 0 {
		return "ignored", "no project bound to " + t.repoName
	}
	var started []string
	var skipped []string
	for _, proj := range projects {
		wfs, err := s.Store.Workflows(c.Request.Context(), proj.ID)
		if err != nil {
			return "failed", err.Error()
		}
		for _, wf := range wfs {
			version, plan, err := s.workflowAtSHA(c, t.repoName, proj.ID, wf, t.sha)
			if err != nil {
				skipped = append(skipped, wf.Name+" ("+err.Error()+")")
				continue
			}
			triggered, why := t.matches(plan)
			if !triggered {
				if why != "" {
					skipped = append(skipped, wf.Name+" ("+why+")")
				}
				continue
			}
			repoURL := t.cloneURL
			if proj.RepoURL != "" && t.trust != "fork" {
				repoURL = proj.RepoURL
			}
			source := map[string]any{
				"repo_url":       repoURL,
				"repo_full_name": t.repoName,
				"local_path":     proj.LocalPath,
				"default_branch": proj.DefaultBranch,
				"delivery":       delivery,
			}
			for k, v := range t.extra {
				source[k] = v
			}
			run, err := s.Orch.StartRun(c.Request.Context(), store.CreateRunParams{
				ProjectID:         proj.ID,
				WorkflowID:        wf.ID,
				WorkflowVersionID: version.ID,
				Event:             t.event,
				Ref:               t.ref,
				SHA:               t.sha,
				CommitMessage:     t.message,
				Actor:             t.actor,
				TrustLevel:        t.trust,
				Source:            source,
			})
			if err != nil {
				skipped = append(skipped, wf.Name+" (start failed: "+err.Error()+")")
				continue
			}
			started = append(started, fmt.Sprintf("%s #%d", wf.Name, run.RunNumber))
		}
	}
	if len(started) == 0 && len(skipped) == 0 {
		return "ignored", "no workflows subscribed to " + t.event
	}
	return "processed", fmt.Sprintf("started: %s; skipped: %s",
		strings.Join(started, ", "), strings.Join(skipped, ", "))
}

// parsePushTrigger normalises a push delivery. Returns (nil, status, detail)
// when nothing should run.
func parsePushTrigger(body []byte) (*ghTrigger, string, string) {
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
		return nil, "failed", "payload is not a push event: " + err.Error()
	}
	if p.After == "" || p.After == "0000000000000000000000000000000000000000" {
		return nil, "ignored", "branch deleted; nothing to run"
	}
	actor := p.Pusher.Name
	if actor == "" {
		actor = p.Sender.Login
	}
	return &ghTrigger{
		event: "push", repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
		sha: p.After, ref: p.Ref, message: p.HeadCommit.Message, actor: actor, trust: "trusted",
		matches: func(plan *workflows.Plan) (bool, string) { return pushMatchesPlan(plan, p.Ref) },
	}, "", ""
}

// parsePRTrigger normalises a pull_request delivery. Fork heads run untrusted
// (trust "fork": no secrets, own cache scope) and check out from the head repo,
// while statuses and workflow discovery stay on the base repo.
func parsePRTrigger(body []byte) (*ghTrigger, string, string) {
	var p struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Title string `json:"title"`
			Head  struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
					CloneURL string `json:"clone_url"`
					Fork     bool   `json:"fork"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		Repository struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "failed", "payload is not a pull_request event: " + err.Error()
	}
	pr := p.PullRequest
	if pr.Head.SHA == "" {
		return nil, "ignored", "pull_request payload has no head sha"
	}
	if pr.Head.Repo.FullName == "" {
		return nil, "ignored", "head repository is gone (deleted fork?); nothing to check out"
	}
	trust := "pull_request"
	if pr.Head.Repo.Fork || pr.Head.Repo.FullName != p.Repository.FullName {
		trust = "fork"
	}
	action := p.Action
	baseRef := pr.Base.Ref
	return &ghTrigger{
		event: "pull_request", repoName: p.Repository.FullName,
		cloneURL: pr.Head.Repo.CloneURL,
		sha:      pr.Head.SHA, ref: fmt.Sprintf("refs/pull/%d/head", p.Number),
		message: pr.Title, actor: p.Sender.Login, trust: trust,
		extra: map[string]any{"pr_number": p.Number},
		matches: func(plan *workflows.Plan) (bool, string) {
			return pullRequestMatchesPlan(plan, action, baseRef)
		},
	}, "", ""
}

// pullRequestMatchesPlan applies on:pull_request trigger semantics. Without a
// types filter GitHub defaults to opened/synchronize/reopened; with one, the
// delivered action must be listed. branches filters match the base branch.
func pullRequestMatchesPlan(plan *workflows.Plan, action, baseBranch string) (bool, string) {
	subscribed := false
	for _, e := range plan.Events {
		if e == "pull_request" {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers["pull_request"].(map[string]any)
	for key, v := range cfg {
		switch key {
		case "types", "branches", "branches-ignore":
		default:
			if !emptyFilter(v) {
				return false, "unsupported pull_request filter " + key
			}
		}
	}
	if types := filterList(cfg["types"]); len(types) > 0 {
		if !contains(types, action) {
			return false, "action " + action + " not in types filter"
		}
	} else {
		switch action {
		case "opened", "reopened", "synchronize":
		default:
			return false, "action " + action + " is not a default pull_request trigger"
		}
	}
	if patterns := filterList(cfg["branches"]); len(patterns) > 0 && !matchPatterns(patterns, baseBranch) {
		return false, "base branch " + baseBranch + " not in branches filter"
	}
	if patterns := filterList(cfg["branches-ignore"]); len(patterns) > 0 && matchPatterns(patterns, baseBranch) {
		return false, "base branch " + baseBranch + " is ignored"
	}
	return true, ""
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// workflowAtSHA compiles the workflow as it exists at the pushed commit and
// stores it as a new version, so the run executes what was pushed — not what
// was last synced. Falls back to the latest stored version when the repo is
// unreachable.
func (s *Server) workflowAtSHA(c *gin.Context, fullName string, projectID uuid.UUID,
	wf store.Workflow, sha string) (store.WorkflowVersion, *workflows.Plan, error) {
	ctx := c.Request.Context()
	gh, err := s.githubClient(ctx, fullName)
	var content string
	if err == nil {
		content, err = gh.FileContent(ctx, fullName, wf.Path, sha)
	}
	if err != nil {
		_, version, lerr := s.Store.LatestWorkflowVersion(ctx, projectID, wf.Path)
		if lerr != nil {
			return store.WorkflowVersion{}, nil, fmt.Errorf("no workflow version available")
		}
		var plan workflows.Plan
		_ = json.Unmarshal(version.Parsed, &plan)
		if !version.Valid {
			return store.WorkflowVersion{}, nil, fmt.Errorf("stored version invalid")
		}
		return version, &plan, nil
	}
	res, err := workflows.Compile(content, s.Orch.Compiler)
	if err != nil || !res.Valid || res.Plan == nil {
		return store.WorkflowVersion{}, nil, fmt.Errorf("workflow does not compile at %s", sha[:8])
	}
	version, err := s.Store.PutWorkflowVersion(ctx, wf.ID, sha, content, res.Plan, res.Valid, res.Diagnostics)
	if err != nil {
		return store.WorkflowVersion{}, nil, err
	}
	return version, res.Plan, nil
}

// pushMatchesPlan reports whether a compiled workflow plan subscribes to a push
// on ref. It honours branches/branches-ignore filters; any other non-empty
// filter (tags, paths, types…) is a declared gap and the workflow does not
// trigger, with the reason returned for the delivery record.
func pushMatchesPlan(plan *workflows.Plan, ref string) (bool, string) {
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

// listWebhookDeliveries feeds the deliveries viewer — metadata only, no
// payloads; the UI fetches one via getWebhookDelivery when a row expands.
func (s *Server) listWebhookDeliveries(c *gin.Context) {
	rows, err := s.Store.WebhookDeliveries(c.Request.Context(), parseInt(c.DefaultQuery("limit", "100"), 100))
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deliveries": rows})
}

func (s *Server) getWebhookDelivery(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "delivery id must be a uuid")
		return
	}
	d, err := s.Store.WebhookDelivery(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "delivery not found")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"delivery": d})
}
