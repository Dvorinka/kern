package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Notification is one row of the derived attention feed. Nothing is stored:
// every row is computed from live state, so the feed can never lie about what
// still needs a human. kinds: run_approval, remediation, run_failed,
// runner_offline.
type Notification struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Sub       string    `json:"sub"`
	Href      string    `json:"href"`
	JobID     string    `json:"job_id,omitempty"`
	Step      int       `json:"step,omitempty"`
	Fix       []string  `json:"fix,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PendingRemediations lists failed steps carrying a fixable, not-yet-acted-on
// remediation proposal. Approving one requeues its job, so the row disappears
// from the feed the moment it is handled.
func (s *Store) PendingRemediations(ctx context.Context, orgID uuid.UUID, limit int) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, s.number, r.id, r.run_number, p.slug, s.name,
		       s.diagnosis->>'summary', s.completed_at,
		       s.diagnosis->'remediation'->'fix'
		FROM steps s
		JOIN jobs j ON j.id = s.job_id
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1
		  AND j.conclusion = 'failure'
		  AND s.diagnosis->>'fixable' = 'true'
		  AND s.diagnosis ? 'remediation'
		  AND coalesce(s.diagnosis->'remediation'->>'approved', 'false') = 'false'
		  AND coalesce(s.diagnosis->'remediation'->>'applied', 'false') = 'false'
		  AND coalesce(s.diagnosis->'remediation'->>'dismissed', 'false') = 'false'
		ORDER BY s.completed_at DESC
		LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("pending remediations: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var jobID, runID uuid.UUID
		var runNo int
		var slug, stepName, summary string
		var fixJSON []byte
		if err := rows.Scan(&jobID, &n.Step, &runID, &runNo, &slug, &stepName, &summary, &n.CreatedAt, &fixJSON); err != nil {
			return nil, fmt.Errorf("pending remediation scan: %w", err)
		}
		n.ID = "rem-" + jobID.String() + "-" + fmt.Sprint(n.Step)
		n.Kind = "remediation"
		n.Title = summary
		n.Sub = fmt.Sprintf("%s · run #%d · step %s", slug, runNo, stepName)
		n.Href = "/runs/" + runID.String()
		n.JobID = jobID.String()
		_ = json.Unmarshal(fixJSON, &n.Fix)
		out = append(out, n)
	}
	return out, rows.Err()
}

// AwaitingApprovalRuns lists fork-gated runs parked on the approval hold.
func (s *Store) AwaitingApprovalRuns(ctx context.Context, orgID uuid.UUID) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.run_number, w.name, p.slug, r.actor, r.created_at
		FROM workflow_runs r
		JOIN workflows w ON w.id = r.workflow_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND r.status = 'awaiting_approval'
		ORDER BY r.created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("awaiting approval runs: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var runID uuid.UUID
		var runNo int
		var wf, slug, actor string
		if err := rows.Scan(&runID, &runNo, &wf, &slug, &actor, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("awaiting approval scan: %w", err)
		}
		n.ID = "gate-" + runID.String()
		n.Kind = "run_approval"
		n.Title = fmt.Sprintf("%s #%d waits on fork approval", wf, runNo)
		n.Sub = fmt.Sprintf("%s · by %s", slug, actor)
		n.Href = "/runs/" + runID.String()
		out = append(out, n)
	}
	return out, rows.Err()
}

// RecentFailedRuns surfaces completed runs that concluded failure inside the
// trailing window — the ambient "something broke" feed.
func (s *Store) RecentFailedRuns(ctx context.Context, orgID uuid.UUID, since time.Duration, limit int) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.run_number, w.name, p.slug, r.commit_message, r.completed_at
		FROM workflow_runs r
		JOIN workflows w ON w.id = r.workflow_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1
		  AND r.status = 'completed' AND r.conclusion = 'failure'
		  AND r.completed_at >= now() - $2::interval
		ORDER BY r.completed_at DESC
		LIMIT $3`, orgID, fmt.Sprintf("%d seconds", int64(since.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("recent failed runs: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var runID uuid.UUID
		var runNo int
		var wf, slug, msg string
		if err := rows.Scan(&runID, &runNo, &wf, &slug, &msg, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("recent failure scan: %w", err)
		}
		n.ID = "fail-" + runID.String()
		n.Kind = "run_failed"
		n.Title = fmt.Sprintf("%s #%d failed", wf, runNo)
		n.Sub = fmt.Sprintf("%s · %s", slug, firstLine(msg))
		n.Href = "/runs/" + runID.String()
		out = append(out, n)
	}
	return out, rows.Err()
}

// WebhookFailures lists deliveries that errored inside the trailing window —
// the push that never became a run.
func (s *Store) WebhookFailures(ctx context.Context, orgID uuid.UUID, since time.Duration, limit int) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.event, d.error, d.received_at
		FROM webhook_deliveries d
		WHERE d.status = 'failed'
		  AND d.received_at >= now() - $2::interval
		ORDER BY d.received_at DESC
		LIMIT $3`, orgID, fmt.Sprintf("%d seconds", int64(since.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("webhook failures: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var id uuid.UUID
		var event, derr string
		if err := rows.Scan(&id, &event, &derr, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("webhook failure scan: %w", err)
		}
		n.ID = "whf-" + id.String()
		n.Kind = "webhook_failed"
		n.Title = fmt.Sprintf("%s delivery failed", event)
		n.Sub = firstLine(derr)
		n.Href = "/deliveries"
		out = append(out, n)
	}
	return out, rows.Err()
}

// OfflineRunners lists runners that checked in within the trailing window
// but no longer report online — recently alive, now silent.
func (s *Store) OfflineRunners(ctx context.Context, orgID uuid.UUID, since time.Duration) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, last_heartbeat_at
		FROM runners
		WHERE org_id = $1 AND status = 'offline'
		  AND last_heartbeat_at >= now() - $2::interval
		ORDER BY last_heartbeat_at DESC`, orgID, fmt.Sprintf("%d seconds", int64(since.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("offline runners: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("offline runner scan: %w", err)
		}
		n.ID = "off-" + id.String()
		n.Kind = "runner_offline"
		n.Title = fmt.Sprintf("runner %s went offline", name)
		n.Sub = "last heartbeat seen"
		n.Href = "/runners"
		out = append(out, n)
	}
	return out, rows.Err()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
