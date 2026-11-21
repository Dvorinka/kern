package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cmdInit bootstraps a repo for kern (#376): detects the git remote and
// default branch, creates the project (or links an existing slug), syncs
// workflows, then reports what the workflows reference — secrets.*, vars.*,
// runs-on labels, environment names — so the operator knows what to fill in.
func (c *cli) cmdInit(ctx context.Context, args []string) error {
	opts, _ := parseFlagsEx(args, []string{"no-sync"}, "slug", "name", "branch")
	if err := c.requireAuth(); err != nil {
		return err
	}
	remote, err := gitOut("remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("not a git repo with an origin remote: %w", err)
	}
	branch := first(opts["branch"], gitOutOr("rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" {
		branch = "main"
	}
	fullName := repoFullName(remote)
	slug := opts["slug"]
	if slug == "" {
		slug = filepath.Base(strings.TrimSuffix(fullName, ".git"))
		if slug == "" || slug == "." {
			if wd, err := os.Getwd(); err == nil {
				slug = filepath.Base(wd)
			}
		}
	}
	if slug == "" {
		return fmt.Errorf("cannot derive a slug — pass --slug")
	}
	name := first(opts["name"], slug)

	var projects struct {
		Projects []project `json:"projects"`
	}
	_ = c.api().get(ctx, "/projects", nil, &projects)
	exists := false
	for _, p := range projects.Projects {
		if p.Slug == slug {
			exists = true
			break
		}
	}
	if !exists {
		var out struct {
			Project project `json:"project"`
		}
		if err := c.api().post(ctx, "/projects", map[string]any{
			"slug": slug, "name": name,
			"repo_url": remote, "repo_full_name": fullName,
			"default_branch": branch,
		}, &out); err != nil {
			return err
		}
		fmt.Printf("created project %s\n", slug)
	} else {
		fmt.Printf("linked existing project %s\n", slug)
	}
	// Persist the default project so subsequent commands need no --project.
	if c.cfg.Project == "" {
		c.cfg.Project = slug
		if err := saveConfig(c.cfg); err == nil {
			fmt.Println("saved as default project in", configPath())
		}
	}
	if opts["no-sync"] != "true" {
		if err := c.api().post(ctx, "/projects/"+slug+"/sync", map[string]any{}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "sync failed: %v\n", err)
		} else {
			fmt.Println("workflows synced")
		}
	}
	reportWorkflowRefs(slug)
	return nil
}

// reportWorkflowRefs statically scans .github/workflows for the surfaces a
// project needs configured (#376): secrets.*, vars.*, runs-on labels,
// environment names.
func reportWorkflowRefs(slug string) {
	files, _ := filepath.Glob(filepath.Join(".github", "workflows", "*.y*ml"))
	if len(files) == 0 {
		return
	}
	secrets := map[string]bool{}
	vars := map[string]bool{}
	labels := map[string]bool{}
	envs := map[string]bool{}
	secretRe := regexp.MustCompile(`secrets\.([A-Za-z_][A-Za-z0-9_]*)`)
	varRe := regexp.MustCompile(`vars\.([A-Za-z_][A-Za-z0-9_]*)`)
	runsOnRe := regexp.MustCompile(`runs-on:\s*[\[\(]?\s*([a-zA-Z0-9_-]+)`)
	envRe := regexp.MustCompile(`environment:\s*(?:name:\s*)?([a-zA-Z0-9_-]+)`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range secretRe.FindAllStringSubmatch(string(data), -1) {
			if m[1] != "GITHUB_TOKEN" {
				secrets[m[1]] = true
			}
		}
		for _, m := range varRe.FindAllStringSubmatch(string(data), -1) {
			vars[m[1]] = true
		}
		for _, m := range runsOnRe.FindAllStringSubmatch(string(data), -1) {
			if m[1] != "matrix" && !strings.HasPrefix(m[1], "{") {
				labels[m[1]] = true
			}
		}
		for _, m := range envRe.FindAllStringSubmatch(string(data), -1) {
			envs[m[1]] = true
		}
	}
	print := func(title, hint string, set map[string]bool) {
		if len(set) == 0 {
			return
		}
		items := make([]string, 0, len(set))
		for k := range set {
			items = append(items, k)
		}
		sort.Strings(items)
		fmt.Printf("%s %s\n", title, strings.Join(items, ", "))
		if hint != "" {
			fmt.Printf("         %s\n", hint)
		}
	}
	print("secrets needed:", "→ kern secrets set "+slug+" <NAME>", secrets)
	print("vars referenced:", "→ kern vars set <NAME> --project "+slug, vars)
	print("runner labels:", "→ runners carrying these labels will pick up the jobs", labels)
	print("environments:", "→ kern environments set "+slug+" <NAME>", envs)
}

func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func gitOutOr(args ...string) string {
	out, _ := gitOut(args...)
	return out
}

// repoFullName extracts owner/repo from ssh or https remote URLs.
func repoFullName(remote string) string {
	r := strings.TrimSuffix(remote, ".git")
	if i := strings.Index(r, ":"); i >= 0 && !strings.HasPrefix(r, "http") {
		return r[i+1:]
	}
	if u, err := url.Parse(r); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	return ""
}
