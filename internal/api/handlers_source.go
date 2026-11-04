package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// uploadSource stores a tar.gz workspace bundle produced by `kern exec`. The
// blob reference it returns is passed back in the run trigger as bundle_ref.
func (s *Server) uploadSource(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	ref := fmt.Sprintf("sources/%s/%s.tar.gz", p.Slug, uuid.NewString())
	size, sha, err := s.Blobs.Put(ref, io.LimitReader(c.Request.Body, 1<<30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "source.upload", ref)
	c.JSON(http.StatusCreated, gin.H{"bundle_ref": ref, "size_bytes": size, "sha256": sha})
}

// jobSource streams a run's uploaded workspace bundle to the runner leasing the
// job. Bundle refs are minted by uploadSource and carried in run.source.
func (s *Server) jobSource(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	var src map[string]any
	_ = json.Unmarshal(run.Source, &src)
	ref, _ := src["bundle_ref"].(string)
	if ref == "" || !strings.HasPrefix(ref, "sources/") {
		fail(c, http.StatusNotFound, "not_found", "run has no uploaded source bundle")
		return
	}
	rc, size, err := s.Blobs.Open(ref)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		fail(c, http.StatusNotFound, "not_found", "source bundle content missing")
		return
	}
	defer rc.Close()
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
}
