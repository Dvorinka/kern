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
	OrgID     uuid.UUID       `json:"org_id"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Actor     string          `json:"actor"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"created_at"`
}

const auditSelect = `
	SELECT a.id, a.org_id, a.action, a.target, a.metadata, a.created_at,
	       coalesce(u.email, '')
	FROM audit_events a
	LEFT JOIN users u ON u.id = a.actor_user_id`

func scanAuditEvent(row interface{ Scan(...any) error }) (AuditEvent, error) {
	var e AuditEvent
	err := row.Scan(&e.ID, &e.OrgID, &e.Action, &e.Target, &e.Metadata, &e.CreatedAt, &e.Actor)
	return e, err
}

func (s *Store) ListAuditEvents(ctx context.Context, orgID uuid.UUID, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, auditSelect+`
		WHERE a.org_id = $1
		ORDER BY a.created_at DESC
		LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("audit event scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EachAuditEvent streams audit events oldest-first inside [from, to) — the
// shape a JSONL compliance export wants. A nil from/to bound means open.
// fn returning an error stops the walk and is returned to the caller.
func (s *Store) EachAuditEvent(ctx context.Context, orgID uuid.UUID, from, to *time.Time, fn func(AuditEvent) error) error {
	rows, err := s.pool.Query(ctx, auditSelect+`
		WHERE a.org_id = $1
		  AND ($2::timestamptz IS NULL OR a.created_at >= $2)
		  AND ($3::timestamptz IS NULL OR a.created_at < $3)
		ORDER BY a.created_at`, orgID, from, to)
	if err != nil {
		return fmt.Errorf("export audit events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return fmt.Errorf("audit event scan: %w", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PruneAuditEvents deletes audit rows older than the retention window and
// reports how many went. A zero/negative maxAge deletes nothing.
func (s *Store) PruneAuditEvents(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM audit_events WHERE created_at < now() - $1::interval`,
		maxAge.String())
	return tag.RowsAffected(), err
}
