package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SaveWebhookDelivery persists a provider delivery exactly once. It reports whether the
// delivery was new, so retries from the provider cannot trigger duplicate work.
// orgID is the org the verifying credential belongs to, when known — the
// payload's repo may still resolve to a different (or additional) org during
// processing, which SetWebhookDeliveryOrg records afterwards.
func (s *Store) SaveWebhookDelivery(ctx context.Context, provider, deliveryID, event string, payload []byte, orgID *uuid.UUID) (bool, error) {
	if !json.Valid(payload) {
		return false, errInvalidJSON
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (provider, delivery_id, event, payload, org_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider, delivery_id) DO NOTHING`,
		provider, deliveryID, event, payload, orgID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetWebhookDeliveryOrg stamps the owning org once the payload's repository
// has been resolved to a project. First writer wins — a redelivery processed
// under a different resolution cannot move the row between tenants.
func (s *Store) SetWebhookDeliveryOrg(ctx context.Context, deliveryID string, orgID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET org_id = $2
		WHERE provider = 'github' AND delivery_id = $1 AND org_id IS NULL`, deliveryID, orgID)
	return err
}

func (s *Store) MarkWebhookProcessed(ctx context.Context, deliveryID, status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET status = $2, error = $3, processed_at = now()
		WHERE provider = 'github' AND delivery_id = $1`, deliveryID, status, errMsg)
	return err
}

type WebhookDelivery struct {
	ID          uuid.UUID       `json:"id"`
	Provider    string          `json:"provider"`
	DeliveryID  string          `json:"delivery_id"`
	Event       string          `json:"event"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Status      string          `json:"status"`
	Error       string          `json:"error"`
	ReceivedAt  time.Time       `json:"received_at"`
	ProcessedAt *time.Time      `json:"processed_at"`
}

// WebhookDeliveries lists recent deliveries newest-first, payload excluded —
// fetch it per-row with WebhookDelivery when the UI expands one. Empty
// status/event strings mean "no filter".
func (s *Store) WebhookDeliveries(ctx context.Context, orgID uuid.UUID, status, event string, limit int) ([]WebhookDelivery, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, delivery_id, event, status, error, received_at, processed_at
		FROM webhook_deliveries
		WHERE org_id = $1 AND ($2 = '' OR status = $2) AND ($3 = '' OR event = $3)
		ORDER BY received_at DESC LIMIT $4`, orgID, status, event, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookDelivery{}
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.ID, &d.Provider, &d.DeliveryID, &d.Event,
			&d.Status, &d.Error, &d.ReceivedAt, &d.ProcessedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// WebhookDeliveryStats counts the whole table for the deliveries page —
// the list is a bounded window, the pills and KPIs must not be.
func (s *Store) WebhookDeliveryStats(ctx context.Context, orgID uuid.UUID) (total int, byStatus, byEvent map[string]int, err error) {
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE org_id = $1`, orgID).Scan(&total); err != nil {
		return 0, nil, nil, err
	}
	if byStatus, err = s.deliveryGroupCounts(ctx, orgID, "status"); err != nil {
		return 0, nil, nil, err
	}
	if byEvent, err = s.deliveryGroupCounts(ctx, orgID, "event"); err != nil {
		return 0, nil, nil, err
	}
	return total, byStatus, byEvent, nil
}

func (s *Store) deliveryGroupCounts(ctx context.Context, orgID uuid.UUID, col string) (map[string]int, error) {
	// col is an internal constant — never user input.
	rows, err := s.pool.Query(ctx,
		`SELECT `+col+`, count(*) FROM webhook_deliveries WHERE org_id = $1 GROUP BY `+col, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (s *Store) WebhookDelivery(ctx context.Context, orgID, id uuid.UUID) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider, delivery_id, event, payload, status, error, received_at, processed_at
		FROM webhook_deliveries WHERE id = $1 AND org_id = $2`, id, orgID).
		Scan(&d.ID, &d.Provider, &d.DeliveryID, &d.Event, &d.Payload,
			&d.Status, &d.Error, &d.ReceivedAt, &d.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	return d, err
}

// PruneWebhookDeliveries drops deliveries older than age. Provider retries
// arrive within hours, so a 30-day horizon cannot resurrect duplicate work.
func (s *Store) PruneWebhookDeliveries(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM webhook_deliveries
		WHERE received_at < now() - make_interval(secs => $1)`,
		int64(age.Seconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type invalidJSONError struct{}

func (invalidJSONError) Error() string { return "webhook payload is not valid JSON" }

var errInvalidJSON = invalidJSONError{}
