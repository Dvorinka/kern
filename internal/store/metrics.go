package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Overview computes dashboard aggregates from live data only, scoped to one
// org — cross-tenant counts must never leak into another org's dashboard.
func (s *Store) Overview(ctx context.Context, orgID uuid.UUID) (Overview, error) {
	var o Overview
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM workflow_runs r JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1),
			(SELECT count(*) FROM workflow_runs r JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1 AND r.conclusion = 'success'),
			(SELECT count(*) FROM workflow_runs r JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1 AND r.conclusion = 'failure'),
			(SELECT count(*) FROM workflow_runs r JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1 AND r.status IN ('awaiting_approval', 'queued', 'in_progress')),
			(SELECT count(*) FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1 AND j.status = 'queued'),
			(SELECT count(*) FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1 AND j.status = 'in_progress'),
			(SELECT count(*) FROM runners WHERE org_id = $1 AND status = 'online'),
			(SELECT count(*) FROM runners WHERE org_id = $1),
			COALESCE((SELECT avg(GREATEST(0, extract(epoch FROM j.started_at - j.queued_at)))
				FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN projects p ON p.id = r.project_id
				WHERE p.org_id = $1 AND j.status = 'completed' AND j.started_at IS NOT NULL
				AND j.queued_at > now() - interval '24 hours'), 0),
			COALESCE((SELECT avg(extract(epoch FROM r.completed_at - r.started_at))
				FROM workflow_runs r JOIN projects p ON p.id = r.project_id
				WHERE p.org_id = $1 AND r.completed_at IS NOT NULL AND r.started_at IS NOT NULL), 0),
			COALESCE((SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY GREATEST(0, extract(epoch FROM j.started_at - j.queued_at)))
				FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN projects p ON p.id = r.project_id
				WHERE p.org_id = $1 AND j.status = 'completed' AND j.started_at IS NOT NULL
				AND j.queued_at > now() - interval '24 hours'), 0),
			(SELECT max(r.created_at) FROM workflow_runs r JOIN projects p ON p.id = r.project_id WHERE p.org_id = $1)`,
		orgID).
		Scan(&o.RunsTotal, &o.RunsSuccess, &o.RunsFailure, &o.RunsActive, &o.JobsQueued,
			&o.JobsRunning, &o.RunnersOnline, &o.RunnersTotal, &o.AvgQueueSeconds,
			&o.AvgRunSeconds, &o.P95QueueSeconds, &o.LastRunAt)
	return o, err
}

// CacheStats reports cache hit information once cache actions exist.
func (s *Store) CacheStats(ctx context.Context, orgID uuid.UUID) (entries int64, bytes int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(c.size_bytes), 0) FROM cache_entries c
		JOIN projects p ON p.id = c.project_id WHERE p.org_id = $1`, orgID).Scan(&entries, &bytes)
	return entries, bytes, err
}

// DurationHistogram is a fixed-bucket histogram over seconds, computed at
// scrape time so it survives restarts and needs no new tables. Counts are
// non-cumulative: Counts[i] holds (Buckets[i-1], Buckets[i]] and the last
// entry is the +Inf bucket.
type DurationHistogram struct {
	Buckets []float64
	Counts  []uint64
	Sum     float64
	Count   uint64
}

var (
	// Queue wait: claimable → claimed. Most jobs land under a minute; the
	// tail matters for backlog evidence.
	queueWaitEdges = []float64{1, 5, 15, 60, 300, 900, 3600}
	// Step duration: checkout to teardown. Wide buckets — steps range from
	// milliseconds to hour-long builds.
	stepDurationEdges = []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 7200}
)

// QueueWaitHistogram buckets job queue waits over the last 24h. started_at is
// set at claim time, so in-progress jobs already contribute their final wait.
func (s *Store) QueueWaitHistogram(ctx context.Context, orgID uuid.UUID) (DurationHistogram, error) {
	return s.durationHistogram(ctx, `
		SELECT GREATEST(0, extract(epoch FROM j.started_at - j.queued_at))
		FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND j.started_at IS NOT NULL AND j.queued_at > now() - interval '24 hours'`,
		queueWaitEdges, orgID)
}

// StepDurationHistogram buckets completed step durations over the last 24h.
func (s *Store) StepDurationHistogram(ctx context.Context, orgID uuid.UUID) (DurationHistogram, error) {
	return s.durationHistogram(ctx, `
		SELECT extract(epoch FROM st.completed_at - st.started_at)
		FROM steps st
		JOIN jobs j ON j.id = st.job_id
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND st.started_at IS NOT NULL AND st.completed_at IS NOT NULL
		  AND st.completed_at > now() - interval '24 hours'`,
		stepDurationEdges, orgID)
}

// durationHistogram runs rowsSQL (a single numeric-seconds column) and folds
// it into per-bucket counts. Edges are compile-time constants — safe to
// interpolate. rowsSQL must reference $1 as the org filter.
func (s *Store) durationHistogram(ctx context.Context, rowsSQL string, edges []float64, args ...any) (DurationHistogram, error) {
	var b strings.Builder
	b.WriteString("SELECT ")
	for i, e := range edges {
		if i > 0 {
			b.WriteString(", ")
		}
		if i == 0 {
			fmt.Fprintf(&b, "count(*) FILTER (WHERE w <= %g)", e)
		} else {
			fmt.Fprintf(&b, "count(*) FILTER (WHERE w > %g AND w <= %g)", edges[i-1], e)
		}
	}
	fmt.Fprintf(&b, ", count(*) FILTER (WHERE w > %g), COALESCE(sum(w), 0), count(*) FROM (%s) t(w)",
		edges[len(edges)-1], rowsSQL)

	out := DurationHistogram{Buckets: edges, Counts: make([]uint64, len(edges)+1)}
	row := s.pool.QueryRow(ctx, b.String(), args...)
	dest := make([]any, 0, len(edges)+3)
	for range edges {
		dest = append(dest, &out.Counts[len(dest)])
	}
	var sum float64
	dest = append(dest, &out.Counts[len(edges)], &sum, &out.Count)
	if err := row.Scan(dest...); err != nil {
		return out, err
	}
	out.Sum = sum
	return out, nil
}

// RunnerUtilizationRow is one runner's latest host metrics plus its live job
// count — the per-runner series behind the fleet gauges.
type RunnerUtilizationRow struct {
	OrgSlug      string
	Name         string
	CPUPercent   *float64
	MemUsedBytes *int64
	DiskFree     *int64
	ActiveJobs   int64
}

// RunnerUtilization lists every runner with its heartbeat metrics and live
// job count. Runners that never reported metrics still appear (ActiveJobs
// only) so fleet size is visible before the first heartbeat lands.
func (s *Store) RunnerUtilization(ctx context.Context, orgID uuid.UUID) ([]RunnerUtilizationRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.slug, r.name, r.cpu_percent, r.mem_used_bytes, r.disk_free_bytes,
		       (SELECT count(*) FROM jobs j WHERE j.runner_id = r.id AND j.status = 'in_progress')
		FROM runners r JOIN orgs o ON o.id = r.org_id
		WHERE r.org_id = $1
		ORDER BY o.slug, r.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunnerUtilizationRow{}
	for rows.Next() {
		var r RunnerUtilizationRow
		if err := rows.Scan(&r.OrgSlug, &r.Name, &r.CPUPercent, &r.MemUsedBytes, &r.DiskFree, &r.ActiveJobs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentFailures lists failing runs for the overview panel.
func (s *Store) RecentFailures(ctx context.Context, orgID uuid.UUID, limit int) ([]Run, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, runSelect+`
		WHERE r.conclusion = 'failure' AND p.org_id = $2
		ORDER BY r.created_at DESC
		LIMIT $1`, limit, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueueSeries returns per-minute run counts for the last hour.
func (s *Store) QueueSeries(ctx context.Context, orgID uuid.UUID) ([]TimeSeriesPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc('minute', r.created_at) AS bucket, count(*)
		FROM workflow_runs r
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND r.created_at > now() - interval '1 hour'
		GROUP BY bucket ORDER BY bucket`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimeSeriesPoint{}
	for rows.Next() {
		var p TimeSeriesPoint
		if err := rows.Scan(&p.Time, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type TimeSeriesPoint struct {
	Time  time.Time `json:"time"`
	Value int64     `json:"value"`
}
