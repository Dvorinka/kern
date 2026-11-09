package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

// ghTrigger is one webhook delivery normalised into "start runs for these
// projects/workflows". push and the pull-request family share the run-start
// loop.
type ghTrigger struct {
	event    string
	repoName string // base repo: workflow discovery + commit statuses land here
	cloneURL string // what the runner clones — the head repo for fork PRs
	sha      string
	ref      string
	message  string
	actor    string
	trust    string
	// association is the PR author's author_association (OWNER, MEMBER,
	// CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, NONE); the fork gate
	// reads it. headRef feeds github.head_ref in expressions.
	association   string
	headRef       string
	headSHA       string
	defaultBranch string
	baseSHA       string
	extra         map[string]any
	// pushBefore/pushAfter bound the push diff for paths filters; pushDiff
	// resolves the changed-file list lazily, once per delivery.
	pushBefore string
	pushAfter  string
	pushDiff   func(ctx context.Context) ([]string, error)
	// tagName pins a release trigger to the tagged commit; tagFallback is the
	// release's target_commitish, tried when the tag ref does not resolve.
	tagName     string
	tagFallback string
	matches     func(ctx context.Context, plan *workflows.Plan) (bool, string)
}

// processGithubDelivery acts on a persisted webhook delivery. push and
// pull_request events trigger matching workflows; other events are recorded
// and ignored.
//
// pull_request_target is an Actions trigger, not a GitHub App webhook.
// The app permissions form has no checkbox for it, and a subscribed app
// only receives pull_request. That delivery also starts pull_request_target
// workflows. A delivery that already names pull_request_target is not
// expanded, so it cannot run twice.
func (s *Server) processGithubDelivery(c *gin.Context, delivery, event string, body []byte) (status, detail string) {
	events := deliveryEvents(event)
	status, detail = s.actOnGithubEvent(c, delivery, events[0], body)
	if len(events) == 1 {
		return status, detail
	}
	st2, det2 := s.actOnGithubEvent(c, delivery, events[1], body)
	return mergeWebhookStatus(status, st2), detail + "; " + events[1] + ": " + det2
}

// deliveryEvents lists the workflow triggers one inbound webhook starts.
func deliveryEvents(event string) []string {
	if event == "pull_request" {
		return []string{"pull_request", "pull_request_target"}
	}
	return []string{event}
}

// mergeWebhookStatus prefers a failure, then a run that actually started.
func mergeWebhookStatus(a, b string) string {
	if a == "failed" || b == "failed" {
		return "failed"
	}
	if a == "processed" || b == "processed" {
		return "processed"
	}
	if a != "" {
		return a
	}
	return b
}

func (s *Server) actOnGithubEvent(c *gin.Context, delivery, event string, body []byte) (status, detail string) {
	var t *ghTrigger
	switch event {
	case "push":
		t, status, detail = parsePushTrigger(body)
	case "pull_request", "pull_request_target":
		t, status, detail = parsePRTrigger(body, event)
	case "workflow_run":
		t, status, detail = parseWorkflowRunTrigger(body)
	case "release":
		t, status, detail = parseReleaseTrigger(body)
	case "repository_dispatch":
		t, status, detail = parseRepoDispatchTrigger(body)
	default:
		return "ignored", "event type " + event + " is recorded but not acted on"
	}
	if t == nil {
		return status, detail
	}
	if event == "push" {
		// One compare-API fetch serves every project/workflow in the delivery.
		var once sync.Once
		var files []string
		var derr error
		t.pushDiff = func(ctx context.Context) ([]string, error) {
			once.Do(func() {
				files, derr = s.pushChangedFiles(ctx, t.repoName, t.pushBefore, t.pushAfter)
			})
			return files, derr
		}
	}
	if t.defaultBranch != "" {
		// pull_request_target runs in the default-branch context: the workflow
		// file and the checkout both come from the base repo's default head.
		if err := s.resolvePRTargetSHA(c, t); err != nil {
			return "failed", err.Error()
		}
	}
	if t.tagName != "" {
		// release runs the workflow version at the tagged commit, like a tag
		// push — the tag must resolve to a sha before anything compiles.
		if err := s.resolveReleaseSHA(c, t); err != nil {
			return "failed", err.Error()
		}
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
		// Fork gate: a project can require operator approval before untrusted
		// fork runs start. Held runs sit in awaiting_approval until released.
		hold := false
		if t.trust == "fork" {
			if raw, err := s.Store.ProjectSettings(c.Request.Context(), proj.ID); err == nil &&
				forkGateHold(settingsValue(raw, "fork_gate", "off"), t.association) {
				hold = true
			}
		}
		// pr_trust: a project can opt same-repo pull_request runs into the
		// trusted tier (privileged executor + secrets), matching GitHub's
		// semantics where same-repo PRs get full runner capabilities. Fork
		// heads are never promoted.
		trust := t.trust
		if trust == "pull_request" {
			if raw, err := s.Store.ProjectSettings(c.Request.Context(), proj.ID); err == nil &&
				prTrustPromoted(raw) {
				trust = "trusted"
			}
		}
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
			triggered, why := t.matches(c.Request.Context(), plan)
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
				TrustLevel:        trust,
				Source:            source,
				HoldForApproval:   hold,
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
		Before     string `json:"before"`
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
	tr := &ghTrigger{
		event: "push", repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
		sha: p.After, ref: p.Ref, message: p.HeadCommit.Message, actor: actor, trust: "trusted",
		pushBefore: p.Before, pushAfter: p.After,
		extra: map[string]any{"before": p.Before},
	}
	tr.matches = func(ctx context.Context, plan *workflows.Plan) (bool, string) {
		return pushMatchesPlan(ctx, plan, p.Ref, tr.pushDiff)
	}
	return tr, "", ""
}

// prPayload is the shared webhook body for pull_request and
// pull_request_target — GitHub sends the same object for both.
type prPayload struct {
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
			SHA string `json:"sha"`
		} `json:"base"`
		AuthorAssociation string `json:"author_association"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// parsePRTrigger normalises a pull_request or pull_request_target delivery.
// pull_request runs the head SHA: fork heads get trust "fork" (no secrets, own
// cache scope) and check out from the head repo, while statuses and workflow
// discovery stay on the base repo. pull_request_target runs in the base
// repo's default-branch context instead — trusted code, secrets allowed —
// with sha/ref resolved later by resolvePRTargetSHA.
func parsePRTrigger(body []byte, event string) (*ghTrigger, string, string) {
	var p prPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "failed", "payload is not a " + event + " event: " + err.Error()
	}
	pr := p.PullRequest
	if pr.Head.SHA == "" {
		return nil, "ignored", "pull request payload has no head sha"
	}
	if event == "pull_request_target" {
		if p.Repository.DefaultBranch == "" {
			return nil, "ignored", "pull_request_target payload has no default branch"
		}
		return &ghTrigger{
			event: event, repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
			sha: "", ref: "", message: pr.Title, actor: p.Sender.Login, trust: "trusted",
			association: pr.AuthorAssociation, headRef: pr.Head.Ref, headSHA: pr.Head.SHA,
			defaultBranch: p.Repository.DefaultBranch, baseSHA: pr.Base.SHA,
			extra: map[string]any{
				"pr_number": p.Number, "head_ref": pr.Head.Ref, "head_sha": pr.Head.SHA,
				"action": p.Action, "base_ref": pr.Base.Ref, "base_sha": pr.Base.SHA,
				"head_repo": pr.Head.Repo.FullName, "base_clone_url": p.Repository.CloneURL,
			},
			matches: func(_ context.Context, plan *workflows.Plan) (bool, string) {
				return pullRequestMatchesPlan(plan, event, p.Action, pr.Base.Ref)
			},
		}, "", ""
	}
	if pr.Head.Repo.FullName == "" {
		return nil, "ignored", "head repository is gone (deleted fork?); nothing to check out"
	}
	trust := "pull_request"
	// Repo identity decides fork trust, not the repo.fork flag: GitHub sets
	// fork=true on every repo in a fork network, so a same-repo PR on a
	// forked project (head.repo.full_name == repository.full_name) would be
	// misclassified and pr_trust could never promote it (#184).
	if pr.Head.Repo.FullName != p.Repository.FullName {
		trust = "fork"
	}
	return &ghTrigger{
		event: event, repoName: p.Repository.FullName,
		cloneURL: pr.Head.Repo.CloneURL,
		sha:      pr.Head.SHA, ref: fmt.Sprintf("refs/pull/%d/head", p.Number),
		message: pr.Title, actor: p.Sender.Login, trust: trust,
		association: pr.AuthorAssociation, headRef: pr.Head.Ref, headSHA: pr.Head.SHA,
		extra: map[string]any{
			"pr_number": p.Number, "head_ref": pr.Head.Ref, "head_sha": pr.Head.SHA,
			"action": p.Action, "base_ref": pr.Base.Ref, "base_sha": pr.Base.SHA,
			"head_repo": pr.Head.Repo.FullName, "base_clone_url": p.Repository.CloneURL,
		},
		matches: func(_ context.Context, plan *workflows.Plan) (bool, string) {
			return pullRequestMatchesPlan(plan, event, p.Action, pr.Base.Ref)
		},
	}, "", ""
}

// workflowRunPayload is the webhook body for workflow_run deliveries. Only
// the fields KERN acts on are modelled; the rest of the run object is rebuilt
// for github.event from the run source extras.
type workflowRunPayload struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Path       string `json:"path"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		RunNumber  int    `json:"run_number"`
		RunAttempt int    `json:"run_attempt"`
		HTMLURL    string `json:"html_url"`
	} `json:"workflow_run"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// parseWorkflowRunTrigger normalises a workflow_run delivery. GitHub runs the
// triggered workflow on the default branch — default-branch code with secrets,
// the same trust model as pull_request_target — so sha/ref resolve through
// resolvePRTargetSHA against the default head.
func parseWorkflowRunTrigger(body []byte) (*ghTrigger, string, string) {
	var p workflowRunPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "failed", "payload is not a workflow_run event: " + err.Error()
	}
	wr := p.WorkflowRun
	if wr.Name == "" && wr.Path == "" {
		return nil, "ignored", "workflow_run payload has no workflow name or path"
	}
	if p.Repository.DefaultBranch == "" {
		return nil, "ignored", "workflow_run payload has no default branch"
	}
	// head_sha is a usable default-head fallback only when the completed run
	// was itself on the default branch.
	baseSHA := ""
	if wr.HeadBranch == p.Repository.DefaultBranch {
		baseSHA = wr.HeadSHA
	}
	return &ghTrigger{
		event: "workflow_run", repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
		sha: "", ref: "", message: wr.Name, actor: p.Sender.Login, trust: "trusted",
		defaultBranch: p.Repository.DefaultBranch, baseSHA: baseSHA,
		extra: map[string]any{
			"action": p.Action,
			"wr_id":  wr.ID, "wr_name": wr.Name, "wr_path": wr.Path,
			"wr_event": wr.Event, "wr_status": wr.Status, "wr_conclusion": wr.Conclusion,
			"wr_head_branch": wr.HeadBranch, "wr_head_sha": wr.HeadSHA,
			"wr_number": wr.RunNumber, "wr_attempt": wr.RunAttempt, "wr_url": wr.HTMLURL,
		},
		matches: func(_ context.Context, plan *workflows.Plan) (bool, string) {
			return workflowRunMatchesPlan(plan, p.Action, wr.Name, wr.Path, wr.HeadBranch)
		},
	}, "", ""
}

// workflowRunMatchesPlan applies on:workflow_run semantics. workflows: lists
// workflow names or paths as glob patterns (matched against both); types:
// lists activity types — requested, in_progress, completed — with no filter
// meaning any; branches/branches-ignore match the triggering run's
// head_branch. Any other non-empty key stays a declared skip.
func workflowRunMatchesPlan(plan *workflows.Plan, action, name, path, headBranch string) (bool, string) {
	subscribed := false
	for _, e := range plan.Events {
		if e == "workflow_run" {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers["workflow_run"].(map[string]any)
	for key, v := range cfg {
		switch key {
		case "workflows", "types", "branches", "branches-ignore":
		default:
			if !emptyFilter(v) {
				return false, "unsupported workflow_run filter " + key
			}
		}
	}
	names := filterList(cfg["workflows"])
	if len(names) == 0 {
		return false, "workflow_run trigger has no workflows filter"
	}
	if !matchPatterns(names, name) && (path == "" || !matchPatterns(names, path)) {
		return false, "completed workflow " + name + " not in workflows filter"
	}
	if types := filterList(cfg["types"]); len(types) > 0 && !contains(types, action) {
		return false, "action " + action + " not in types filter"
	}
	if patterns := filterList(cfg["branches"]); len(patterns) > 0 && !matchPatterns(patterns, headBranch) {
		return false, "head branch " + headBranch + " not in branches filter"
	}
	if patterns := filterList(cfg["branches-ignore"]); len(patterns) > 0 && matchPatterns(patterns, headBranch) {
		return false, "head branch " + headBranch + " is ignored"
	}
	return true, ""
}

// releasePayload is the webhook body for release deliveries.
type releasePayload struct {
	Action  string `json:"action"`
	Release struct {
		TagName         string `json:"tag_name"`
		Name            string `json:"name"`
		Body            string `json:"body"`
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
		HTMLURL         string `json:"html_url"`
		TargetCommitish string `json:"target_commitish"`
		Author          struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"release"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// parseReleaseTrigger normalises a release delivery. GitHub runs release
// workflows at the tagged commit — ref is the tag, sha resolves later via
// resolveReleaseSHA — with secrets, the same trust model as a trusted push.
func parseReleaseTrigger(body []byte) (*ghTrigger, string, string) {
	var p releasePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "failed", "payload is not a release event: " + err.Error()
	}
	rel := p.Release
	if rel.TagName == "" {
		return nil, "ignored", "release payload has no tag_name"
	}
	actor := p.Sender.Login
	if actor == "" {
		actor = rel.Author.Login
	}
	message := rel.Name
	if message == "" {
		message = rel.TagName
	}
	return &ghTrigger{
		event: "release", repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
		sha: "", ref: "refs/tags/" + rel.TagName, message: message, actor: actor, trust: "trusted",
		tagName: rel.TagName, tagFallback: rel.TargetCommitish,
		extra: map[string]any{
			"action":      p.Action,
			"release_tag": rel.TagName, "release_name": rel.Name, "release_body": rel.Body,
			"release_draft": rel.Draft, "release_prerelease": rel.Prerelease,
			"release_url": rel.HTMLURL, "release_author": rel.Author.Login,
			"target_commitish": rel.TargetCommitish,
		},
		matches: func(_ context.Context, plan *workflows.Plan) (bool, string) {
			return releaseMatchesPlan(plan, p.Action, rel.TargetCommitish)
		},
	}, "", ""
}

// releaseMatchesPlan applies on:release semantics. types: lists release
// actions (published, prereleased, released, created, edited, deleted,
// unpublished); with no filter GitHub fires only on published. branches
// filters match the release's target_commitish — a commit-ish that does not
// name a matching branch simply does not match.
func releaseMatchesPlan(plan *workflows.Plan, action, targetCommitish string) (bool, string) {
	subscribed := false
	for _, e := range plan.Events {
		if e == "release" {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers["release"].(map[string]any)
	for key, v := range cfg {
		switch key {
		case "types", "branches", "branches-ignore":
		default:
			if !emptyFilter(v) {
				return false, "unsupported release filter " + key
			}
		}
	}
	types := filterList(cfg["types"])
	if len(types) == 0 {
		types = []string{"published"}
	}
	if !contains(types, action) {
		return false, "action " + action + " not in types filter"
	}
	if patterns := filterList(cfg["branches"]); len(patterns) > 0 && !matchPatterns(patterns, targetCommitish) {
		return false, "target " + targetCommitish + " not in branches filter"
	}
	if patterns := filterList(cfg["branches-ignore"]); len(patterns) > 0 && matchPatterns(patterns, targetCommitish) {
		return false, "target " + targetCommitish + " is ignored"
	}
	return true, ""
}

// repoDispatchPayload is the webhook body for repository_dispatch deliveries.
type repoDispatchPayload struct {
	Action             string `json:"action"`
	RepositoryDispatch struct {
		EventType     string         `json:"event_type"`
		ClientPayload map[string]any `json:"client_payload"`
	} `json:"repository_dispatch"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Repository struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// parseRepoDispatchTrigger normalises a repository_dispatch delivery. Like
// workflow_dispatch via the API, it runs on the default branch head with
// secrets — defaultBranch defers sha/ref to resolvePRTargetSHA.
func parseRepoDispatchTrigger(body []byte) (*ghTrigger, string, string) {
	var p repoDispatchPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "failed", "payload is not a repository_dispatch event: " + err.Error()
	}
	rd := p.RepositoryDispatch
	if rd.EventType == "" {
		return nil, "ignored", "repository_dispatch payload has no event_type"
	}
	if p.Repository.DefaultBranch == "" {
		return nil, "ignored", "repository_dispatch payload has no default branch"
	}
	return &ghTrigger{
		event: "repository_dispatch", repoName: p.Repository.FullName, cloneURL: p.Repository.CloneURL,
		sha: "", ref: "", message: rd.EventType, actor: p.Sender.Login, trust: "trusted",
		defaultBranch: p.Repository.DefaultBranch,
		extra: map[string]any{
			"rd_event_type": rd.EventType, "rd_payload": rd.ClientPayload,
		},
		matches: func(_ context.Context, plan *workflows.Plan) (bool, string) {
			return repoDispatchMatchesPlan(plan, rd.EventType)
		},
	}, "", ""
}

// repoDispatchMatchesPlan applies on:repository_dispatch semantics: types:
// lists the client-chosen event_type strings and is required — GitHub never
// fires a repository_dispatch workflow that does not name its types.
func repoDispatchMatchesPlan(plan *workflows.Plan, eventType string) (bool, string) {
	subscribed := false
	for _, e := range plan.Events {
		if e == "repository_dispatch" {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers["repository_dispatch"].(map[string]any)
	for key, v := range cfg {
		if key != "types" && !emptyFilter(v) {
			return false, "unsupported repository_dispatch filter " + key
		}
	}
	types := filterList(cfg["types"])
	if len(types) == 0 {
		return false, "repository_dispatch trigger has no types filter"
	}
	if !contains(types, eventType) {
		return false, "event_type " + eventType + " not in types filter"
	}
	return true, ""
}

// resolveReleaseSHA pins a release trigger to the tagged commit, falling back
// to the release's target_commitish (a branch name or sha) when the tag ref
// does not resolve.
func (s *Server) resolveReleaseSHA(c *gin.Context, t *ghTrigger) error {
	ctx := c.Request.Context()
	gh, err := s.githubClient(ctx, t.repoName)
	if err == nil {
		if sha, herr := gh.HeadSHA(ctx, t.repoName, url.PathEscape(t.tagName)); herr == nil && sha != "" {
			t.sha = sha
		} else if t.tagFallback != "" {
			if sha, ferr := gh.HeadSHA(ctx, t.repoName, url.PathEscape(t.tagFallback)); ferr == nil && sha != "" {
				t.sha = sha
			}
		}
	}
	if t.sha == "" {
		return fmt.Errorf("cannot resolve release tag %q to a commit", t.tagName)
	}
	return nil
}

// pushChangedFiles resolves the paths a push changed via the compare API. A
// push with no diff base — ref creation (before is all zeros) or an empty
// range — errors, and callers treat that as a declared skip, never a blind
// fire.
func (s *Server) pushChangedFiles(ctx context.Context, repo, before, after string) ([]string, error) {
	if before == "" || after == "" || before == after ||
		before == "0000000000000000000000000000000000000000" {
		return nil, fmt.Errorf("push has no comparable diff base")
	}
	gh, err := s.githubClient(ctx, repo)
	if err != nil {
		return nil, err
	}
	return gh.CompareFiles(ctx, repo, before, after)
}

// resolvePRTargetSHA pins a pull_request_target trigger to the base repo's
// default-branch head, falling back to the PR's recorded base sha when the
// ref cannot be resolved.
func (s *Server) resolvePRTargetSHA(c *gin.Context, t *ghTrigger) error {
	ctx := c.Request.Context()
	gh, err := s.githubClient(ctx, t.repoName)
	if err == nil {
		if sha, herr := gh.HeadSHA(ctx, t.repoName, t.defaultBranch); herr == nil && sha != "" {
			t.sha = sha
		}
	}
	if t.sha == "" {
		t.sha = t.baseSHA
	}
	if t.sha == "" {
		return fmt.Errorf("cannot resolve default branch %s head for pull_request_target", t.defaultBranch)
	}
	t.ref = "refs/heads/" + t.defaultBranch
	return nil
}

// forkGateHold reports whether a fork-trust run must wait for operator
// approval under the project's fork_gate setting: "all" holds every fork
// run; "first-time" holds authors GitHub marks as new (FIRST_TIME_CONTRIBUTOR,
// FIRST_TIMER) or unaffiliated (NONE, or a payload without the field).
func forkGateHold(gate, association string) bool {
	switch gate {
	case "all":
		return true
	case "first-time":
		switch association {
		case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "MANNEQUIN":
			return false
		}
		return true
	}
	return false
}

// prTrustPromoted reports whether a project's pr_trust setting opts
// same-repo pull_request runs into the trusted tier.
func prTrustPromoted(raw json.RawMessage) bool {
	return settingsValue(raw, "pr_trust", "off") == "trusted"
}

// pullRequestMatchesPlan applies on:pull_request / on:pull_request_target
// trigger semantics — identical for both events. Without a types filter
// GitHub defaults to opened/synchronize/reopened; with one, the delivered
// action must be listed. branches filters match the base branch.
func pullRequestMatchesPlan(plan *workflows.Plan, event, action, baseBranch string) (bool, string) {
	subscribed := false
	for _, e := range plan.Events {
		if e == event {
			subscribed = true
		}
	}
	if !subscribed {
		return false, ""
	}
	cfg, _ := plan.Triggers[event].(map[string]any)
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
	cc := s.Orch.Compiler
	cc.Resolve = s.reusableResolver(ctx, "", fullName, sha, gh)
	res, err := workflows.Compile(content, cc)
	if err != nil || !res.Valid || res.Plan == nil {
		return store.WorkflowVersion{}, nil, fmt.Errorf("workflow does not compile at %s", sha[:8])
	}
	version, err := s.Store.PutWorkflowVersion(ctx, wf.ID, sha, content, res.Plan, res.Valid, res.Diagnostics, res.Deps)
	if err != nil {
		return store.WorkflowVersion{}, nil, err
	}
	return version, res.Plan, nil
}

// pushMatchesPlan reports whether a compiled workflow plan subscribes to a push
// on ref. GitHub semantics: branches/branches-ignore apply to branch pushes
// only, tags/tags-ignore to tag pushes only — defining just one family means
// pushes to the other ref type never run; defining neither runs on both.
// paths/paths-ignore evaluate the push diff on branch pushes only — GitHub
// does not evaluate them for tag pushes. diff resolves the changed-file list;
// a nil diff or a failed lookup skips rather than firing blindly.
func pushMatchesPlan(ctx context.Context, plan *workflows.Plan, ref string,
	diff func(context.Context) ([]string, error)) (bool, string) {
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
		case "branches", "branches-ignore", "tags", "tags-ignore", "paths", "paths-ignore":
		default:
			if !emptyFilter(v) {
				return false, "unsupported push filter " + key
			}
		}
	}

	family, name := pushRefKind(ref)
	if family == "" {
		// Neither a branch nor a tag push: only an unfiltered workflow runs.
		if pushHasRefFilter(cfg) {
			return false, "push to " + ref + " is neither a branch nor a tag"
		}
	} else {
		var incKey, ignKey, otherFamily string
		if family == "tags" {
			incKey, ignKey, otherFamily = "tags", "tags-ignore", "branches"
		} else {
			incKey, ignKey, otherFamily = "branches", "branches-ignore", "tags"
		}
		own := filterList(cfg[incKey])
		ownIgnore := filterList(cfg[ignKey])
		if len(own) == 0 && len(ownIgnore) == 0 {
			// The other family's filters, when present, still gate this push out.
			if len(filterList(cfg[otherFamily]))+len(filterList(cfg[otherFamily+"-ignore"])) > 0 {
				return false, family + " push skipped: workflow defines only " + otherFamily + " filters"
			}
		} else {
			if len(own) > 0 && !matchPatterns(own, name) {
				return false, family + " " + name + " not in " + incKey + " filter"
			}
			if len(ownIgnore) > 0 && matchPatterns(ownIgnore, name) {
				return false, family + " " + name + " is ignored"
			}
		}
		if family == "tags" {
			// GitHub does not evaluate path filters for tag pushes.
			return true, ""
		}
	}

	paths := filterList(cfg["paths"])
	ignore := filterList(cfg["paths-ignore"])
	if len(paths) == 0 && len(ignore) == 0 {
		return true, ""
	}
	if diff == nil {
		return false, "paths filter present but no diff resolver"
	}
	files, err := diff(ctx)
	if err != nil {
		return false, "push diff unavailable: " + err.Error()
	}
	return pushPathsMatch(paths, ignore, files)
}

// pushPathsMatch applies paths/paths-ignore to the push diff: paths runs the
// workflow when at least one changed file is included by the ordered pattern
// list; paths-ignore skips it when any changed file is included.
func pushPathsMatch(paths, ignore, files []string) (bool, string) {
	if len(paths) > 0 {
		hit := false
		for _, f := range files {
			if pathPatternsInclude(paths, f) {
				hit = true
				break
			}
		}
		if !hit {
			return false, "no changed file matches paths filter"
		}
	}
	for _, f := range files {
		if pathPatternsInclude(ignore, f) {
			return false, "changed file " + f + " matches paths-ignore"
		}
	}
	return true, ""
}

// pushRefKind splits a git ref into the push-filter family it belongs to
// ("branches" or "tags") and its short name. Anything else returns "".
func pushRefKind(ref string) (family, name string) {
	if strings.HasPrefix(ref, "refs/tags/") {
		return "tags", strings.TrimPrefix(ref, "refs/tags/")
	}
	if strings.HasPrefix(ref, "refs/heads/") {
		return "branches", strings.TrimPrefix(ref, "refs/heads/")
	}
	return "", ref
}

// pushHasRefFilter reports whether the push trigger config declares any
// branch/tag name filter.
func pushHasRefFilter(cfg map[string]any) bool {
	for _, key := range []string{"branches", "branches-ignore", "tags", "tags-ignore"} {
		if len(filterList(cfg[key])) > 0 {
			return true
		}
	}
	return false
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
	return matchPatternsFn(patterns, name, globMatch)
}

// pathPatternsInclude is matchPatterns for path filters — same '!' ordering,
// different glob rules ('*' does not cross '/').
func pathPatternsInclude(patterns []string, name string) bool {
	return matchPatternsFn(patterns, name, pathGlobMatch)
}

func matchPatternsFn(patterns []string, name string, match func(string, string) bool) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if match(strings.TrimPrefix(p, "!"), name) {
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

// pathGlobMatch implements GitHub's path-filter glob: the whole repo-relative
// path must match, '*' and '?' never cross '/', '**' does, '**/' matches zero
// or more directories, and '?' makes the preceding character optional.
// jarvis: ceiling — no [] classes or backslash escapes; upgrade if needed.
func pathGlobMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteByte('^')
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				i++
				if i+1 < len(rs) && rs[i+1] == '/' {
					b.WriteString("(?:.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			if b.Len() > 1 {
				b.WriteByte('?')
			} else {
				b.WriteString(`\?`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		}
	}
	b.WriteByte('$')
	ok, _ := regexp.MatchString(b.String(), name)
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
