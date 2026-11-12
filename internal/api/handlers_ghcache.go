package api

// GitHub Actions cache-service v1 compatibility. actions/setup-node,
// actions/setup-go and @actions/cache all speak the same REST protocol:
//
//	GET  {ACTIONS_CACHE_URL}_apis/artifactcache/cache?keys=a,b&version=v
//	POST {ACTIONS_CACHE_URL}_apis/artifactcache/caches        {key,version,cacheSize}
//	PATCH {ACTIONS_CACHE_URL}_apis/artifactcache/caches/{id}  Content-Range chunks
//	POST {ACTIONS_CACHE_URL}_apis/artifactcache/caches/{id}   {size} commit
//
// The runner points ACTIONS_CACHE_URL at a job-scoped path under the
// existing runner-auth routes, so every call is already authenticated and
// leased-job checked by runnerJob. Downloads cannot carry that bearer token
// (the toolkit fetches archiveLocation with a bare client), so lookups hand
// back a short-lived HMAC-signed URL on the public /cache-dl route instead.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

// ghUpload is one in-flight reserve→upload→commit cycle. Chunks land via
// WriteAt so parallel PATCHes need no ordering; the temp file becomes a
// blob only at commit.
type ghUpload struct {
	id      int64
	jobID   uuid.UUID
	key     string
	version string
	scope   string
	path    string
	file    *os.File
}

// ghCacheState tracks pending uploads. Single-process is enough: the API
// leader serves runner traffic and a restart only orphans temp files,
// which the startup sweep reclaims.
type ghCacheState struct {
	mu      sync.Mutex
	seq     atomic.Int64
	uploads map[int64]*ghUpload
}

func newGHCacheState() *ghCacheState {
	return &ghCacheState{uploads: map[int64]*ghUpload{}}
}

// cacheJobToken mints the job-scoped credential handed to steps as
// ACTIONS_RUNTIME_TOKEN: an HS256 JWT bound to the job. It must be a JWT —
// not an opaque string — because buildx's type=gha cache backend
// (tonistiigi/go-actions-cache) parses the claims unverified and refuses a
// token without `ac`/`exp`/`nbf` before it ever calls ACTIONS_CACHE_URL
// (#321). The `ac` claim carries the GitHub-scopes shape it looks for;
// `kern_job` binds the token to the job. Cache routes accept it instead of
// the runner's broad token, so untrusted job code cannot escalate to
// runner-wide API access.
func (s *Server) cacheJobToken(jobID uuid.UUID) string {
	now := time.Now()
	ac, _ := json.Marshal([]map[string]any{{"Scope": "*", "Permission": 3}})
	claims, _ := json.Marshal(map[string]any{
		"iss":      "kern",
		"kern_job": jobID.String(),
		"ac":       string(ac),
		"iat":      now.Unix(),
		"nbf":      now.Add(-time.Minute).Unix(),
		"exp":      now.Add(24 * time.Hour).Unix(),
	})
	msg := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) +
		"." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte("ghcache-jwt|" + msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// parseCacheJobToken validates a job token and returns its job ID. The
// current format is the HS256 JWT; legacy "kc1.<jobID>.<exp>.<sig>" tokens
// minted before the JWT switch still parse so running jobs keep their
// cache access until they finish.
func (s *Server) parseCacheJobToken(token string) (uuid.UUID, bool) {
	parts := strings.Split(token, ".")
	if len(parts) == 4 && parts[0] == "kc1" {
		return s.parseKc1Token(parts)
	}
	if len(parts) != 3 {
		return uuid.Nil, false
	}
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte("ghcache-jwt|" + parts[0] + "." + parts[1]))
	want, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(mac.Sum(nil), want) {
		return uuid.Nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, false
	}
	var claims struct {
		Job string  `json:"kern_job"`
		Exp float64 `json:"exp"`
		Nbf float64 `json:"nbf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return uuid.Nil, false
	}
	now := float64(time.Now().Unix())
	if claims.Exp <= now || now < claims.Nbf {
		return uuid.Nil, false
	}
	jobID, err := uuid.Parse(claims.Job)
	if err != nil {
		return uuid.Nil, false
	}
	return jobID, true
}

// parseKc1Token validates the pre-JWT token shape.
func (s *Server) parseKc1Token(parts []string) (uuid.UUID, bool) {
	jobID, err := uuid.Parse(parts[1])
	if err != nil {
		return uuid.Nil, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return uuid.Nil, false
	}
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte(fmt.Sprintf("ghcache|%s|%d", jobID, exp)))
	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !hmac.Equal(mac.Sum(nil), want) {
		return uuid.Nil, false
	}
	return jobID, true
}

// requestBaseURL rebuilds the origin the runner used to reach us — the
// archiveLocation must be fetchable from inside the job container, which
// may sit on a LAN address PublicBaseURL does not cover.
func requestBaseURL(c *gin.Context, fallback string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.Request.Host
	if host == "" {
		return fallback
	}
	return scheme + "://" + host
}

// signCacheRef mints the download token: "<exp>.<ref64>.<sig64>" where sig is
// HMAC-SHA256(secret, "v1|exp|ref"). 15 minutes covers lookup→download lag.
func (s *Server) signCacheRef(blobRef string) string {
	exp := time.Now().Add(15 * time.Minute).Unix()
	payload := fmt.Sprintf("v1|%d|%s", exp, blobRef)
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	ref := base64.RawURLEncoding.EncodeToString([]byte(blobRef))
	return fmt.Sprintf("%d.%s.%s", exp, ref, sig)
}

func (s *Server) verifyCacheRef(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	ref, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte(fmt.Sprintf("v1|%s|%s", parts[0], string(ref))))
	want, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(mac.Sum(nil), want) {
		return "", false
	}
	return string(ref), true
}

// ghCacheContext resolves the job and its run for the cache-service routes.
// Runner-token callers go through runnerJob (lease-checked); kc1 job-token
// callers are bound to the jobID their token was minted for.
func (s *Server) ghCacheContext(c *gin.Context) (store.Job, store.Run, bool) {
	var job store.Job
	var err error
	if _, isRunner := currentRunner(c); isRunner {
		var ok bool
		job, _, ok = s.runnerJob(c)
		if !ok {
			return store.Job{}, store.Run{}, false
		}
	} else {
		jobID, ok := c.Get(ctxCacheJob)
		if !ok {
			fail(c, http.StatusUnauthorized, "unauthenticated", "runner or job token required")
			return store.Job{}, store.Run{}, false
		}
		id := jobID.(uuid.UUID)
		if p := c.Param("jobID"); p != id.String() {
			fail(c, http.StatusForbidden, "forbidden", "token is not valid for this job")
			return store.Job{}, store.Run{}, false
		}
		job, err = s.Store.Job(c.Request.Context(), id)
		if err != nil {
			s.internalError(c, err)
			return store.Job{}, store.Run{}, false
		}
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return store.Job{}, store.Run{}, false
	}
	return job, run, true
}

// ghCacheLookup answers GET .../cache?keys=k1,k2,...&version=v. keys[0] is the
// primary (exact) key; the rest are restore prefixes, matching GitHub.
func (s *Server) ghCacheLookup(c *gin.Context) {
	_, run, ok := s.ghCacheContext(c)
	if !ok {
		return
	}
	keys := strings.Split(c.Query("keys"), ",")
	if len(keys) == 0 || strings.TrimSpace(keys[0]) == "" {
		fail(c, http.StatusBadRequest, "invalid_key", "keys parameter is required")
		return
	}
	entry, err := s.Store.CacheLookup(c.Request.Context(), run.ProjectID,
		keys[0], keys[1:], cacheReadScopes(run.TrustLevel))
	if errors.Is(err, store.ErrNotFound) {
		if s.metrics != nil {
			s.metrics.observeCache("miss")
		}
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	if s.metrics != nil {
		s.metrics.observeCache("exact")
	}
	s.Store.TouchCache(c.Request.Context(), entry.ID)
	base := requestBaseURL(c, s.Cfg.PublicBaseURL)
	c.JSON(http.StatusOK, gin.H{
		"cacheKey":        entry.Key,
		"scope":           entry.Scope,
		"cacheVersion":    entry.Version,
		"creationTime":    entry.CreatedAt.UTC().Format(time.RFC3339),
		"archiveLocation": base + "/api/v1/cache-dl/" + s.signCacheRef(entry.BlobRef),
	})
}

// ghCacheList backs the client's debug diagnostics (GET .../caches?key=…).
// Exact-key only: prefix diagnostics are not worth a second query shape.
func (s *Server) ghCacheList(c *gin.Context) {
	_, run, ok := s.ghCacheContext(c)
	if !ok {
		return
	}
	key := c.Query("key")
	caches := []gin.H{}
	if key != "" {
		if entry, err := s.Store.CacheLookup(c.Request.Context(), run.ProjectID,
			key, nil, cacheReadScopes(run.TrustLevel)); err == nil {
			caches = append(caches, gin.H{
				"cacheKey":     entry.Key,
				"cacheVersion": entry.Version,
				"scope":        entry.Scope,
				"creationTime": entry.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"totalCount": len(caches), "artifactCaches": caches})
}

// ghCacheReserve handles POST .../caches {key,version,cacheSize}: rejects
// over-cap and already-existing keys, otherwise opens a temp file and hands
// back a numeric cacheId.
func (s *Server) ghCacheReserve(c *gin.Context) {
	job, run, ok := s.ghCacheContext(c)
	if !ok {
		return
	}
	var req struct {
		Key       string `json:"key"`
		Version   string `json:"version"`
		CacheSize int64  `json:"cacheSize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !cacheKeyRe.MatchString(req.Key) {
		fail(c, http.StatusBadRequest, "invalid_key", "cache key must be 1-512 chars and contain no commas")
		return
	}
	max := s.Cfg.CacheMaxBytes
	if max > 0 && req.CacheSize > max {
		fail(c, http.StatusBadRequest, "cache_too_large",
			fmt.Sprintf("Cache size of ~%d MB (%d B) is over the data cap limit, not saving cache.",
				req.CacheSize/(1024*1024), req.CacheSize))
		return
	}
	scope := cacheScope(run.TrustLevel)
	// Caches are immutable per scope: an existing exact key means another job
	// already saved it — 409 maps to the client's ReserveCacheError warning.
	if _, err := s.Store.CacheLookup(c.Request.Context(), run.ProjectID, req.Key,
		nil, []string{scope}); err == nil {
		fail(c, http.StatusConflict, "cache_exists", "a cache with this key already exists")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internalError(c, err)
		return
	}
	dir, err := os.MkdirTemp("", "kern-ghcache-*")
	if err != nil {
		s.internalError(c, err)
		return
	}
	path := filepath.Join(dir, "archive.tar.gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		s.internalError(c, err)
		return
	}
	id := s.ghCache.seq.Add(1)
	s.ghCache.mu.Lock()
	s.ghCache.uploads[id] = &ghUpload{
		id: id, jobID: job.ID, key: req.Key,
		version: req.Version, scope: scope, path: path, file: f,
	}
	s.ghCache.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"cacheId": id})
}

// ghCacheUpload appends one Content-Range chunk at its declared offset.
func (s *Server) ghCacheUpload(c *gin.Context) {
	job, _, ok := s.ghCacheContext(c)
	if !ok {
		return
	}
	up := s.ghUploadFor(c, job.ID)
	if up == nil {
		return
	}
	start, end, okRange := parseContentRange(c.GetHeader("Content-Range"))
	if !okRange {
		fail(c, http.StatusBadRequest, "invalid_range", "Content-Range must be 'bytes start-end/*'")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 256<<20))
	if err != nil {
		s.internalError(c, err)
		return
	}
	if int64(len(body)) != end-start+1 {
		fail(c, http.StatusBadRequest, "invalid_range", "chunk size does not match Content-Range")
		return
	}
	if _, err := up.file.WriteAt(body, start); err != nil {
		s.internalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// ghCacheCommit finalizes the upload: verifies the object, moves it into blob
// storage and records the cache_entries row. An existing key at commit time
// still answers 200 — the wanted state (key present) already holds.
func (s *Server) ghCacheCommit(c *gin.Context) {
	job, run, ok := s.ghCacheContext(c)
	if !ok {
		return
	}
	up := s.ghUploadFor(c, job.ID)
	if up == nil {
		return
	}
	var req struct {
		Size int64 `json:"size"`
	}
	_ = c.ShouldBindJSON(&req)
	_ = up.file.Sync()
	fi, err := up.file.Stat()
	if err != nil {
		s.failUpload(up)
		s.internalError(c, err)
		return
	}
	size := fi.Size()
	max := s.Cfg.CacheMaxBytes
	if max > 0 && size > max {
		s.failUpload(up)
		fail(c, http.StatusRequestEntityTooLarge, "cache_too_large",
			"cache archive exceeds KERN_CACHE_MAX_BYTES")
		return
	}
	if _, err := up.file.Seek(0, io.SeekStart); err != nil {
		s.failUpload(up)
		s.internalError(c, err)
		return
	}
	// Same blobRef layout as cacheSave so listCache/deleteCache/stale sweeps
	// treat protocol and native entries identically.
	blobRef := "cache/" + run.ProjectID.String() + "/" + url.PathEscape(up.key) + ".tar.gz"
	written, _, err := s.Blobs.Put(blobRef, up.file)
	s.failUpload(up)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if _, err := s.Store.SaveCacheEntry(c.Request.Context(), run.ProjectID,
		up.key, up.version, up.scope, blobRef, written); err != nil {
		if !errors.Is(err, store.ErrExists) {
			s.internalError(c, err)
			return
		}
		// A sibling job won the key race; our blob is redundant.
		if derr := s.Blobs.Delete(blobRef); derr != nil {
			s.Log.Error("delete duplicate cache blob", zap.String("ref", blobRef), zap.Error(derr))
		}
	}
	if s.metrics != nil {
		s.metrics.observeCache("save")
	}
	c.Status(http.StatusOK)
}

// ghUploadFor resolves the pending upload and binds it to the calling job so
// one runner cannot commit another job's reservation.
func (s *Server) ghUploadFor(c *gin.Context, jobID uuid.UUID) *ghUpload {
	id, err := strconv.ParseInt(c.Param("cacheID"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "cache id must be numeric")
		return nil
	}
	s.ghCache.mu.Lock()
	up := s.ghCache.uploads[id]
	s.ghCache.mu.Unlock()
	if up == nil || up.jobID != jobID {
		fail(c, http.StatusNotFound, "not_found", "cache reservation not found")
		return nil
	}
	return up
}

// failUpload discards a pending upload: temp dir, map entry, open handle.
func (s *Server) failUpload(up *ghUpload) {
	s.ghCache.mu.Lock()
	delete(s.ghCache.uploads, up.id)
	s.ghCache.mu.Unlock()
	_ = up.file.Close()
	_ = os.RemoveAll(filepath.Dir(up.path))
}

// parseContentRange reads "bytes start-end/*" (a concrete total is accepted
// too — only the offsets matter).
func parseContentRange(h string) (int64, int64, bool) {
	var start, end int64
	if n, _ := fmt.Sscanf(h, "bytes %d-%d/", &start, &end); n != 2 || start < 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// ghCacheDownload serves archiveLocation fetches. The HMAC token is the only
// credential — it is unguessable, scoped to one blob, and expires in 15m.
func (s *Server) ghCacheDownload(c *gin.Context) {
	ref, ok := s.verifyCacheRef(c.Param("token"))
	if !ok {
		fail(c, http.StatusNotFound, "not_found", "cache content missing")
		return
	}
	rc, size, err := s.Blobs.Open(ref)
	if err != nil || rc == nil {
		fail(c, http.StatusNotFound, "not_found", "cache content missing")
		return
	}
	defer rc.Close()
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
}

// sweepGHCacheUploads removes temp dirs orphaned by a crash or abandoned
// reservation. Called once at startup; ages out anything older than an hour.
func (s *Server) sweepGHCacheUploads() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "kern-ghcache-*"))
	for _, dir := range matches {
		if fi, err := os.Stat(dir); err == nil && time.Since(fi.ModTime()) > time.Hour {
			_ = os.RemoveAll(dir)
		}
	}
}
