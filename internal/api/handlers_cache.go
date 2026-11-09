package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

// GitHub cache keys: up to 512 chars, commas forbidden (they separate restore keys).
var cacheKeyRe = regexp.MustCompile(`^[^,]{1,512}$`)

// cacheScope maps run trust onto the cache namespace. Pull-request runs read
// trusted entries (base-branch caches) but save into their own scope so an
// untrusted ref cannot poison the shared cache.
func cacheScope(tl string) string {
	if tl == "pull_request" || tl == "fork" {
		return "pull_request"
	}
	return "trusted"
}

func cacheReadScopes(tl string) []string {
	if tl == "pull_request" || tl == "fork" {
		return []string{"pull_request", "trusted"}
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
	entry, err := s.Store.CacheLookup(c.Request.Context(), run.ProjectID, key, prefixes, cacheReadScopes(run.TrustLevel))
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
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "cache id must be a uuid")
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
	blobRef := "cache/" + run.ProjectID.String() + "/" + url.PathEscape(key) + ".tar.gz"
	size, _, err := s.Blobs.Put(blobRef, io.LimitReader(c.Request.Body, 4<<30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	if max > 0 && size > max {
		if derr := s.Blobs.Delete(blobRef); derr != nil {
			s.Log.Error("delete oversized cache blob", zap.String("ref", blobRef), zap.Error(derr))
		}
		fail(c, http.StatusRequestEntityTooLarge, "cache_too_large",
			"cache archive exceeds KERN_CACHE_MAX_BYTES")
		return
	}
	_, err = s.Store.SaveCacheEntry(c.Request.Context(), run.ProjectID, key, "", cacheScope(run.TrustLevel), blobRef, size)
	if errors.Is(err, store.ErrExists) {
		fail(c, http.StatusConflict, "cache_exists", "a cache with this key already exists")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": key, "size": size})
}
