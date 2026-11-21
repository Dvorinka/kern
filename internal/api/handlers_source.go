package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var bundleDigestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// bundleRef names a source blob: content-addressed when the client supplies a
// digest, a fresh uuid otherwise. Keys carry org and project ids so two
// tenants can never share — or guess — each other's bundle namespace (#368).
func bundleRef(orgID, projectID uuid.UUID, digest string) string {
	name := uuid.NewString()
	if bundleDigestRe.MatchString(digest) {
		name = digest
	}
	return fmt.Sprintf("sources/%s/%s/%s.tar.gz", orgID, projectID, name)
}

// uploadSource stores a tar.gz workspace bundle produced by `kern exec`. The
// blob reference it returns is passed back in the run trigger as bundle_ref.
// With ?sha256=<content digest> the store becomes content-addressed: an
// identical tree already present is returned without being written again.
func (s *Server) uploadSource(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	digest := c.Query("sha256")
	if digest != "" && !bundleDigestRe.MatchString(digest) {
		fail(c, http.StatusBadRequest, "invalid_digest", "sha256 must be 64 lowercase hex characters")
		return
	}
	ref := bundleRef(user.OrgID, p.ID, digest)
	if digest != "" {
		if rc, _, err := s.Blobs.Open(ref); err == nil && rc != nil {
			rc.Close()
			size, _ := s.Blobs.Size(ref)
			c.JSON(http.StatusOK, gin.H{"bundle_ref": ref, "size_bytes": size, "deduped": true})
			return
		}
	}
	size, sha, err := s.Blobs.Put(ref, io.LimitReader(c.Request.Body, 1<<30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	// A client-supplied digest names the blob — the content must actually
	// hash to it, or anyone could poison a digest another tenant resolves
	// to different bytes (#368).
	if digest != "" && sha != digest {
		_ = s.Blobs.Delete(ref)
		fail(c, http.StatusBadRequest, "digest_mismatch", "uploaded content does not match the sha256 digest")
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "source.upload", ref)
	c.JSON(http.StatusCreated, gin.H{"bundle_ref": ref, "size_bytes": size, "sha256": sha})
}

// resolveSource maps a content digest to an existing bundle, letting `kern
// exec` skip the upload entirely on a hit.
func (s *Server) resolveSource(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	digest := c.Query("sha256")
	if !bundleDigestRe.MatchString(digest) {
		fail(c, http.StatusBadRequest, "invalid_digest", "sha256 must be 64 lowercase hex characters")
		return
	}
	ref := bundleRef(user.OrgID, p.ID, digest)
	rc, _, err := s.Blobs.Open(ref)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if rc == nil {
		fail(c, http.StatusNotFound, "not_found", "no bundle for this content digest")
		return
	}
	rc.Close()
	size, _ := s.Blobs.Size(ref)
	c.JSON(http.StatusOK, gin.H{"bundle_ref": ref, "size_bytes": size, "deduped": true})
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
	// The ref must live in this run's own org+project namespace. The legacy
	// slug-scoped prefix stays readable so bundles uploaded before org
	// keying still resolve; new triggers only mint the scoped form (#368).
	project, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	scoped := fmt.Sprintf("sources/%s/%s/", project.OrgID, project.ID)
	legacy := "sources/" + project.Slug + "/"
	if !strings.HasPrefix(ref, scoped) && !strings.HasPrefix(ref, legacy) {
		fail(c, http.StatusNotFound, "not_found", "source bundle not found for this run")
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
