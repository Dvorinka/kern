package api

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dvorinka/kern/internal/github"
)

// reusableResolver resolves jobs.<id>.uses references during workflow compile.
// Local "./" refs read from localPath when set, otherwise via the project's
// GitHub client at the run's sha. Remote owner/repo/...@ref refs get their own
// installation client; resolution failures surface as compile diagnostics.
func (s *Server) reusableResolver(ctx context.Context, localPath, fullName, sha string, gh *github.Client) func(string) (string, error) {
	return func(uses string) (string, error) {
		if strings.HasPrefix(uses, "./") {
			rel, err := cleanLocalUses(strings.TrimPrefix(uses, "./"))
			if err != nil {
				return "", err
			}
			if localPath != "" {
				b, err := os.ReadFile(localReusablePath(localPath, rel))
				if err != nil {
					return "", fmt.Errorf("reusable workflow %q: %w", uses, err)
				}
				return string(b), nil
			}
			if gh == nil {
				return "", fmt.Errorf("reusable workflow %q: no GitHub client available", uses)
			}
			return gh.FileContent(ctx, fullName, rel, sha)
		}
		repo, path, ref, err := splitRemoteReusable(uses)
		if err != nil {
			return "", err
		}
		rg, err := s.githubClient(ctx, repo)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", uses, err)
		}
		return rg.FileContent(ctx, repo, path, ref)
	}
}

// cleanLocalUses normalizes a "./" reusable-workflow path and rejects any
// form that could escape the repository root — ".." segments, absolute
// paths, and dot-only results (#368).
func cleanLocalUses(rel string) (string, error) {
	clean := path.Clean("/" + strings.TrimPrefix(rel, "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("reusable workflow path %q escapes the repository root", rel)
	}
	return clean, nil
}

// localReusablePath joins a cleaned repo-relative path under localPath and
// verifies — when both sides resolve — that symlinks cannot lead outside the
// workspace (#368).
func localReusablePath(localPath, rel string) string {
	target := filepath.Join(localPath, filepath.FromSlash(rel))
	root, rerr := filepath.EvalSymlinks(localPath)
	real, terr := filepath.EvalSymlinks(target)
	if rerr == nil && terr == nil {
		if rel2, err := filepath.Rel(root, real); err == nil &&
			rel2 != ".." && !strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
			return real
		}
		// Resolved outside the root: return the unresolvable lexical path so
		// the read fails instead of serving an escaped file.
		return filepath.Join(localPath, "\x00blocked")
	}
	return target
}

// splitRemoteReusable parses owner/repo/.github/workflows/x.yml@ref.
func splitRemoteReusable(uses string) (repo, path, ref string, err error) {
	at := strings.LastIndex(uses, "@")
	if at < 0 {
		return "", "", "", fmt.Errorf("reusable workflow %q is missing a @ref", uses)
	}
	left, ref := uses[:at], uses[at+1:]
	parts := strings.SplitN(left, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("reusable workflow %q must be owner/repo/path@ref", uses)
	}
	if ref == "" {
		return "", "", "", fmt.Errorf("reusable workflow %q is missing a @ref", uses)
	}
	if strings.Contains(parts[2], "..") {
		return "", "", "", fmt.Errorf("reusable workflow %q escapes the repository root", uses)
	}
	return parts[0] + "/" + parts[1], parts[2], ref, nil
}
