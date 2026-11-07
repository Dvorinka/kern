package store

import (
	"context"
	"time"
)

// Overview computes dashboard aggregates from live data only.
func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM workflow_runs),
			(SELECT count(*) FROM workflow_runs WHERE conclusion = 'success'),
			(SELECT count(*) FROM workflow_runs WHERE conclusion = 'failure'),
			(SELECT count(*) FROM workflow_runs WHERE status IN ('awaiting_approval', 'queued', 'in_progress')),
			(SELECT count(*) FROM jobs WHERE status = 'queued'),
			(SELECT count(*) FROM jobs WHERE status = 'in_progress'),
			(SELECT count(*) FROM runners WHERE status = 'online'),
			(SELECT count(*) FROM runners),
			COALESCE((SELECT avg(GREATEST(0, extract(epoch FROM started_at - queued_at)))
				FROM jobs WHERE status = 'completed' AND started_at IS NOT NULL
				AND queued_at > now() - interval '24 hours'), 0),
			COALESCE((SELECT avg(extract(epoch FROM completed_at - started_at))
				FROM workflow_runs WHERE completed_at IS NOT NULL AND started_at IS NOT NULL), 0),
			COALESCE((SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY GREATEST(0, extract(epoch FROM started_at - queued_at)))
				FROM jobs WHERE status = 'completed' AND started_at IS NOT NULL
				AND queued_at > now() - interval '24 hours'), 0),
			(SELECT max(created_at) FROM workflow_runs)`).
		Scan(&o.RunsTotal, &o.RunsSuccess, &o.RunsFailure, &o.RunsActive, &o.JobsQueued,
			&o.JobsRunning, &o.RunnersOnline, &o.RunnersTotal, &o.AvgQueueSeconds,
			&o.AvgRunSeconds, &o.P95QueueSeconds, &o.LastRunAt)
	return o, err
}

// CacheStats reports cache hit information once cache actions exist.
func (s *Store) CacheStats(ctx context.Context) (entries int64, bytes int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(size_bytes), 0) FROM cache_entries`).Scan(&entries, &bytes)
	return entries, bytes, err
}

// RecentFailures lists failing runs for the overview panel.
func (s *Store) RecentFailures(ctx context.Context, limit int) ([]Run, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, runSelect+`
		WHERE r.conclusion = 'failure'
		ORDER BY r.created_at DESC
		LIMIT $1`, limit)
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
func (s *Store) QueueSeries(ctx context.Context) ([]TimeSeriesPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc('minute', created_at) AS bucket, count(*)
		FROM workflow_runs
		WHERE created_at > now() - interval '1 hour'
		GROUP BY bucket ORDER BY bucket`)
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
