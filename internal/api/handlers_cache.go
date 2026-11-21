package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/store"
)

// GitHub cache keys: up to 512 chars, commas forbidden (they separate restore keys).
var cacheKeyRe = regexp.MustCompile(`^[^,]{1,512}$`)

// cacheScope maps run trust onto the cache namespace. Pull-request runs read
// trusted entries (base-branch caches) but save into their own scope so an
// untrusted ref cannot poison the shared cache.
func cacheScope(run store.Run) string {
	if run.TrustLevel != "pull_request" && run.TrustLevel != "fork" {
		return "trusted"
	}
	// All PRs used to share one "pull_request" scope — PR-A could poison
	// PR-B's keys. Scope writes per PR; the bare scope remains for runs
	// that carry no pr_number (#369).
	var src struct {
		PRNumber any `json:"pr_number"`
	}
	if err := json.Unmarshal(run.Source, &src); err == nil {
		if pr := fmt.Sprint(src.PRNumber); pr != "" && pr != "<nil>" {
			return "pull_request:" + pr
		}
	}
	return "pull_request"
}

func cacheReadScopes(run store.Run) []string {
	if run.TrustLevel == "pull_request" || run.TrustLevel == "fork" {
		return []string{cacheScope(run), "trusted"}
	}
	return []string{"trusted"}
}

// cacheRestore resolves key → restore-keys and streams the archive back.
// The matched key rides in X-Kern-Cache-Key so the runner can report
// cache-hit = (matched == requested), matching GitHub's semantics.
func (s *Server) cacheRestore(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	key := c.Query("key")
	if !cacheKeyRe.MatchString(key) {
		fail(c, http.StatusBadRequest, "invalid_key", "cache key must be 1-512 chars and contain no commas")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// Commas are a safe separator: they are not legal inside cache keys.
	var prefixes []string
	if rk := c.Query("restore_keys"); rk != "" {
		prefixes = strings.Split(rk, ",")
	}
	entry, err := s.Store.CacheLookup(c.Request.Context(), run.ProjectID, key, "", prefixes, cacheReadScopes(run))
	if errors.Is(err, store.ErrNotFound) {
		if s.metrics != nil {
			s.metrics.observeCache("miss")
		}
		fail(c, http.StatusNotFound, "cache_miss", "no cache entry matched")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	result := "prefix"
	if entry.Key == key {
		result = "exact"
	}
	// direct=1 hands the runner a pre-signed object URL instead of proxying
	// bytes through the API (#369). Filesystem has no Presigner — the runner
	// only asks when its prepare probe said presign is available.
	if c.Query("direct") == "1" {
		ps, okP := s.Blobs.(blobs.Presigner)
		if !okP {
			fail(c, http.StatusNotFound, "presign_unavailable", "blob backend cannot presign")
			return
		}
		u, uerr := ps.PresignGet(c.Request.Context(), entry.BlobRef, 15*time.Minute)
		if uerr != nil {
			s.internalError(c, uerr)
			return
		}
		if s.metrics != nil {
			s.metrics.observeCache(result)
		}
		s.Store.TouchCache(c.Request.Context(), entry.ID)
		c.JSON(http.StatusOK, gin.H{"url": u, "key": entry.Key})
		return
	}
	rc, size, err := s.Blobs.Open(entry.BlobRef)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		// Row exists but the archive is gone — the runner sees a miss either way.
		if s.metrics != nil {
			s.metrics.observeCache("miss")
		}
		fail(c, http.StatusNotFound, "not_found", "cache content missing")
		return
	}
	if s.metrics != nil {
		s.metrics.observeCache(result)
	}
	defer rc.Close()
	s.Store.TouchCache(c.Request.Context(), entry.ID)
	c.Header("X-Kern-Cache-Key", entry.Key)
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
}

// listCache exposes the org's cache entries to operators (CLI and web).
func (s *Server) listCache(c *gin.Context) {
	user, _ := currentUser(c)
	entries, err := s.Store.CacheEntries(c.Request.Context(), user.OrgID,
		parseInt(c.DefaultQuery("limit", "200"), 200))
	if err != nil {
		s.internalError(c, err)
		return
	}
	count, bytes, largest, projects, err := s.Store.CacheEntryStats(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"entries": entries,
		"stats":   gin.H{"count": count, "bytes": bytes, "largest": largest, "projects": projects},
	})
}

// deleteCache removes one entry and its archive. Manual eviction until
// automatic retention lands.
func (s *Server) deleteCache(c *gin.Context) {
	user, _ := currentUser(c)
	id, ok := s.cacheIDParam(c)
	if !ok {
		return
	}
	blobRef, err := s.Store.DeleteCacheEntry(c.Request.Context(), user.OrgID, id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if err := s.Blobs.Delete(blobRef); err != nil {
		s.Log.Error("delete cache blob", zap.String("ref", blobRef), zap.Error(err))
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "cache.delete", id.String())
	c.Status(http.StatusNoContent)
}

// cacheSave stores an archive under (project, key, version, scope). Caches are
// immutable: an existing key answers 409, which the runner reports as a warning,
// the same way GitHub treats a concurrent save.
func (s *Server) cacheSave(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	// gin already decodes path params; no second unescape here.
	key := c.Param("key")
	if !cacheKeyRe.MatchString(key) {
		fail(c, http.StatusBadRequest, "invalid_key", "cache key must be 1-512 chars and contain no commas")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	max := s.Cfg.CacheMaxBytes
	// Content-Length short-circuits obviously oversized uploads; the size
	// check after Put covers chunked bodies that declare no length.
	if max > 0 && c.Request.ContentLength > max {
		fail(c, http.StatusRequestEntityTooLarge, "cache_too_large",
			"cache archive exceeds KERN_CACHE_MAX_BYTES")
		return
	}
	// Unique ref per save attempt: a deterministic key-derived path let a fork
	// save overwrite a trusted branch's live blob in place, and a losing
	// concurrent save then deleted the winner's blob on ErrExists (#369).
	blobRef := fmt.Sprintf("cache/%s/%s/%s-%s.tar.gz", run.ProjectID,
		cacheScope(run), url.PathEscape(key), uuid.NewString())
	size, _, err := s.Blobs.Put(blobRef, io.LimitReader(c.Request.Body, 4<<30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	dropBlob := func() {
		if derr := s.Blobs.Delete(blobRef); derr != nil {
			s.Log.Error("delete cache blob", zap.String("ref", blobRef), zap.Error(derr))
		}
	}
	if max > 0 && size > max {
		dropBlob()
		fail(c, http.StatusRequestEntityTooLarge, "cache_too_large",
			"cache archive exceeds KERN_CACHE_MAX_BYTES")
		return
	}
	_, err = s.Store.SaveCacheEntry(c.Request.Context(), run.ProjectID, key, "", cacheScope(run), blobRef, size)
	if errors.Is(err, store.ErrExists) {
		dropBlob()
		fail(c, http.StatusConflict, "cache_exists", "a cache with this key already exists")
		return
	}
	if err != nil {
		dropBlob()
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": key, "size": size})
}

// cachePrepareUpload hands the runner a pre-signed PUT URL for its archive
// plus the blob_ref it must echo at commit. 404 presign_unavailable tells
// older/again runners to use the plain PUT path instead (#369).
func (s *Server) cachePrepareUpload(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	key := c.Param("key")
	if !cacheKeyRe.MatchString(key) {
		fail(c, http.StatusBadRequest, "invalid_key", "cache key must be 1-512 chars and contain no commas")
		return
	}
	ps, okP := s.Blobs.(blobs.Presigner)
	if !okP {
		fail(c, http.StatusNotFound, "presign_unavailable", "blob backend cannot presign")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// Same unique-ref scheme as cacheSave — fork saves can never overwrite a
	// trusted blob in place (#369).
	blobRef := fmt.Sprintf("cache/%s/%s/%s-%s.tar.gz", run.ProjectID,
		cacheScope(run), url.PathEscape(key), uuid.NewString())
	u, err := ps.PresignPut(c.Request.Context(), blobRef, 15*time.Minute)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"upload_url": u, "blob_ref": blobRef})
}

// cacheCommitUpload finalizes a direct-S3 save: the blob must exist at the
// prepared ref with a sane size before the cache_entries row is written.
func (s *Server) cacheCommitUpload(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	key := c.Param("key")
	if !cacheKeyRe.MatchString(key) {
		fail(c, http.StatusBadRequest, "invalid_key", "cache key must be 1-512 chars and contain no commas")
		return
	}
	var req struct {
		BlobRef string `json:"blob_ref"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.BlobRef == "" {
		fail(c, http.StatusBadRequest, "invalid_request", "blob_ref required")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// The ref must be one WE issued for this project+scope — a runner could
	// otherwise point commit at another tenant's object (#368).
	prefix := fmt.Sprintf("cache/%s/%s/", run.ProjectID, cacheScope(run))
	if !strings.HasPrefix(req.BlobRef, prefix) {
		fail(c, http.StatusBadRequest, "invalid_ref", "blob_ref is not a cache upload for this run")
		return
	}
	size, err := s.Blobs.Size(req.BlobRef)
	if err != nil || size <= 0 {
		fail(c, http.StatusBadRequest, "missing_blob", "upload not found — PUT to the signed URL first")
		return
	}
	if max := s.Cfg.CacheMaxBytes; max > 0 && size > max {
		_ = s.Blobs.Delete(req.BlobRef)
		fail(c, http.StatusRequestEntityTooLarge, "cache_too_large",
			"cache archive exceeds KERN_CACHE_MAX_BYTES")
		return
	}
	_, err = s.Store.SaveCacheEntry(c.Request.Context(), run.ProjectID, key, "", cacheScope(run), req.BlobRef, size)
	if errors.Is(err, store.ErrExists) {
		_ = s.Blobs.Delete(req.BlobRef)
		fail(c, http.StatusConflict, "cache_exists", "a cache with this key already exists")
		return
	}
	if err != nil {
		_ = s.Blobs.Delete(req.BlobRef)
		s.internalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}
