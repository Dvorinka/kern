package api

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

// Icon auto-discovery: scan the project checkout (local_path) or git tree
// (GitHub repo) for image files that plausibly serve as the project icon,
// score them, and let sync pin the best one when the operator hasn't chosen.

var iconExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".ico": true, ".avif": true,
}

// iconSkipDirs never produce a sensible project icon and cost walk time.
var iconSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	"out": true, "target": true, "vendor": true, "coverage": true,
	".next": true, ".nuxt": true, "tmp": true, "temp": true,
	"__pycache__": true, ".venv": true, "venv": true, "bin": true,
	"obj": true, ".idea": true, ".vs": true,
}

// iconNameScore rewards conventional icon names; depth is handled separately.
func iconNameScore(base string) int {
	name := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	score := 0
	for _, kw := range []struct {
		sub string
		pts int
	}{
		{"logo", 50}, {"icon", 40}, {"favicon", 35}, {"brand", 30},
		{"avatar", 20}, {"mark", 15}, {"symbol", 10}, {"app", 8},
	} {
		if strings.Contains(name, kw.sub) {
			score += kw.pts
		}
	}
	return score
}

// iconDirScore rewards the places project art usually lives.
func iconDirScore(dir string) int {
	d := strings.ToLower(dir)
	for _, good := range []string{"/assets/", "/public/", "/static/", "/img/", "/images/", "/docs/", "/.github/", "/art/", "/media/"} {
		if strings.Contains(d, good) {
			return 10
		}
	}
	return 0
}

// iconPathScore ranks one repo-relative candidate; higher is better.
func iconPathScore(rel string) int {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 5 {
		return -1 // too deep to be the project face
	}
	for _, seg := range parts[:len(parts)-1] {
		if iconSkipDirs[seg] {
			return -1
		}
	}
	base := parts[len(parts)-1]
	if !iconExts[strings.ToLower(filepath.Ext(base))] {
		return -1
	}
	// shallower wins; root files beat nested ones
	depthPenalty := (len(parts) - 1) * 6
	dir := strings.TrimSuffix(filepath.ToSlash(rel), base)
	return iconNameScore(base) + iconDirScore(dir) - depthPenalty
}

// rankIcons scores, sorts best-first and caps the list for the picker.
func rankIcons(paths []string) []string {
	type scored struct {
		p string
		s int
	}
	var ok []scored
	for _, p := range paths {
		if s := iconPathScore(p); s >= 0 {
			ok = append(ok, scored{p, s})
		}
	}
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].s != ok[j].s {
			return ok[i].s > ok[j].s
		}
		return ok[i].p < ok[j].p
	})
	out := make([]string, 0, len(ok))
	for i, s := range ok {
		if i >= 80 {
			break
		}
		out = append(out, s.p)
	}
	return out
}

// discoverLocalIcons walks a local checkout for image candidates.
func discoverLocalIcons(root string) ([]string, error) {
	var paths []string
	walked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtrees just don't contribute
		}
		if walked++; walked > 20000 {
			return fs.SkipAll
		}
		if d.IsDir() {
			if path != root && iconSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	return paths, err
}

// discoverGitHubIcons lists image candidates from the repo's git tree.
func (s *Server) discoverGitHubIcons(ctx context.Context, repo, ref string) ([]string, error) {
	gh, err := s.githubClient(ctx, repo)
	if err != nil {
		return nil, err
	}
	entries, err := gh.TreeEntries(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type == "blob" {
			paths = append(paths, e.Path)
		}
	}
	return paths, nil
}

// iconCandidates resolves image paths for the project at the given ref
// (branch name or sha — the git trees API accepts either).
func (s *Server) iconCandidates(ctx context.Context, p store.Project, ref string) ([]string, error) {
	if p.LocalPath != "" {
		paths, err := discoverLocalIcons(p.LocalPath)
		if err != nil {
			return nil, err
		}
		return rankIcons(paths), nil
	}
	if p.RepoFullName != "" {
		if ref == "" {
			ref = firstNonEmpty(p.DefaultBranch, "main")
		}
		paths, err := s.discoverGitHubIcons(ctx, p.RepoFullName, ref)
		if err != nil {
			return nil, err
		}
		return rankIcons(paths), nil
	}
	return nil, nil
}

// autoDiscoverIcon pins icon_path on sync when the operator hasn't chosen
// one. An explicit setting — even a bad path — is never overwritten.
func (s *Server) autoDiscoverIcon(ctx context.Context, p store.Project, ref string) {
	raw, err := s.Store.ProjectSettings(ctx, p.ID)
	if err != nil {
		return
	}
	if settingsValue(raw, "icon_path", "") != "" {
		return
	}
	cands, err := s.iconCandidates(ctx, p, ref)
	if err != nil {
		s.Log.Debug("icon discovery failed", zap.String("project", p.Slug), zap.Error(err))
		return
	}
	if len(cands) == 0 {
		return
	}
	if err := s.Store.PutProjectSetting(ctx, p.ID, "icon_path", cands[0]); err != nil {
		s.Log.Warn("auto icon store", zap.String("project", p.Slug), zap.Error(err))
		return
	}
	s.Log.Info("auto-discovered project icon", zap.String("project", p.Slug), zap.String("icon", cands[0]))
}

// listProjectIcons serves the discovered candidates for the settings picker.
func (s *Server) listProjectIcons(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	cands, err := s.iconCandidates(c.Request.Context(), p, "")
	if err != nil {
		s.internalError(c, err)
		return
	}
	raw, err := s.Store.ProjectSettings(c.Request.Context(), p.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"icons":     cands,
		"icon_path": settingsValue(raw, "icon_path", ""),
		"source":    map[bool]string{true: "local", false: "github"}[p.LocalPath != ""],
	})
}
