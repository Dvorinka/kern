package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// cmdTop is a dependency-free fleet dashboard: alternate screen, polls
// overview + runners + recent runs every 2s, Ctrl-C exits (#376).
func (c *cli) cmdTop(ctx context.Context, args []string) error {
	opts, _ := parseFlags(args, "interval")
	interval := 2 * time.Second
	if v := opts["interval"]; v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer fmt.Print("\x1b[?25h\x1b[?1049l")
	for {
		var ov struct {
			Overview struct {
				RunsActive     int     `json:"runs_active"`
				JobsQueued     int     `json:"jobs_queued"`
				JobsRunning    int     `json:"jobs_running"`
				RunnersOnline  int     `json:"runners_online"`
				RunnersTotal   int     `json:"runners_total"`
				AvgQueueSecs   float64 `json:"avg_queue_seconds"`
				AvgRunSecs     float64 `json:"avg_run_seconds"`
			} `json:"overview"`
		}
		var runnersResp struct {
			Runners []runnerRow `json:"runners"`
		}
		var runsResp struct {
			Runs []run `json:"runs"`
		}
		errOv := c.api().get(ctx, "/overview", nil, &ov)
		errR := c.api().get(ctx, "/runners", nil, &runnersResp)
		errS := c.api().get(ctx, "/runs?limit=8", nil, &runsResp)
		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		fmt.Fprintf(&b, "kern top — %s   (ctrl-c to quit)\n\n", time.Now().Format("15:04:05"))
		if errOv != nil {
			fmt.Fprintf(&b, "overview: %v\n", errOv)
		} else {
			o := ov.Overview
			fmt.Fprintf(&b, "runs active %d   jobs %d running / %d queued   avg wait %.0fs   avg run %.0fs\n",
				o.RunsActive, o.JobsRunning, o.JobsQueued, o.AvgQueueSecs, o.AvgRunSecs)
		}
		if errR == nil {
			fmt.Fprintf(&b, "runners online %d/%d\n\n", ov.Overview.RunnersOnline, ov.Overview.RunnersTotal)
			for _, r := range runnersResp.Runners {
				cpu := ""
				if r.CPUPercent != nil {
					cpu = fmt.Sprintf(" cpu %.0f%%", *r.CPUPercent)
				}
				drain := ""
				if r.Draining {
					drain = " draining"
				}
				fmt.Fprintf(&b, "  %-22s %-8s jobs %-2d%s%s\n", r.Name, r.Status, r.ActiveJobs, cpu, drain)
			}
		}
		if errS == nil {
			b.WriteString("\nrecent runs\n")
			for _, r := range runsResp.Runs {
				status := r.Status
				if r.Conclusion != "" {
					status += "/" + r.Conclusion
				}
				fmt.Fprintf(&b, "  #%-4d %-24s %-22s %s\n", r.RunNumber, trunc(r.ProjectSlug+"/"+r.WorkflowName, 24), status, r.Actor)
			}
		}
		fmt.Print(b.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
