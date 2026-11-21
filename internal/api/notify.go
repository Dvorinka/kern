package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

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
		if _, dup := seen[r.ID]; dup {
			continue
		}
		seen[r.ID] = struct{}{}
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
	if secret := settingsValue(orgRaw, "notify_secret", ""); secret != "" {
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

// notifyHTTPClient is the send path's client; a var so tests can stub it.
var notifyHTTPClient = &http.Client{Timeout: 10 * time.Second}

// LISTEN/NOTIFY fan-out for live followers. One dedicated connection LISTENs
// on kern_step_log; subscribers get a coalesced wake per step instead of
// polling the store every few hundred milliseconds (#374). Delivery is
// best-effort — subscribers keep a slow fallback tick, so a dead listener
// degrades to polling rather than stalling.

const stepLogChannel = "kern_step_log"

// logBus fans notifications out to in-process subscribers keyed by
// "jobID:number".
type logBus struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func newLogBus() *logBus {
	return &logBus{subs: map[string]map[chan struct{}]struct{}{}}
}

func (b *logBus) subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = map[chan struct{}]struct{}{}
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[key], ch)
		if len(b.subs[key]) == 0 {
			delete(b.subs, key)
		}
		b.mu.Unlock()
	}
}

func (b *logBus) publish(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[key] {
		select {
		case ch <- struct{}{}:
		default: // coalesce: one pending wake is enough
		}
	}
}

// listenStepLog holds a LISTEN connection and republishes payloads. Runs for
// the server's lifetime; reconnects after 5s on any failure.
func (s *Server) listenStepLog() {
	for {
		if err := s.listenStepLogOnce(); err != nil {
			s.Log.Warn("step log listener dropped; retrying", zap.Error(err))
		}
		time.Sleep(5 * time.Second)
	}
}

func (s *Server) listenStepLogOnce() error {
	ctx := context.Background()
	conn, err := s.Store.Pool().Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+stepLogChannel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.stepLogBus.publish(n.Payload)
	}
}
