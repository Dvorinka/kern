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
	Ephemeral   bool
}

func (s *Store) RegisterRunner(ctx context.Context, p RegisterRunnerParams) (Runner, error) {
	return scanRunner(s.pool.QueryRow(ctx, `
		INSERT INTO runners (org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes,
		                     labels, version, status, ephemeral, token_hash, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'online', $11, $12, now())
		ON CONFLICT (org_id, name) DO UPDATE SET
			hostname = EXCLUDED.hostname, os = EXCLUDED.os, arch = EXCLUDED.arch,
			cpus = EXCLUDED.cpus, memory_bytes = EXCLUDED.memory_bytes,
			disk_bytes = EXCLUDED.disk_bytes, labels = EXCLUDED.labels,
			version = EXCLUDED.version, status = 'online',
			ephemeral = EXCLUDED.ephemeral,
			token_hash = EXCLUDED.token_hash, last_heartbeat_at = now()
		RETURNING `+runnerCols,
		p.OrgID, p.Name, p.Hostname, p.OS, p.Arch, p.CPUs, p.MemoryBytes, p.DiskBytes,
		p.Labels, p.Version, p.Ephemeral, p.TokenHash))
}

// InsertRunner creates a runner row without the (org, name) upsert used by
// registration. The JIT endpoint uses it so a name collision fails instead of
// silently rotating an existing runner's credential.
func (s *Store) InsertRunner(ctx context.Context, p RegisterRunnerParams) (Runner, error) {
	return scanRunner(s.pool.QueryRow(ctx, `
		INSERT INTO runners (org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes,
		                     labels, version, status, ephemeral, token_hash, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'online', $11, $12, now())
		RETURNING `+runnerCols,
		p.OrgID, p.Name, p.Hostname, p.OS, p.Arch, p.CPUs, p.MemoryBytes, p.DiskBytes,
		p.Labels, p.Version, p.Ephemeral, p.TokenHash))
}

// runnerCols is the full runners column list; keep scan order in lockstep.
const runnerCols = `id, org_id, name, hostname, os, arch, cpus, memory_bytes, disk_bytes, labels,
	version, status, draining, ephemeral, last_heartbeat_at, cpu_percent,
	mem_used_bytes, disk_free_bytes, metrics_at, geo_lat, geo_lng, geo_city,
	geo_country, geo_source, geo_ip, geo_updated_at, created_at`

func scanRunner(row pgx.Row) (Runner, error) {
	var r Runner
	err := row.Scan(&r.ID, &r.OrgID, &r.Name, &r.Hostname, &r.OS, &r.Arch, &r.CPUs,
		&r.MemoryBytes, &r.DiskBytes, &r.Labels, &r.Version, &r.Status, &r.Draining,
		&r.Ephemeral, &r.LastHeartbeatAt, &r.CPUPercent, &r.MemUsedBytes,
		&r.DiskFreeBytes, &r.MetricsAt, &r.GeoLat, &r.GeoLng, &r.GeoCity,
		&r.GeoCountry, &r.GeoSource, &r.GeoIP, &r.GeoUpdatedAt, &r.CreatedAt)
	return r, err
}

func (s *Store) RunnerByToken(ctx context.Context, tokenHash string) (Runner, error) {
	r, err := scanRunner(s.pool.QueryRow(ctx, `SELECT `+runnerCols+` FROM runners WHERE token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return Runner{}, ErrNotFound
	}
	return r, err
}

// FleetSlots sums CPUs of online, non-draining runners whose labels can run
// runsOn. A runner with cpus < 1 counts as one. Zero matching runners returns 0.
func (s *Store) FleetSlots(ctx context.Context, orgID uuid.UUID, runsOn []string) (int, error) {
	if runsOn == nil {
		runsOn = []string{}
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(GREATEST(cpus, 1)), 0)::int
		FROM runners
		WHERE org_id = $1
		  AND status = 'online'
		  AND NOT draining
		  AND $2::text[] <@ (labels || ARRAY['self-hosted', 'kern']::text[])`,
		orgID, runsOn).Scan(&n)
	return n, err
}

func (s *Store) Runners(ctx context.Context, orgID uuid.UUID) ([]Runner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runnerCols+` FROM runners WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Runner{}
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
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

// SetRunnerGeoManual applies an operator-placed location. It wins over
// auto-detection until the operator asks for auto again.
func (s *Store) SetRunnerGeoManual(ctx context.Context, orgID, id uuid.UUID, lat, lng float64, city, country string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runners SET geo_lat = $3, geo_lng = $4, geo_city = $5, geo_country = $6,
			geo_source = 'manual', geo_updated_at = now()
		WHERE org_id = $1 AND id = $2`, orgID, id, lat, lng, city, country)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRunnerGeoAuto writes an egress-IP lookup result. The WHERE clause keeps a
// concurrent manual override from being clobbered by a lookup already in flight.
func (s *Store) SetRunnerGeoAuto(ctx context.Context, id uuid.UUID, ip string, lat, lng float64, city, country string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runners SET geo_lat = $2, geo_lng = $3, geo_city = $4, geo_country = $5,
			geo_ip = $6, geo_source = 'auto', geo_updated_at = now()
		WHERE id = $1 AND geo_source <> 'manual'`, id, lat, lng, city, country, ip)
	return err
}

// MarkRunnerGeoAuto flips a manually placed runner back to auto mode; the next
// heartbeat (or an explicit re-resolve) rewrites the coordinates.
func (s *Store) MarkRunnerGeoAuto(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	var ip string
	err := s.pool.QueryRow(ctx, `
		UPDATE runners SET geo_source = 'auto', geo_updated_at = now()
		WHERE org_id = $1 AND id = $2 RETURNING geo_ip`, orgID, id).Scan(&ip)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return ip, err
}

// LiveJob is an in-flight job tied to the runner executing it — the fleet map
// draws one arc per live job from the control plane to that runner.
type LiveJob struct {
	RunnerID uuid.UUID `json:"runner_id"`
	Key      string    `json:"key"`
	Name     string    `json:"name"`
}

// InFlightJobs lists running jobs with their leased runner for one org.
func (s *Store) InFlightJobs(ctx context.Context, orgID uuid.UUID) ([]LiveJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.runner_id, j.job_key, j.name
		FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND j.status = 'in_progress' AND j.runner_id IS NOT NULL`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveJob{}
	for rows.Next() {
		var j LiveJob
		if err := rows.Scan(&j.RunnerID, &j.Key, &j.Name); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RunnerActiveJobs lists jobs currently leased to a runner.
func (s *Store) RunnerActiveJobs(ctx context.Context, runnerID uuid.UUID) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.key, j.name, j.status, j.conclusion, j.needs, j.runs_on,
		       j.image, j.matrix, j.env, j.if_condition, j.timeout_seconds, j.continue_on_error,
		       j.fail_fast, j.priority, j.attempt, j.runner_id, j.queued_at, j.started_at, j.completed_at,
		       j.lease_expires_at, j.error, j.outputs, j.outputs_spec, j.environment, j.environment_url,
		       j.inputs, j.services, j.permissions
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
