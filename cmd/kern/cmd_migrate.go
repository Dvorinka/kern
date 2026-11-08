package main

// kern migrate <repo> — import a GitHub repo's workflows into KERN: create
// the project (or reuse it by slug), sync workflow definitions, and report
// what compiled. Secrets cannot be imported — GitHub never returns their
// values — so the report reminds the operator to recreate them.

import (
	"context"
	"fmt"
	"strings"
)

func (c *cli) cmdMigrate(ctx context.Context, args []string) error {
	opts, pos := parseFlags(args, "branch", "name", "slug")
	if err := c.requireAuth(); err != nil {
		return err
	}
	repo, err := strarg(pos, 0, "repo (owner/name or github URL)")
	if err != nil {
		return err
	}
	owner, name, err := parseGitHubRepo(repo)
	if err != nil {
		return err
	}
	slug := first(opts["slug"], slugify(name))
	if slug == "" {
		return fmt.Errorf("cannot derive a project slug from %q — pass --slug", name)
	}
	fullName := owner + "/" + name
	repoURL := "https://github.com/" + fullName + ".git"

	var projects struct {
		Projects []project `json:"projects"`
	}
	if err := c.api().get(ctx, "/projects", nil, &projects); err != nil {
		return err
	}
	var p project
	for _, cand := range projects.Projects {
		if cand.Slug == slug {
			p = cand
			break
		}
	}
	created := false
	if p.ID == "" {
		var out struct {
			Project project `json:"project"`
		}
		dispName := first(opts["name"], name)
		if err := c.api().post(ctx, "/projects", map[string]any{
			"slug": slug, "name": dispName, "repo_url": repoURL,
			"repo_full_name": fullName, "default_branch": opts["branch"],
		}, &out); err != nil {
			return err
		}
		p = out.Project
		created = true
	}

	var syncOut struct {
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
	if err := c.api().post(ctx, "/projects/"+slug+"/sync", map[string]any{}, &syncOut); err != nil {
		return err
	}

	if c.json {
		printJSON(map[string]any{"project": p, "created": created, "sync": syncOut})
		return nil
	}
	verb := "reused"
	if created {
		verb = "created"
	}
	fmt.Printf("project %s %s (%s)\n", slug, verb, fullName)
	fmt.Printf("synced at %s — %d workflow(s)\n", short(syncOut.SHA, 12), len(syncOut.Workflows))
	rows := make([][]string, 0, len(syncOut.Workflows))
	for _, w := range syncOut.Workflows {
		status := "ok"
		if !w.Valid {
			status = "INVALID"
		}
		rows = append(rows, []string{w.Path, w.Name, status, fmt.Sprintf("%d", len(w.Diagnostics))})
		for _, d := range w.Diagnostics {
			rows = append(rows, []string{"", "", d.Level + ": " + d.Code, d.Message})
		}
	}
	table([]string{"PATH", "NAME", "STATUS", "DIAGS"}, rows)
	fmt.Println("note: GitHub secrets are write-only — recreate them with `kern secrets set " + slug + " NAME`")
	return nil
}

// parseGitHubRepo accepts owner/name, https://github.com/owner/name[.git],
// and git@github.com:owner/name.git forms.
func parseGitHubRepo(arg string) (owner, name string, err error) {
	s := strings.TrimSpace(arg)
	if s == "" {
		return "", "", fmt.Errorf("empty repo")
	}
	if !strings.Contains(s, "://") && !strings.Contains(s, "@") && strings.Count(s, "/") == 1 {
		parts := strings.SplitN(s, "/", 2)
		return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
	}
	// URL or scp-like form: locate github.com and read owner/name after it.
	t := strings.TrimSuffix(s, ".git")
	t = strings.TrimSuffix(t, "/")
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	if i := strings.LastIndex(t, "@"); i >= 0 {
		t = t[i+1:]
	}
	i := strings.Index(t, "github.com")
	if i < 0 {
		return "", "", fmt.Errorf("%q is not a github.com repo — expected owner/name or a github URL", arg)
	}
	rest := strings.TrimLeft(t[i+len("github.com"):], ":/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/name from %q", arg)
	}
	return parts[0], parts[1], nil
}

// slugify derives a project slug: lowercase alnum runs joined by single
// dashes. Mirrors the server's slug rules (no spaces or slashes).
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
