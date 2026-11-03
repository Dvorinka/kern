package orchestrator

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

// MintScheduledRuns creates runs for cron boundaries crossed since the last
// tick. There is no catch-up: a long-down control plane only fires schedules
// due within the last minute, matching GitHub's "missed schedules are skipped".
func (o *Orchestrator) MintScheduledRuns(ctx context.Context, now time.Time) {
	since := o.lastScheduleCheck
	if since.IsZero() || now.Sub(since) > 10*time.Minute {
		since = now.Add(-time.Minute)
	}
	o.lastScheduleCheck = now

	items, err := o.Store.ScheduledWorkflows(ctx)
	if err != nil {
		o.Log.Error("list scheduled workflows", zap.Error(err))
		return
	}
	for _, it := range items {
		for _, expr := range it.Schedules {
			next, err := workflows.NextCron(expr, since)
			if err != nil {
				o.Log.Warn("invalid cron expression",
					zap.String("workflow_id", it.WorkflowID.String()),
					zap.String("cron", expr), zap.Error(err))
				continue
			}
			if next.After(now) {
				continue
			}
			if exists, err := o.Store.HasScheduledRun(ctx, it.VersionID, next); err != nil || exists {
				if err != nil {
					o.Log.Error("schedule dedupe check", zap.Error(err))
				}
				continue
			}
			p := it.Project
			run, err := o.StartRun(ctx, store.CreateRunParams{
				ProjectID:         p.ID,
				WorkflowID:        it.WorkflowID,
				WorkflowVersionID: it.VersionID,
				Event:             "schedule",
				Ref:               "refs/heads/" + p.DefaultBranch,
				SHA:               it.CommitSHA,
				Actor:             "kern-scheduler",
				TrustLevel:        "trusted",
				Source: map[string]any{
					"repo_url":       p.RepoURL,
					"repo_full_name": p.RepoFullName,
					"local_path":     p.LocalPath,
					"default_branch": p.DefaultBranch,
				},
			})
			if err != nil {
				o.Log.Error("mint scheduled run",
					zap.String("workflow_id", it.WorkflowID.String()), zap.Error(err))
				continue
			}
			o.Log.Info("scheduled run minted",
				zap.String("run_id", run.ID.String()),
				zap.String("workflow_id", it.WorkflowID.String()),
				zap.String("cron", expr), zap.Time("instant", next))
		}
	}
}
