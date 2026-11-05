package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Usage aggregates what the fleet consumed and produced over a trailing
// window. All durations are seconds; "billable minutes" mirrors GitHub's
// per-job round-up billing so the hosted-cost comparison stays honest.
type UsageTotals struct {
	RunnerSeconds      float64 `json:"runner_seconds"`
	BillableMinutes    int64   `json:"billable_minutes"`
	Jobs               int64   `json:"jobs"`
	Runs               int64   `json:"runs"`
	RunsSuccess        int64   `json:"runs_success"`
	QueueMedianSeconds float64 `json:"queue_median_seconds"`
	JobMedianSeconds   float64 `json:"job_median_seconds"`
}

type UsageDay struct {
	Date            time.Time `json:"date"`
	Seconds         float64   `json:"seconds"`
	BillableMinutes int64     `json:"billable_minutes"`
	Jobs            int64     `json:"jobs"`
	FailedJobs      int64     `json:"failed_jobs"`
}

type UsageProject struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Runs    int64   `json:"runs"`
}

type UsageRunner struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Jobs    int64   `json:"jobs"`
}

// FailureCause counts failed steps by diagnosis kind over the window — the
// "which workflow line keeps breaking" answer.
type FailureCause struct {
	Kind    string `json:"kind"`
	Count   int64  `json:"count"`
	Fixable int64  `json:"fixable"`
}

type UsageReport struct {
	Totals   UsageTotals    `json:"totals"`
	Days     []UsageDay     `json:"days"`
	Projects []UsageProject `json:"projects"`
	Runners  []UsageRunner  `json:"runners"`
	Failures []FailureCause `json:"failures"`
}

// usageFrom/usageWhere bound the aggregation to whole jobs that finished
// inside the trailing N days, scoped to the org through the owning project.
// Extra joins (e.g. runners) slot between the two fragments.
const usageFrom = `
	FROM jobs j
	JOIN workflow_runs r ON r.id = j.run_id
	JOIN projects p ON p.id = r.project_id`

const usageWhere = `
	WHERE p.org_id = $1
	  AND j.started_at IS NOT NULL
	  AND j.completed_at IS NOT NULL
	  AND j.status = 'completed'
	  AND j.started_at >= now() - make_interval(days => $2)`

func (s *Store) Usage(ctx context.Context, orgID uuid.UUID, days int) (*UsageReport, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	rep := &UsageReport{}

	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       coalesce(sum(extract(epoch from j.completed_at - j.started_at)), 0),
		       coalesce(sum(ceil(extract(epoch from j.completed_at - j.started_at) / 60.0)), 0),
		       coalesce(percentile_cont(0.5) within group (order by extract(epoch from j.started_at - j.queued_at)), 0),
		       coalesce(percentile_cont(0.5) within group (order by extract(epoch from j.completed_at - j.started_at)), 0)
		`+usageFrom+usageWhere, orgID, days).Scan(
		&rep.Totals.Jobs, &rep.Totals.RunnerSeconds, &rep.Totals.BillableMinutes,
		&rep.Totals.QueueMedianSeconds, &rep.Totals.JobMedianSeconds)
	if err != nil {
		return nil, fmt.Errorf("usage totals: %w", err)
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) filter (where r.conclusion = 'success')
		FROM workflow_runs r JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND r.status = 'completed'
		  AND r.created_at >= now() - make_interval(days => $2)`,
		orgID, days).Scan(&rep.Totals.Runs, &rep.Totals.RunsSuccess); err != nil {
		return nil, fmt.Errorf("usage runs: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc('day', j.started_at)::date,
		       sum(extract(epoch from j.completed_at - j.started_at)),
		       sum(ceil(extract(epoch from j.completed_at - j.started_at) / 60.0)),
		       count(*),
		       count(*) filter (where j.conclusion = 'failure')
		`+usageFrom+usageWhere+` GROUP BY 1 ORDER BY 1`, orgID, days)
	if err != nil {
		return nil, fmt.Errorf("usage days: %w", err)
	}
	for rows.Next() {
		var d UsageDay
		if err := rows.Scan(&d.Date, &d.Seconds, &d.BillableMinutes, &d.Jobs, &d.FailedJobs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("usage day scan: %w", err)
		}
		rep.Days = append(rep.Days, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := s.pool.Query(ctx, `
		SELECT p.slug, p.name,
		       sum(extract(epoch from j.completed_at - j.started_at)),
		       count(DISTINCT j.run_id)
		`+usageFrom+usageWhere+` GROUP BY p.slug, p.name ORDER BY 3 DESC`, orgID, days)
	if err != nil {
		return nil, fmt.Errorf("usage projects: %w", err)
	}
	for prows.Next() {
		var u UsageProject
		if err := prows.Scan(&u.Slug, &u.Name, &u.Seconds, &u.Runs); err != nil {
			prows.Close()
			return nil, fmt.Errorf("usage project scan: %w", err)
		}
		rep.Projects = append(rep.Projects, u)
	}
	prows.Close()

	rrows, err := s.pool.Query(ctx, `
		SELECT coalesce(rn.name, 'unassigned'),
		       sum(extract(epoch from j.completed_at - j.started_at)),
		       count(*)
		`+usageFrom+` LEFT JOIN runners rn ON rn.id = j.runner_id`+usageWhere+`
		GROUP BY 1 ORDER BY 2 DESC`, orgID, days)
	if err != nil {
		return nil, fmt.Errorf("usage runners: %w", err)
	}
	for rrows.Next() {
		var u UsageRunner
		if err := rrows.Scan(&u.Name, &u.Seconds, &u.Jobs); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("usage runner scan: %w", err)
		}
		rep.Runners = append(rep.Runners, u)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}

	frows, err := s.pool.Query(ctx, `
		SELECT coalesce(s.diagnosis->>'kind', 'unclassified'),
		       count(*),
		       count(*) filter (where s.diagnosis->>'fixable' = 'true')
		FROM steps s
		JOIN jobs j ON j.id = s.job_id
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1
		  AND s.diagnosis IS NOT NULL
		  AND s.completed_at >= now() - make_interval(days => $2)
		GROUP BY 1 ORDER BY 2 DESC`, orgID, days)
	if err != nil {
		return nil, fmt.Errorf("usage failures: %w", err)
	}
	for frows.Next() {
		var f FailureCause
		if err := frows.Scan(&f.Kind, &f.Count, &f.Fixable); err != nil {
			frows.Close()
			return nil, fmt.Errorf("usage failure scan: %w", err)
		}
		rep.Failures = append(rep.Failures, f)
	}
	frows.Close()
	return rep, frows.Err()
}
