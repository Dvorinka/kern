package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/update"
	"github.com/dvorinka/kern/internal/version"
)

// One upstream call per hour, cached in-process. A failed check is
// ok:false. It never becomes a banner that claims an update exists.

type updateInfo struct {
	OK            bool                     `json:"ok"`
	Current       string                   `json:"current"`
	Latest        string                   `json:"latest"`
	URL           string                   `json:"url"`
	PublishedAt   time.Time                `json:"published_at,omitempty"`
	Available     bool                     `json:"available"`
	CheckedAt     time.Time                `json:"checked_at"`
	Repo          string                   `json:"repo"`
	Err           string                   `json:"error,omitempty"`
	AutoUpdate    string                   `json:"auto_update"`
	ManifestOK    bool                     `json:"manifest_ok"`
	ManifestError string                   `json:"manifest_error,omitempty"`
	GitSHA        string                   `json:"git_sha,omitempty"`
	RunnerImage   string                   `json:"runner_image,omitempty"`
	RunnerBins    map[string]update.Binary `json:"-"`
	ApplyBlock    string                   `json:"apply_block,omitempty"`
	Updater       string                   `json:"updater"`
	UpdaterError  string                   `json:"updater_error,omitempty"`
	UpdaterAt     time.Time                `json:"updater_at,omitempty"`
}

var updateCache struct {
	mu    sync.Mutex
	info  updateInfo
	until time.Time
}

var runnerOffer update.Lease

const runnerOfferHold = 15 * time.Minute

// updaterStatus is what the host sidecar last posted. It is in-process
// and resets when the API container is replaced, which is the point of
// a successful apply.
var updaterStatus struct {
	mu      sync.Mutex
	state   string
	err     string
	version string
	at      time.Time
}

func (s *Server) updateCheck(c *gin.Context) {
	user, _ := currentUser(c)
	info := s.cachedRelease(c.Request.Context(), s.releaseRepo(), c.Query("refresh") == "1")
	s.decorateUpdate(c.Request.Context(), user.OrgID, &info)
	c.JSON(http.StatusOK, info)
}

// updatePlan is what the host updater polls. The join token is the
// credential it already holds. Apply stays false unless auto_update is
// on and the manifest verified against a comparable version.
//
// ponytail: FirstOrg drives the host plan, one control plane has one
// checkout. A second org would need its own updater.
func (s *Server) updatePlan(c *gin.Context) {
	if !s.joinTokenValid(bearer(c)) {
		fail(c, http.StatusUnauthorized, "unauthorized", "invalid join token")
		return
	}
	org, err := s.Store.FirstOrg(c.Request.Context())
	if err != nil {
		s.internalError(c, err)
		return
	}
	info := s.cachedRelease(c.Request.Context(), s.releaseRepo(), false)
	s.decorateUpdate(c.Request.Context(), org.ID, &info)
	c.JSON(http.StatusOK, gin.H{
		"apply":          info.AutoUpdate == "on" && info.ApplyBlock == "" && info.Available && info.ManifestOK,
		"version":        info.Latest,
		"git_sha":        info.GitSHA,
		"current":        info.Current,
		"manifest_ok":    info.ManifestOK,
		"manifest_error": info.ManifestError,
		"apply_block":    info.ApplyBlock,
	})
}

func (s *Server) updateStatusReport(c *gin.Context) {
	if !s.joinTokenValid(bearer(c)) {
		fail(c, http.StatusUnauthorized, "unauthorized", "invalid join token")
		return
	}
	var body struct {
		State   string `json:"state"`
		Error   string `json:"error"`
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid status body")
		return
	}
	switch body.State {
	case "idle", "applying", "error":
	default:
		fail(c, http.StatusBadRequest, "invalid_body", "state must be idle, applying or error")
		return
	}
	if len(body.Error) > 300 {
		body.Error = body.Error[:300]
	}
	updaterStatus.mu.Lock()
	updaterStatus.state = body.State
	updaterStatus.err = body.Error
	updaterStatus.version = body.Version
	updaterStatus.at = time.Now()
	updaterStatus.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) releaseRepo() string {
	if s.Cfg != nil && s.Cfg.ReleaseRepo != "" {
		return s.Cfg.ReleaseRepo
	}
	return "Dvorinka/kern"
}

func (s *Server) decorateUpdate(ctx context.Context, orgID uuid.UUID, info *updateInfo) {
	info.AutoUpdate = "off"
	if orgID != uuid.Nil {
		if raw, err := s.Store.OrgSettings(ctx, orgID); err == nil {
			info.AutoUpdate = settingsValue(raw, "auto_update", "off")
		}
	}
	updaterStatus.mu.Lock()
	info.Updater = updaterStatus.state
	info.UpdaterError = updaterStatus.err
	info.UpdaterAt = updaterStatus.at
	updaterStatus.mu.Unlock()
	if info.Updater == "" {
		info.Updater = "not_seen"
	}
	info.ApplyBlock = applyBlock(*info)
}

func applyBlock(info updateInfo) string {
	if info.AutoUpdate != "on" {
		return "auto_update is off"
	}
	if !info.OK {
		if info.Err != "" {
			return "release check failed: " + info.Err
		}
		return "release check failed"
	}
	if !info.ManifestOK {
		if info.ManifestError != "" {
			return info.ManifestError
		}
		return "release has no signed manifest"
	}
	if !update.Comparable(info.Current) {
		return "running version " + info.Current + " is not a dotted triple, so nothing is applied"
	}
	if !info.Available {
		return ""
	}
	return ""
}

func (s *Server) cachedRelease(ctx context.Context, repo string, force bool) updateInfo {
	updateCache.mu.Lock()
	if !force && time.Now().Before(updateCache.until) && updateCache.info.Repo == repo && !updateCache.info.CheckedAt.IsZero() {
		info := updateCache.info
		updateCache.mu.Unlock()
		return info
	}
	updateCache.mu.Unlock()

	// The fetch must outlive the caller's request: a runner heartbeat has a
	// ~5s client budget, and its cancellation used to poison the cache with
	// "context canceled" for a full hour.
	info := s.fetchRelease(context.WithoutCancel(ctx), repo)
	updateCache.mu.Lock()
	updateCache.info = info
	ttl := time.Hour
	if !info.OK || !info.ManifestOK {
		ttl = 5 * time.Minute
	}
	updateCache.until = time.Now().Add(ttl)
	updateCache.mu.Unlock()
	return info
}

func (s *Server) fetchRelease(ctx context.Context, repo string) updateInfo {
	info := updateInfo{Current: version.Version, Repo: repo, CheckedAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	rel, err := s.latestRelease(ctx, repo)
	if err != nil {
		info.Err = err.Error()
		return info
	}
	info.OK = true
	info.Latest = rel.TagName
	info.URL = rel.HTMLURL
	info.PublishedAt = rel.PublishedAt
	info.Available = update.Newer(info.Current, rel.TagName)
	manifest, sig, merr := releaseAssets(ctx, rel, s.githubToken())
	if merr != nil {
		info.ManifestError = merr.Error()
		return info
	}
	parsed, err := update.Verify(manifest, sig)
	if err != nil {
		info.ManifestError = err.Error()
		return info
	}
	if parsed.Version != strings.TrimSpace(rel.TagName) && parsed.Version != strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v") &&
		"v"+parsed.Version != strings.TrimSpace(rel.TagName) {
		info.ManifestError = "manifest version does not match the release tag"
		return info
	}
	ref, err := update.PullRef(parsed.RunnerImage, parsed.RunnerDigest)
	if err != nil {
		info.ManifestError = err.Error()
		return info
	}
	info.ManifestOK = true
	info.GitSHA = parsed.GitSHA
	info.RunnerImage = ref
	info.RunnerBins = parsed.Binaries
	return info
}

func (s *Server) githubToken() string {
	if s.Cfg == nil {
		return ""
	}
	return s.Cfg.GitHub.StatusToken
}

type ghAsset struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

func (s *Server) latestRelease(ctx context.Context, repo string) (ghRelease, error) {
	var rel ghRelease
	base := "https://api.github.com"
	if s.Cfg != nil && s.Cfg.GitHub.APIBaseURL != "" {
		base = strings.TrimRight(s.Cfg.GitHub.APIBaseURL, "/")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", base, repo)
	body, err := githubGet(ctx, endpoint, s.githubToken(), 2<<20)
	if err != nil {
		return rel, err
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return rel, err
	}
	if rel.TagName == "" {
		return rel, fmt.Errorf("release has no tag")
	}
	return rel, nil
}

func releaseAssets(ctx context.Context, rel ghRelease, token string) (manifest, sig []byte, err error) {
	var manifestAsset, sigAsset *ghAsset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case "kern-update.json":
			manifestAsset = &rel.Assets[i]
		case "kern-update.json.sig":
			sigAsset = &rel.Assets[i]
		}
	}
	if manifestAsset == nil || sigAsset == nil {
		return nil, nil, fmt.Errorf("release has no signed manifest")
	}
	manifest, err = assetBytes(ctx, *manifestAsset, token)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest download: %w", err)
	}
	sig, err = assetBytes(ctx, *sigAsset, token)
	if err != nil {
		return nil, nil, fmt.Errorf("signature download: %w", err)
	}
	return manifest, sig, nil
}

// assetBytes prefers the browser download URL and falls back to the
// asset API endpoint: the release CDN 504s in bursts while api.github.com
// keeps serving. Same fallback as deployments/runner.Dockerfile.
func assetBytes(ctx context.Context, a ghAsset, token string) ([]byte, error) {
	body, err := githubGet(ctx, a.BrowserDownloadURL, token, 64<<10)
	if err == nil {
		return body, nil
	}
	if a.URL == "" {
		return nil, err
	}
	body, ferr := githubGetAccept(ctx, a.URL, "application/octet-stream", token, 64<<10)
	if ferr != nil {
		return nil, fmt.Errorf("%w (asset api fallback: %v)", err, ferr)
	}
	return body, nil
}

func githubGet(ctx context.Context, endpoint, token string, limit int64) ([]byte, error) {
	return githubGetAccept(ctx, endpoint, "", token, limit)
}

func githubGetAccept(ctx context.Context, endpoint, accept, token string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else if strings.Contains(endpoint, "://api.github.com/") || strings.Contains(endpoint, "/api/v3/") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	req.Header.Set("User-Agent", "kern/"+version.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %d", resp.StatusCode)
	}
	return body, nil
}

// autoUpdateEnabled resolves the org auto_update setting, with KERN_AUTO_UPDATE
// env as an operator override ("1", "true", "on") for installs that manage the
// control plane declaratively.
func autoUpdateEnabled(raw json.RawMessage) bool {
	switch os.Getenv("KERN_AUTO_UPDATE") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return settingsValue(raw, "auto_update", "off") == "on"
}

// offerRunnerUpdate returns the pinned image when this runner is the one
// allowed to replace itself. The offer is omitted otherwise.
func (s *Server) offerRunnerUpdate(ctx context.Context, runner store.Runner, optedIn bool, active int) (image, ver string, bins map[string]update.Binary, ok bool) {
	if runner.Draining {
		return "", "", nil, false
	}
	raw, err := s.Store.OrgSettings(ctx, runner.OrgID)
	if err != nil || !autoUpdateEnabled(raw) {
		return "", "", nil, false
	}
	info := s.cachedRelease(ctx, s.releaseRepo(), false)
	if !info.ManifestOK || info.RunnerImage == "" {
		return "", "", nil, false
	}
	if !update.RunnerShouldOffer(optedIn, active, runner.Draining, runner.Version, info.Latest) {
		return "", "", nil, false
	}
	if !runnerOffer.Allow(runner.ID.String(), runnerOfferHold) {
		return "", "", nil, false
	}
	return info.RunnerImage, info.Latest, info.RunnerBins, true
}
