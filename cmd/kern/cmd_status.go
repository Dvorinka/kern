package main

import (
	"context"
	"fmt"
	"time"
)

// cmdStatus prints the control-plane dashboard: api meta, queue depth, and
// runner counts. --watch refreshes every few seconds until Ctrl-C.
func (c *cli) cmdStatus(ctx context.Context, args []string) error {
	watch := false
	for _, a := range args {
		switch a {
		case "--watch", "-w":
			watch = true
		default:
			return fmt.Errorf("usage: kern status [--watch]")
		}
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	for {
		if err := c.printStatus(ctx); err != nil {
			return err
		}
		if !watch {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *cli) printStatus(ctx context.Context) error {
	var meta struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Env     string `json:"env"`
	}
	if err := c.api().get(ctx, "/meta", nil, &meta); err != nil {
		return err
	}
	var ov struct {
		Overview struct {
			RunsTotal     int64      `json:"runs_total"`
			RunsSuccess   int64      `json:"runs_success"`
			RunsFailure   int64      `json:"runs_failure"`
			RunsActive    int64      `json:"runs_active"`
			JobsQueued    int64      `json:"jobs_queued"`
			JobsRunning   int64      `json:"jobs_running"`
			RunnersOnline int64      `json:"runners_online"`
			RunnersTotal  int64      `json:"runners_total"`
			LastRunAt     *time.Time `json:"last_run_at"`
		} `json:"overview"`
	}
	if err := c.api().get(ctx, "/overview", nil, &ov); err != nil {
		return err
	}
	var fleet struct {
		Runners []runnerRow `json:"runners"`
	}
	if err := c.api().get(ctx, "/runners", nil, &fleet); err != nil {
		return err
	}
	o := ov.Overview
	online, draining, offline := 0, 0, 0
	var lastBeat *time.Time
	for _, r := range fleet.Runners {
		switch {
		case r.Draining:
			draining++
		case r.Status == "online":
			online++
		default:
			offline++
		}
		if r.LastHeartbeatAt != nil && (lastBeat == nil || r.LastHeartbeatAt.After(*lastBeat)) {
			lastBeat = r.LastHeartbeatAt
		}
	}
	if c.json {
		printJSON(map[string]any{
			"server": c.server, "version": meta.Version, "env": meta.Env,
			"runs_active": o.RunsActive, "jobs_queued": o.JobsQueued,
			"jobs_running": o.JobsRunning, "runners_online": online,
			"runners_draining": draining, "runners_offline": offline,
			"runners_total": o.RunnersTotal, "last_heartbeat": lastBeat,
			"last_run": o.LastRunAt,
		})
		return nil
	}
	fmt.Printf("%s %s · %s · %s\n", meta.Name, meta.Version, meta.Env, c.server)
	fmt.Printf("runs:    %d active · %d total (%d ok, %d failed)\n", o.RunsActive, o.RunsTotal, o.RunsSuccess, o.RunsFailure)
	fmt.Printf("queue:   %d queued · %d running\n", o.JobsQueued, o.JobsRunning)
	fmt.Printf("runners: %d online · %d draining · %d offline (of %d) · last beat %s\n",
		online, draining, offline, o.RunnersTotal, rel(lastBeat))
	fmt.Printf("last run: %s\n", rel(o.LastRunAt))
	return nil
}
