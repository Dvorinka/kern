package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) FirstOrg(ctx context.Context) (Org, error) {
	var o Org
	err := s.pool.QueryRow(ctx, `SELECT id, slug, name, created_at FROM orgs ORDER BY created_at LIMIT 1`).
		Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Org{}, ErrNotFound
	}
	return o, err
}

type RegisterRunnerParams struct {
	OrgID       uuid.UUID
	Name        string
	Hostname    string
	OS          string
	Arch        string
	CPUs        int
	MemoryBytes int64
	DiskBytes   int64
	Labels      []string
	Version     string
	TokenHash   string
}

func (s *Store) RegisterRunner(ctx context.Context, p RegisterRunnerParams) (Runner, error) {
	var r Runner
	err := s.pool.QueryRow(ctx, `
		INSERT INTO runners (org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes,
		                     labels, version, status, token_hash, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'online', $11, now())
		ON CONFLICT (org_id, name) DO UPDATE SET
			hostname = EXCLUDED.hostname, os = EXCLUDED.os, arch = EXCLUDED.arch,
			cpus = EXCLUDED.cpus, memory_bytes = EXCLUDED.memory_bytes,
			disk_bytes = EXCLUDED.disk_bytes, labels = EXCLUDED.labels,
			version = EXCLUDED.version, status = 'online',
			token_hash = EXCLUDED.token_hash, last_heartbeat_at = now()
		RETURNING id, org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes, labels,
		          version, status, draining, ephemeral, last_heartbeat_at, created_at`,
		p.OrgID, p.Name, p.Hostname, p.OS, p.Arch, p.CPUs, p.MemoryBytes, p.DiskBytes,
		p.Labels, p.Version, p.TokenHash).
		Scan(&r.ID, &r.OrgID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.CPUs, &r.MemoryBytes,
			&r.DiskBytes, &r.Labels, &r.Version, &r.Status, &r.Draining, &r.Ephemeral,
			&r.LastHeartbeatAt, &r.CreatedAt)
	return r, err
}

func (s *Store) RunnerByToken(ctx context.Context, tokenHash string) (Runner, error) {
	var r Runner
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes, labels,
		       version, status, draining, ephemeral, last_heartbeat_at, created_at
		FROM runners WHERE token_hash = $1`, tokenHash).
		Scan(&r.ID, &r.OrgID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.CPUs, &r.MemoryBytes,
			&r.DiskBytes, &r.Labels, &r.Version, &r.Status, &r.Draining, &r.Ephemeral,
			&r.LastHeartbeatAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runner{}, ErrNotFound
	}
	return r, err
}

func (s *Store) Runners(ctx context.Context, orgID uuid.UUID) ([]Runner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes, labels,
		       version, status, draining, ephemeral, last_heartbeat_at, created_at
		FROM runners WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Runner{}
	for rows.Next() {
		var r Runner
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.CPUs,
			&r.MemoryBytes, &r.DiskBytes, &r.Labels, &r.Version, &r.Status, &r.Draining,
			&r.Ephemeral, &r.LastHeartbeatAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetRunnerDraining(ctx context.Context, orgID, id uuid.UUID, draining bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runners SET draining = $3,
			status = CASE WHEN $3 THEN 'draining' ELSE 'online' END
		WHERE org_id = $1 AND id = $2`, orgID, id, draining)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRunner(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM runners WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RunnerActiveJobs lists jobs currently leased to a runner.
func (s *Store) RunnerActiveJobs(ctx context.Context, runnerID uuid.UUID) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.name, j.status, j.conclusion, j.needs, j.runs_on,
		       j.image, j.matrix, j.env, j.if_condition, j.timeout_seconds, j.continue_on_error,
		       j.fail_fast, j.priority, j.attempt, j.runner_id, j.queued_at, j.started_at, j.completed_at,
		       j.lease_expires_at, j.error, j.outputs, j.outputs_spec
		FROM jobs j WHERE j.runner_id = $1 AND j.status = 'in_progress'`, runnerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
