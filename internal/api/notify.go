package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/telemetry"
)

// notifyMaxAttempts bounds send tries per delivery (handoff: 3).
const notifyMaxAttempts = 3

// notifyRunCompleted enqueues the org's outbound webhook for a finished run.
// Delivery is persisted in notify_deliveries and driven by the leader-gated
// tick, so an api restart mid-send loses nothing. Filters and the per-project
// URL override resolve here, at enqueue time.
func (s *Server) notifyRunCompleted(ctx context.Context, run store.Run) {
	project, err := s.Store.ProjectByID(ctx, run.ProjectID)
	if err != nil {
		return
	}
	orgRaw, err := s.Store.OrgSettings(ctx, project.OrgID)
	if err != nil {
		return
	}
	projRaw, _ := s.Store.ProjectSettings(ctx, project.ID)

	projNotify := settingsValue(projRaw, "notify_url", "")
	if projNotify == "off" {
		return // project opted out of notifications entirely
	}
	url := strings.TrimSpace(projNotify)
	if url == "" {
		url = strings.TrimSpace(settingsValue(orgRaw, "notify_url", ""))
	}
	if url == "" || !notifyConclusionAllowed(settingsValue(orgRaw, "notify_events", ""), run.Conclusion) {
		return
	}
	payload, err := notifyPayload(run, project, url, settingsValue(orgRaw, "notify_format", "kern"), s.runPageURL(ctx, run))
	if err != nil {
		return
	}
	if err := s.Store.EnqueueNotify(ctx, project.OrgID, project.ID, run.ID, url, payload); err != nil {
		s.Log.Warn("enqueue notify", zap.String("run", run.ID.String()), zap.Error(err))
	}
}

// notifyConclusionAllowed reports whether a run conclusion passes the org's
// notify_events filter. Empty filter means every conclusion notifies.
func notifyConclusionAllowed(filter, conclusion string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	for _, f := range strings.Split(filter, ",") {
		if strings.EqualFold(strings.TrimSpace(f), conclusion) {
			return true
		}
	}
	return false
}

// notifyRunnersOffline enqueues a runner.offline event per runner that just
// flipped offline — one delivery per runner per org, only while the org has
// a notify_url configured. The transition itself is detected in the
// orchestrator's stale-runner sweep; alerting fires once per episode.
func (s *Server) NotifyRunnersOffline(ctx context.Context, runners []store.OfflineRunner) {
	seen := map[[16]byte]struct{}{}
	for _, r := range runners {
		if _, dup := seen[r.OrgID]; dup {
			continue
		}
		seen[r.OrgID] = struct{}{}
		orgRaw, err := s.Store.OrgSettings(ctx, r.OrgID)
		if err != nil {
			continue
		}
		if settingsValue(orgRaw, "notify_runner_offline", "on") == "off" {
			continue
		}
		url := strings.TrimSpace(settingsValue(orgRaw, "notify_url", ""))
		if url == "" {
			continue
		}
		var payload []byte
		if settingsValue(orgRaw, "notify_format", "kern") == "slack" {
			payload, _ = json.Marshal(map[string]any{
				"text": fmt.Sprintf("KERN runner %s went offline", r.Name),
			})
		} else {
			payload, _ = json.Marshal(map[string]any{
				"event":  "runner.offline",
				"runner": map[string]any{"id": r.ID, "name": r.Name},
			})
		}
		if err := s.Store.EnqueueNotifyEvent(ctx, r.OrgID, nil, nil, "runner.offline", url, payload); err != nil {
			s.Log.Warn("enqueue runner.offline", zap.String("runner", r.Name), zap.Error(err))
		}
	}
}

// notifyPayload builds the webhook body. format "slack" emits Slack's
// incoming-webhook {"text": ...} shape; anything else keeps the kern event
// envelope.
func notifyPayload(run store.Run, project store.Project, url, format, pageURL string) ([]byte, error) {
	if format == "slack" {
		return json.Marshal(map[string]any{
			"text": fmt.Sprintf("KERN %s/%s run #%d %s: %s",
				project.Slug, run.WorkflowName, run.RunNumber, run.Conclusion, pageURL),
		})
	}
	return json.Marshal(map[string]any{
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
			"url":        pageURL,
		},
	})
}

// DeliverNotifyBatch sends every due notification. Wired as the
// leader-gated orchestrator's NotifySweep, so exactly one replica delivers
// at a time.
func (s *Server) DeliverNotifyBatch(ctx context.Context) {
	due, err := s.Store.DueNotifyDeliveries(ctx, 20)
	if err != nil {
		s.Log.Warn("notify sweep", zap.Error(err))
		return
	}
	for _, d := range due {
		s.deliverNotify(ctx, d)
	}
}

// deliverNotify performs one send attempt and records the outcome. The HMAC
// secret comes from current org settings — a rotated or cleared secret
// applies to rows enqueued before the change.
func (s *Server) deliverNotify(ctx context.Context, d store.NotifyDelivery) {
	orgRaw, err := s.Store.OrgSettings(ctx, d.OrgID)
	if err != nil {
		s.finishNotify(ctx, d, "org settings: "+err.Error())
		return
	}
	// A cleared or "off" notify_url disables delivery for rows already queued.
	// Org-level events carry no project row.
	live := ""
	if d.ProjectID != nil {
		projRaw, _ := s.Store.ProjectSettings(ctx, *d.ProjectID)
		projNotify := settingsValue(projRaw, "notify_url", "")
		if projNotify == "off" {
			_ = s.Store.MarkNotifyDead(ctx, d.ID, d.Attempts+1, "project notify disabled")
			return
		}
		live = strings.TrimSpace(projNotify)
	}
	if live == "" {
		live = strings.TrimSpace(settingsValue(orgRaw, "notify_url", ""))
	}
	if live == "" {
		_ = s.Store.MarkNotifyDead(ctx, d.ID, d.Attempts+1, "notify_url removed")
		return
	}

	// The send target is the live setting, not the enqueued URL: retargeting
	// notify_url moves pending deliveries to the new endpoint.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, live, bytes.NewReader(d.Payload))
	if err != nil {
		s.finishNotify(ctx, d, "bad url: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	event := d.Event
	if event == "" {
		event = "run.completed"
	}
	req.Header.Set("X-Kern-Event", event)
	if secret := s.notifySecret(d.OrgID, orgRaw); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(d.Payload)
		req.Header.Set("X-Kern-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := notifyHTTPClient.Do(req)
	if err != nil {
		telemetry.IncNotify("error")
		s.finishNotify(ctx, d, err.Error())
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		telemetry.IncNotify("error")
		s.finishNotify(ctx, d, fmt.Sprintf("http %d", resp.StatusCode))
		return
	}
	telemetry.IncNotify("sent")
	if err := s.Store.MarkNotifySent(ctx, d.ID); err != nil {
		s.Log.Warn("mark notify sent", zap.Error(err))
	}
}

// finishNotify records a failed attempt: backoff-reschedule under the cap,
// dead-letter beyond it.
func (s *Server) finishNotify(ctx context.Context, d store.NotifyDelivery, cause string) {
	n := d.Attempts + 1
	if n >= notifyMaxAttempts {
		telemetry.IncNotify("dead")
		if err := s.Store.MarkNotifyDead(ctx, d.ID, n, cause); err != nil {
			s.Log.Warn("mark notify dead", zap.Error(err))
		}
		s.Log.Warn("notify delivery failed permanently", zap.String("delivery", d.ID.String()), zap.String("cause", cause))
		return
	}
	if err := s.Store.MarkNotifyFailed(ctx, d.ID, n, cause); err != nil {
		s.Log.Warn("mark notify failed", zap.Error(err))
	}
	s.Log.Warn("notify delivery attempt failed", zap.String("delivery", d.ID.String()),
		zap.Int("attempt", n), zap.String("cause", cause))
}

// notifySecret resolves the org's webhook HMAC secret. Values written by
// putOrgSettings are stored as "enc1:<b64ct>:<b64nonce>" AES-GCM ciphertext
// bound to the org; legacy plaintext settings still verify (#368). An
// undecryptable value signs nothing rather than leaking an error detail.
func (s *Server) notifySecret(orgID uuid.UUID, raw json.RawMessage) string {
	v := strings.TrimSpace(settingsValue(raw, "notify_secret", ""))
	if !strings.HasPrefix(v, "enc1:") {
		return v
	}
	parts := strings.SplitN(v, ":", 3)
	if len(parts) != 3 {
		return ""
	}
	ct, err1 := base64.StdEncoding.DecodeString(parts[1])
	nonce, err2 := base64.StdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return ""
	}
	dec, err := s.Cipher.DecryptAAD(ct, nonce, "notify_secret|"+orgID.String())
	if err != nil {
		return ""
	}
	return dec
}

// publicNotifyIP reports whether ip is a globally routable unicast address —
// loopback, private, link-local (incl. 169.254.169.254 metadata), multicast
// and unspecified ranges are all rejected (#368).
func publicNotifyIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

// notifyURLHostSafe is the write-time half of the SSRF guard: literal IP
// hosts must be public. Hostnames that resolve to internal addresses are
// still caught at dial time by notifySafeDial (#368).
func notifyURLHostSafe(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return publicNotifyIP(ip)
	}
	return true
}

// notifySafeDial resolves the target host once and dials a checked public
// IP, so DNS rebinding cannot swap in an internal address after validation.
// Redirects re-enter through this dialer, so they get the same check.
func notifySafeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !publicNotifyIP(ip) {
			return nil, fmt.Errorf("notify_url target %s is not a public address", host)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if publicNotifyIP(ip) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
	}
	return nil, fmt.Errorf("notify_url host %s resolves only to non-public addresses", host)
}

// notifyHTTPClient is the send path's client; a var so tests can stub it.
var notifyHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: notifySafeDial,
	},
}
