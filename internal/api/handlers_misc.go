package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

func (s *Server) overview(c *gin.Context) {
	ctx, cancel := contextWithTimeout(c, 5*time.Second)
	defer cancel()
	o, err := s.Store.Overview(ctx)
	if err != nil {
		s.internalError(c, err)
		return
	}
	series, _ := s.Store.QueueSeries(ctx)
	failures, _ := s.Store.RecentFailures(ctx, 5)
	cacheEntries, cacheBytes, _ := s.Store.CacheStats(ctx)
	c.JSON(http.StatusOK, gin.H{
		"overview": o,
		"series":   series,
		"failures": failures,
		"cache":    gin.H{"entries": cacheEntries, "bytes": cacheBytes},
	})
}

// prometheus exposes a minimal text exposition format for scraping.
func (s *Server) prometheus(c *gin.Context) {
	ctx, cancel := contextWithTimeout(c, 3*time.Second)
	defer cancel()
	o, err := s.Store.Overview(ctx)
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "metrics_unavailable", "could not read metrics")
		return
	}
	var b strings.Builder
	writeMetric := func(name, help, kind string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %g\n", name, help, name, kind, name, value)
	}
	writeMetric("kern_runs_total", "Total workflow runs", "counter", float64(o.RunsTotal))
	writeMetric("kern_runs_success_total", "Successful workflow runs", "counter", float64(o.RunsSuccess))
	writeMetric("kern_runs_failure_total", "Failed workflow runs", "counter", float64(o.RunsFailure))
	writeMetric("kern_runs_active", "Runs currently queued or running", "gauge", float64(o.RunsActive))
	writeMetric("kern_jobs_queued", "Jobs waiting for a runner", "gauge", float64(o.JobsQueued))
	writeMetric("kern_jobs_running", "Jobs currently executing", "gauge", float64(o.JobsRunning))
	writeMetric("kern_runners_online", "Runners with a recent heartbeat", "gauge", float64(o.RunnersOnline))
	writeMetric("kern_job_queue_seconds_avg", "Average queue wait over 24h", "gauge", o.AvgQueueSeconds)
	writeMetric("kern_job_queue_seconds_p95", "p95 queue wait over 24h", "gauge", o.P95QueueSeconds)
	writeMetric("kern_run_seconds_avg", "Average run duration", "gauge", o.AvgRunSeconds)
	c.Header("Content-Type", "text/plain; version=0.0.4")
	c.String(http.StatusOK, b.String())
}

// githubWebhook verifies the HMAC, persists the delivery and acknowledges duplicates.
func (s *Server) githubWebhook(c *gin.Context) {
	secrets := s.webhookSecrets(c.Request.Context())
	if len(secrets) == 0 {
		fail(c, http.StatusNotImplemented, "github_not_configured", "no webhook secret is configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 25<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "could not read webhook payload")
		return
	}
	signature := c.GetHeader("X-Hub-Signature-256")
	valid := false
	for _, secret := range secrets {
		if validSignature(secret, body, signature) {
			valid = true
			break
		}
	}
	if !valid {
		fail(c, http.StatusUnauthorized, "invalid_signature", "webhook signature does not match")
		return
	}
	delivery := c.GetHeader("X-GitHub-Delivery")
	event := c.GetHeader("X-GitHub-Event")
	if delivery == "" || event == "" {
		fail(c, http.StatusBadRequest, "missing_headers", "X-GitHub-Delivery and X-GitHub-Event are required")
		return
	}
	inserted, err := s.Store.SaveWebhookDelivery(c.Request.Context(), "github", delivery, event, body)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !inserted {
		c.JSON(http.StatusOK, gin.H{"duplicate": true})
		return
	}
	status, detail := s.processGithubDelivery(c, delivery, event, body)
	if err := s.Store.MarkWebhookProcessed(c.Request.Context(), delivery, status, detail); err != nil {
		s.Log.Error("mark webhook processed", zap.Error(err))
	}
	s.Log.Info("github webhook received", zap.String("event", event),
		zap.String("delivery", delivery), zap.String("status", status), zap.String("detail", detail))
	c.JSON(http.StatusAccepted, gin.H{"delivery": delivery, "event": event, "status": status, "detail": detail})
}

func validSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// secretScope resolves the optional ?project=<slug> and ?environment=<name>
// scope into ids. Environment scope requires a project and must name an
// existing environment.
func (s *Server) secretScope(c *gin.Context) (*uuid.UUID, *uuid.UUID, bool) {
	user, _ := currentUser(c)
	var projectID *uuid.UUID
	if scope := c.Query("project"); scope != "" {
		p, err := s.Store.Project(c.Request.Context(), user.OrgID, scope)
		if err != nil {
			s.internalError(c, err)
			return nil, nil, false
		}
		projectID = &p.ID
	}
	var envID *uuid.UUID
	if envName := c.Query("environment"); envName != "" {
		if projectID == nil {
			fail(c, http.StatusBadRequest, "invalid_scope", "environment scope requires ?project=")
			return nil, nil, false
		}
		env, err := s.Store.Environment(c.Request.Context(), *projectID, envName)
		if err != nil {
			s.internalError(c, err)
			return nil, nil, false
		}
		envID = &env.ID
	}
	return projectID, envID, true
}

func (s *Server) listSecrets(c *gin.Context) {
	user, _ := currentUser(c)
	projectID, envID, ok := s.secretScope(c)
	if !ok {
		return
	}
	rows, err := s.Store.Secrets(c.Request.Context(), user.OrgID, projectID, envID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"secrets": secretSummaries(rows)})
}

// validSelfHeal enumerates the remediation ladder rungs; "inherit" is
// project-only, meaning "use the org value".
func validSelfHeal(v string, allowInherit bool) bool {
	switch v {
	case "off", "auto", "approve":
		return true
	case "inherit":
		return allowInherit
	}
	return false
}

func (s *Server) getOrgSettings(c *gin.Context) {
	user, _ := currentUser(c)
	raw, err := s.Store.OrgSettings(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"settings":  raw,
		"self_heal": settingsValue(raw, "self_heal", "off"),
		"public_ci": settingsValue(raw, "public_ci", "off"),
	})
}

type settingsRequest struct {
	SelfHeal string `json:"self_heal"`
	ForkGate string `json:"fork_gate"`
}

// orgSettingsRequest accepts either key. A self_heal-only body, which the
// runners page sends, must keep working.
type orgSettingsRequest struct {
	SelfHeal *string `json:"self_heal"`
	PublicCI *string `json:"public_ci"`
}

func orgSettingWrites(selfHeal, publicCI *string) (map[string]string, string) {
	if selfHeal == nil && publicCI == nil {
		return nil, "self_heal or public_ci is required"
	}
	out := map[string]string{}
	if selfHeal != nil {
		if !validSelfHeal(*selfHeal, false) {
			return nil, "self_heal must be off, auto or approve"
		}
		out["self_heal"] = *selfHeal
	}
	if publicCI != nil {
		if *publicCI != "on" && *publicCI != "off" {
			return nil, "public_ci must be on or off"
		}
		out["public_ci"] = *publicCI
	}
	return out, ""
}

func (s *Server) putOrgSettings(c *gin.Context) {
	user, _ := currentUser(c)
	var req orgSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "self_heal or public_ci is required")
		return
	}
	writes, msg := orgSettingWrites(req.SelfHeal, req.PublicCI)
	if msg != "" {
		fail(c, http.StatusBadRequest, "invalid_body", msg)
		return
	}
	for k, v := range writes {
		if err := s.Store.PutOrgSetting(c.Request.Context(), user.OrgID, k, v); err != nil {
			s.internalError(c, err)
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "settings."+k, v)
	}
	c.JSON(http.StatusOK, writes)
}

func (s *Server) getProjectSettings(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
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
		"settings":  raw,
		"self_heal": settingsValue(raw, "self_heal", "inherit"),
		"fork_gate": settingsValue(raw, "fork_gate", "off"),
	})
}

func validForkGate(v string) bool {
	switch v {
	case "off", "all", "first-time":
		return true
	}
	return false
}

func (s *Server) putProjectSettings(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	var req settingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if req.SelfHeal == "" && req.ForkGate == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "self_heal or fork_gate is required")
		return
	}
	writes := map[string]string{}
	if req.SelfHeal != "" {
		if !validSelfHeal(req.SelfHeal, true) {
			fail(c, http.StatusBadRequest, "invalid_body", "self_heal must be off, auto, approve or inherit")
			return
		}
		value := req.SelfHeal
		if value == "inherit" {
			value = "" // deleting the key falls back to the org setting
		}
		writes["self_heal"] = value
	}
	if req.ForkGate != "" {
		if !validForkGate(req.ForkGate) {
			fail(c, http.StatusBadRequest, "invalid_body", "fork_gate must be off, all or first-time")
			return
		}
		if req.ForkGate == "off" {
			writes["fork_gate"] = "" // deleting the key restores the default
		} else {
			writes["fork_gate"] = req.ForkGate
		}
	}
	for k, v := range writes {
		if err := s.Store.PutProjectSetting(c.Request.Context(), p.ID, k, v); err != nil {
			s.internalError(c, err)
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "settings."+k, p.Slug+"="+v)
	}
	resp := map[string]string{}
	if _, ok := writes["self_heal"]; ok {
		resp["self_heal"] = req.SelfHeal
	}
	if _, ok := writes["fork_gate"]; ok {
		resp["fork_gate"] = req.ForkGate
	}
	c.JSON(http.StatusOK, resp)
}

func settingsValue(raw json.RawMessage, key, def string) string {
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return def
	}
	if m[key] == "" {
		return def
	}
	return m[key]
}

// approveRemediation flips a step's proposal to approved and requeues the job.
// The retried job receives the fix commands in its claim payload; the runner
// applies them before the step. Audit-logged; no secrets are involved.
func (s *Server) approveRemediation(c *gin.Context) {
	user, _ := currentUser(c)
	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return
	}
	var req struct {
		Step int `json:"step"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Step <= 0 {
		fail(c, http.StatusBadRequest, "invalid_body", "step number is required")
		return
	}
	if err := s.Store.ApproveRemediation(c.Request.Context(), jobID, req.Step); err != nil {
		if strings.Contains(err.Error(), "no remediation proposal") {
			fail(c, http.StatusNotFound, "no_proposal", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "remediation.approve",
		fmt.Sprintf("job %s step %d", jobID, req.Step))
	c.JSON(http.StatusOK, gin.H{"ok": true, "requeued": true})
}

func secretSummaries(rows []store.Secret) []gin.H {
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id": r.ID, "name": r.Name, "project_id": r.ProjectID, "environment_id": r.EnvironmentID,
			"created_at": r.CreatedAt, "updated_at": r.UpdatedAt,
		})
	}
	return out
}

func (s *Server) putSecret(c *gin.Context) {
	user, _ := currentUser(c)
	name := strings.TrimSpace(c.Param("name"))
	if name == "" || strings.ContainsAny(name, " =") {
		fail(c, http.StatusBadRequest, "invalid_name", "secret name must not contain spaces or '='")
		return
	}
	projectID, envID, ok := s.secretScope(c)
	if !ok {
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "secret value is required")
		return
	}
	ct, nonce, err := s.Cipher.Encrypt(req.Value)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if err := s.Store.UpsertSecret(c.Request.Context(), user.OrgID, projectID, envID, name, ct, nonce); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "secret.write", name)
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteSecret(c *gin.Context) {
	user, _ := currentUser(c)
	name := c.Param("name")
	projectID, envID, ok := s.secretScope(c)
	if !ok {
		return
	}
	rows, err := s.Store.Secrets(c.Request.Context(), user.OrgID, projectID, envID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	for _, r := range rows {
		if r.Name == name {
			if err := s.Store.DeleteSecret(c.Request.Context(), user.OrgID, r.ID); err != nil {
				s.internalError(c, err)
				return
			}
			s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "secret.delete", name)
			c.Status(http.StatusNoContent)
			return
		}
	}
	fail(c, http.StatusNotFound, "not_found", "secret not found")
}
