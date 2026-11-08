package workflows

// EventPayload rebuilds the GitHub webhook body that GITHUB_EVENT_PATH points
// at and that the github.event expression context mirrors. Actions like
// dorny/paths-filter and github-script read fields KERN does not model as
// first-class run columns, so the webhook extras stored in the run source
// (pr_number, before, base_ref, …) are folded back in here.
//
// Deliberately partial: repository carries the fields actions actually read
// (name, full_name, default_branch, clone/html urls, owner.login). Fields with
// no KERN source (private, installation, full PR objects) are omitted rather
// than guessed.

import "strings"

func EventPayload(event, ref, sha, actor, commitMessage, repoFullName, repoURL, defaultBranch string, source map[string]any) map[string]any {
	payload := map[string]any{
		"repository": eventRepository(repoFullName, repoURL, defaultBranch, source),
		"sender":     map[string]any{"login": actor},
	}
	switch event {
	case "push":
		payload["ref"] = ref
		payload["before"] = srcString(source, "before")
		payload["after"] = sha
		payload["deleted"] = false
		payload["head_commit"] = map[string]any{
			"id":      sha,
			"message": commitMessage,
		}
	case "workflow_run":
		payload["action"] = srcString(source, "action")
		payload["workflow_run"] = map[string]any{
			"id":          source["wr_id"],
			"name":        srcString(source, "wr_name"),
			"path":        srcString(source, "wr_path"),
			"event":       srcString(source, "wr_event"),
			"status":      srcString(source, "wr_status"),
			"conclusion":  srcString(source, "wr_conclusion"),
			"head_branch": srcString(source, "wr_head_branch"),
			"head_sha":    srcString(source, "wr_head_sha"),
			"run_number":  source["wr_number"],
			"run_attempt": source["wr_attempt"],
			"html_url":    srcString(source, "wr_url"),
		}
	case "pull_request", "pull_request_target":
		head := map[string]any{
			"ref": srcString(source, "head_ref"),
			"sha": srcString(source, "head_sha"),
		}
		if repo := srcString(source, "head_repo"); repo != "" {
			head["repo"] = map[string]any{"full_name": repo}
		}
		payload["action"] = srcString(source, "action")
		payload["number"] = source["pr_number"]
		payload["pull_request"] = map[string]any{
			"number": source["pr_number"],
			"title":  commitMessage,
			"head":   head,
			"base": map[string]any{
				"ref": srcString(source, "base_ref"),
				"sha": srcString(source, "base_sha"),
			},
		}
	case "release":
		payload["action"] = srcString(source, "action")
		rel := map[string]any{
			"tag_name":   srcString(source, "release_tag"),
			"name":       srcString(source, "release_name"),
			"body":       srcString(source, "release_body"),
			"draft":      source["release_draft"],
			"prerelease": source["release_prerelease"],
			"html_url":   srcString(source, "release_url"),
		}
		if author := srcString(source, "release_author"); author != "" {
			rel["author"] = map[string]any{"login": author}
		}
		payload["release"] = rel
	case "repository_dispatch":
		payload["action"] = "repository_dispatch"
		rd := map[string]any{"event_type": srcString(source, "rd_event_type")}
		if cp, ok := source["rd_payload"]; ok {
			rd["client_payload"] = cp
		}
		payload["repository_dispatch"] = rd
	}
	return payload
}

// eventRepository describes the base repo. clone_url prefers the webhook's
// recorded base clone URL: for fork PRs repoURL is the head repo's.
func eventRepository(repoFullName, repoURL, defaultBranch string, source map[string]any) map[string]any {
	repo := map[string]any{
		"name":           repoName(repoFullName),
		"full_name":      repoFullName,
		"default_branch": defaultBranch,
		"owner":          map[string]any{"login": repoOwner(repoFullName)},
	}
	cloneURL := repoURL
	if base := srcString(source, "base_clone_url"); base != "" {
		cloneURL = base
	}
	if cloneURL != "" {
		repo["clone_url"] = cloneURL
		repo["html_url"] = strings.TrimSuffix(cloneURL, ".git")
	}
	return repo
}

func repoOwner(full string) string {
	if i := strings.Index(full, "/"); i > 0 {
		return full[:i]
	}
	return ""
}

func repoName(full string) string {
	if i := strings.Index(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}

func srcString(source map[string]any, key string) string {
	if s, ok := source[key].(string); ok {
		return s
	}
	return ""
}
