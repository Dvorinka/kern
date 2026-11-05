package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuditEvent is one row of the org audit trail, joined with the actor's
// email when the action came from a signed-in user (empty for runner/system
// actions).
type AuditEvent struct {
	ID        uuid.UUID       `json:"id"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Actor     string          `json:"actor"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) ListAuditEvents(ctx context.Context, orgID uuid.UUID, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.action, a.target, a.metadata, a.created_at,
		       coalesce(u.email, '')
		FROM audit_events a
		LEFT JOIN users u ON u.id = a.actor_user_id
		WHERE a.org_id = $1
		ORDER BY a.created_at DESC
		LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Action, &e.Target, &e.Metadata, &e.CreatedAt, &e.Actor); err != nil {
			return nil, fmt.Errorf("audit event scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
