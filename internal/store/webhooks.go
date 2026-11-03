package store

import (
	"context"
	"encoding/json"
)

// SaveWebhookDelivery persists a provider delivery exactly once. It reports whether the
// delivery was new, so retries from the provider cannot trigger duplicate work.
func (s *Store) SaveWebhookDelivery(ctx context.Context, provider, deliveryID, event string, payload []byte) (bool, error) {
	if !json.Valid(payload) {
		return false, errInvalidJSON
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (provider, delivery_id, event, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, delivery_id) DO NOTHING`,
		provider, deliveryID, event, payload)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) MarkWebhookProcessed(ctx context.Context, deliveryID, status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET status = $2, error = $3, processed_at = now()
		WHERE provider = 'github' AND delivery_id = $1`, deliveryID, status, errMsg)
	return err
}

type invalidJSONError struct{}

func (invalidJSONError) Error() string { return "webhook payload is not valid JSON" }

var errInvalidJSON = invalidJSONError{}
