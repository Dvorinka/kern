package api

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var artifactNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// uploadArtifact stores a tar.gz payload produced by actions/upload-artifact on the runner.
func (s *Server) uploadArtifact(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if !artifactNameRe.MatchString(name) {
		fail(c, http.StatusBadRequest, "invalid_name", "artifact name may contain letters, digits, '.', '_' and '-'")
		return
	}
	key := fmt.Sprintf("artifacts/%s/%s.tar.gz", job.RunID.String(), name)
	size, sha, err := s.Blobs.Put(key, io.LimitReader(c.Request.Body, 4<<30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	if msg := s.checkArtifactQuota(c, job.RunID, name, size); msg != "" {
		_ = s.Blobs.Delete(key)
		fail(c, http.StatusRequestEntityTooLarge, "quota_exceeded", msg)
		return
	}
	a, err := s.Store.CreateArtifact(c.Request.Context(), job.RunID, job.ID, name, key, size, sha)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"artifact": gin.H{"id": a.ID, "name": a.Name, "size": a.SizeBytes}})
}

// checkArtifactQuota enforces the project's artifact_quota_mb setting. An
// overwrite of an existing same-name artifact replaces its bytes rather than
// adding to the total. Returns "" when the upload fits or no quota is set.
func (s *Server) checkArtifactQuota(c *gin.Context, runID uuid.UUID, name string, size int64) string {
	run, err := s.Store.Run(c.Request.Context(), runID)
	if err != nil {
		return ""
	}
	raw, err := s.Store.ProjectSettings(c.Request.Context(), run.ProjectID)
	if err != nil {
		return ""
	}
	mb, err := strconv.ParseInt(settingsValue(raw, "artifact_quota_mb", "0"), 10, 64)
	if err != nil || mb <= 0 {
		return ""
	}
	quota := mb << 20
	existing := int64(0)
	if prev, err := s.Store.ArtifactByName(c.Request.Context(), runID, name); err == nil {
		existing = prev.SizeBytes
	}
	total, err := s.Store.ArtifactBytesForProject(c.Request.Context(), run.ProjectID)
	if err != nil {
		return ""
	}
	projected := total - existing + size
	if projected <= quota {
		return ""
	}
	return fmt.Sprintf("artifact quota exceeded: %d MB limit, upload would use %d MB",
		mb, projected>>20)
}

// downloadArtifact streams an artifact back to a runner for actions/download-artifact.
func (s *Server) downloadArtifact(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	a, err := s.Store.ArtifactByName(c.Request.Context(), job.RunID, c.Param("name"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	rc, size, err := s.Blobs.Open(a.BlobRef)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		fail(c, http.StatusNotFound, "not_found", "artifact content missing")
		return
	}
	defer rc.Close()
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
}

// jobArtifacts lists artifact names in this run (runner-side download-all).
func (s *Server) jobArtifacts(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	project, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rows, err := s.Store.Artifacts(c.Request.Context(), project.OrgID, &job.RunID, 500)
	if err != nil {
		s.internalError(c, err)
		return
	}
	names := []string{}
	for _, a := range rows {
		names = append(names, a.Name)
	}
	c.JSON(http.StatusOK, gin.H{"artifacts": names})
}

func (s *Server) listArtifacts(c *gin.Context) {
	user, _ := currentUser(c)
	var runID *uuid.UUID
	if v := c.Query("run_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_run_id", "run_id must be a uuid")
			return
		}
		runID = &id
	}
	rows, err := s.Store.Artifacts(c.Request.Context(), user.OrgID, runID, parseInt(c.DefaultQuery("limit", "200"), 200))
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"artifacts": rows})
}

func (s *Server) deleteArtifact(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "artifact id must be a uuid")
		return
	}
	ref, err := s.Store.DeleteArtifact(c.Request.Context(), user.OrgID, id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if err := s.Blobs.Delete(ref); err != nil {
		s.Log.Error("delete artifact blob", zap.String("ref", ref), zap.Error(err))
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "artifact.delete", id.String())
	c.Status(http.StatusNoContent)
}

func (s *Server) getArtifact(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "artifact id must be a uuid")
		return
	}
	a, err := s.Store.Artifact(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rc, size, err := s.Blobs.Open(a.BlobRef)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		fail(c, http.StatusNotFound, "not_found", "artifact content missing")
		return
	}
	defer rc.Close()
	c.Header("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(a.Name, `"`, "")+`.tar.gz"`)
	c.DataFromReader(http.StatusOK, size, "application/gzip", rc, nil)
}
