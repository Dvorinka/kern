package api

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	a, err := s.Store.CreateArtifact(c.Request.Context(), job.RunID, job.ID, name, key, size, sha)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"artifact": gin.H{"id": a.ID, "name": a.Name, "size": a.SizeBytes}})
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
	rows, err := s.Store.Artifacts(c.Request.Context(), project.OrgID, 500)
	if err != nil {
		s.internalError(c, err)
		return
	}
	names := []string{}
	for _, a := range rows {
		if a.RunID == job.RunID {
			names = append(names, a.Name)
		}
	}
	c.JSON(http.StatusOK, gin.H{"artifacts": names})
}

func (s *Server) listArtifacts(c *gin.Context) {
	user, _ := currentUser(c)
	rows, err := s.Store.Artifacts(c.Request.Context(), user.OrgID, parseInt(c.DefaultQuery("limit", "200"), 200))
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"artifacts": rows})
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
