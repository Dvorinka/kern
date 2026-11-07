package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NotifyDelivery is one outbound notification attempt queue entry. The URL
// and payload are frozen at enqueue time; the signing secret is resolved from
// current org settings at send time so rotations apply to pending rows.
type NotifyDelivery struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	ProjectID   uuid.UUID  `json:"project_id"`
	RunID       uuid.UUID  `json:"run_id"`
	URL         string     `json:"url"`
	Payload     []byte     `json:"-"`
	Attempts    int        `json:"attempts"`
	NextAttempt time.Time  `json:"next_attempt_at"`
	LastError   string     `json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

// EnqueueNotify inserts a delivery due immediately.
func (s *Store) EnqueueNotify(ctx context.Context, orgID, projectID, runID uuid.UUID, url string, payload []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notify_deliveries (org_id, project_id, run_id, url, payload)
		VALUES ($1, $2, $3, $4, $5)`, orgID, projectID, runID, url, payload)
	return err
}

// DueNotifyDeliveries returns undelivered rows whose backoff has elapsed,
// oldest first. The leader-gated tick is the only caller.
func (s *Store) DueNotifyDeliveries(ctx context.Context, limit int) ([]NotifyDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, run_id, url, payload, attempts,
		       next_attempt_at, last_error, created_at, delivered_at
		FROM notify_deliveries
		WHERE delivered_at IS NULL AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyDelivery{}
	for rows.Next() {
		var d NotifyDelivery
		if err := rows.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.RunID, &d.URL, &d.Payload,
			&d.Attempts, &d.NextAttempt, &d.LastError, &d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkNotifySent records a successful delivery.
func (s *Store) MarkNotifySent(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE notify_deliveries SET delivered_at = now() WHERE id = $1`, id)
	return err
}

// MarkNotifyFailed records a failed attempt and schedules the next one with
// exponential backoff (base 5s, cap 10m, max 3 attempts total).
func (s *Store) MarkNotifyFailed(ctx context.Context, id uuid.UUID, attempts int, cause string) error {
	backoff := time.Duration(1<<uint(attempts)) * 5 * time.Second
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE notify_deliveries
		SET attempts = $2, last_error = $3, next_attempt_at = now() + $4::interval
		WHERE id = $1`, id, attempts, cause, backoff.String())
	return err
}

// MarkNotifyDead gives up on a delivery after max attempts.
func (s *Store) MarkNotifyDead(ctx context.Context, id uuid.UUID, attempts int, cause string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE notify_deliveries
		SET attempts = $2, last_error = $3, delivered_at = now()
		WHERE id = $1`, id, attempts, cause)
	return err
}

// PruneNotifyDeliveries removes terminal rows older than maxAge.
func (s *Store) PruneNotifyDeliveries(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM notify_deliveries WHERE delivered_at IS NOT NULL
		  AND delivered_at < now() - $1::interval`, maxAge.String())
	return tag.RowsAffected(), err
}
