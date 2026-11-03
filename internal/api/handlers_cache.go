package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

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
		fail(c, http.StatusNotFound, "cache_miss", "no cache entry matched")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	rc, size, err := s.Blobs.Open(entry.BlobRef)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		fail(c, http.StatusNotFound, "not_found", "cache content missing")
		return
	}
	defer rc.Close()
	s.Store.TouchCache(c.Request.Context(), entry.ID)
	c.Header("X-Kern-Cache-Key", entry.Key)
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
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
	blobRef := "cache/" + run.ProjectID.String() + "/" + url.PathEscape(key) + ".tar.gz"
	size, _, err := s.Blobs.Put(blobRef, io.LimitReader(c.Request.Body, 4<<30))
	if err != nil {
		s.internalError(c, err)
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
