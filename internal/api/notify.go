package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

// notifyRunCompleted delivers the org's outbound webhook for a finished run.
// It is best-effort and non-blocking: one POST, a short timeout, failures are
// logged and audited nowhere — retries belong to the receiver, not here.
func (s *Server) notifyRunCompleted(ctx context.Context, run store.Run) {
	project, err := s.Store.ProjectByID(ctx, run.ProjectID)
	if err != nil {
		return
	}
	raw, err := s.Store.OrgSettings(ctx, project.OrgID)
	if err != nil {
		return
	}
	url := settingsValue(raw, "notify_url", "")
	if url == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"event": "run.completed",
		"run": map[string]any{
			"id":         run.ID,
			"project":    run.ProjectSlug,
			"workflow":   run.WorkflowName,
			"number":     run.RunNumber,
			"conclusion": run.Conclusion,
			"event":      run.Event,
			"ref":        run.Ref,
			"sha":        run.SHA,
			"url":        s.runPageURL(ctx, run),
		},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kern-Event", "run.completed")
	if secret := settingsValue(raw, "notify_secret", ""); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-Kern-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req = req.WithContext(ctx)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			s.Log.Warn("notify webhook failed", zap.String("run", run.ID.String()), zap.Error(err))
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			s.Log.Warn("notify webhook rejected", zap.String("run", run.ID.String()), zap.Int("status", resp.StatusCode))
		}
	}()
}
