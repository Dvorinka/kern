package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
	"sort"
	"strings"

	"github.com/dvorinka/kern/internal/workflows"
)

// cmdOptimize scans the workflows under a directory and prints ranked
// suggestions for making CI faster and better fitted to this fleet — the
// agentic side of `kern lint`. Rules are conservative: only things the
// runner actually supports get suggested.
func (c *cli) cmdOptimize(args []string) error {
	if len(args) > 0 && args[0] == "--profile" {
		return c.cmdOptimizeProfile(context.Background(), args[1:])
	}
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	paths := findWorkflows(dir)
	if len(paths) == 0 {
		return fmt.Errorf("no workflows found under %s/.github/workflows", dir)
	}

	// fleet label inventory — a runs-on label no runner carries means the
	// job can never be claimed.
	fleet := map[string]bool{}
	var runnersResp struct {
		Runners []struct {
			Labels []string `json:"labels"`
			Status string   `json:"status"`
		} `json:"runners"`
	}
	if err := c.api().get(context.Background(), "/runners", nil, &runnersResp); err == nil {
		for _, r := range runnersResp.Runners {
			if r.Status != "online" {
				continue
			}
			for _, l := range r.Labels {
				fleet[l] = true
			}
		}
	}

	type suggestion struct {
		sev  string // high, med, low
		text string
	}
	var out []suggestion
	add := func(sev, format string, a ...any) {
		out = append(out, suggestion{sev, fmt.Sprintf(format, a...)})
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cc := workflows.Compiler{Resolve: makeResolver(dir)}
		res, err := workflows.Compile(string(content), cc)
		if err != nil || res.Plan == nil {
			continue
		}
		short := path[strings.LastIndex(path, "/")+1:]
		isPR := false
		for _, e := range res.Events {
			if e == "pull_request" {
				isPR = true
			}
		}
		for _, j := range res.Plan.Jobs {
			// unclaimable: every label must exist on some online runner
			if len(fleet) > 0 {
				for _, l := range j.RunsOn {
					if !fleet[l] && !strings.Contains(l, "${{") {
						add("high", "%s: job %q wants label %q — no online runner carries it (job will wait forever)", short, j.JobID, l)
					}
				}
			}
			// priority: PR pipelines benefit most from queue-jumping
			if isPR && j.Priority == 0 {
				add("med", "%s: job %q has no priority — `priority: 60` under runs-on jumps the shared queue (kern extension, GitHub ignores it)", short, j.JobID)
			}
			// split: long serial test lanes parallelize for free
			if j.SplitN == 0 && !j.SplitAuto && len(j.Steps) >= 4 {
				names := strings.ToLower(j.Name + " " + j.JobID)
				if strings.Contains(names, "test") || strings.Contains(names, "e2e") {
					add("med", "%s: job %q (%d steps) is a serial test lane — `strategy: {split: N}` shards it across runners via KERN_SHARD", short, j.JobID, len(j.Steps))
				}
			}
			// services without health checks race the first step
			for _, svc := range j.Services {
				hasHealth := false
				for _, o := range svc.Options {
					if strings.HasPrefix(o, "--health-") {
						hasHealth = true
						break
					}
				}
				if !hasHealth {
					add("low", "%s: job %q service %q has no --health-* options in services.options — steps may race the service's first connection", short, j.JobID, svc.Name)
				}
			}
		}
	}

	if len(out) == 0 {
		fmt.Println("no suggestions — the workflows already match the fleet well")
		return nil
	}
	sevOrder := map[string]int{"high": 0, "med": 1, "low": 2}
	sort.SliceStable(out, func(i, k int) bool { return sevOrder[out[i].sev] < sevOrder[out[k].sev] })
	for _, s := range out {
		fmt.Printf("[%s] %s\n", s.sev, s.text)
	}
	return nil
}

// cmdOptimizeProfile renders a run's critical-path waterfall and, with
// --flamegraph, a speedscope JSON the browser speedscope.app can open.
// Jobs carry queued_at/started_at/completed_at; needs edges rebuild the
// longest chain the same way the web gantt does.
func (c *cli) cmdOptimizeProfile(ctx context.Context, args []string) error {
	opts, pos := parseFlags(args, "flamegraph")
	if len(pos) == 0 {
		return fmt.Errorf("kern optimize --profile <run-id> [--flamegraph out.json]")
	}
	var out runDetail
	if err := c.api().get(ctx, "/runs/"+pos[0], nil, &out); err != nil {
		return err
	}
	jobs := out.Jobs
	if len(jobs) == 0 {
		return fmt.Errorf("run %s has no jobs", pos[0])
	}
	t0 := jobs[0].QueuedAt
	for _, j := range jobs {
		if j.QueuedAt != nil && (t0 == nil || j.QueuedAt.Before(*t0)) {
			t0 = j.QueuedAt
		}
	}
	if t0 == nil {
		return fmt.Errorf("run has no queued_at timestamps")
	}
	span := func(a, b *time.Time) time.Duration {
		if a == nil || b == nil {
			return 0
		}
		return b.Sub(*a)
	}
	fmt.Printf("%-28s %8s %8s %8s  %s\n", "job", "queue", "exec", "end", "note")
	depIdx := map[string]int{}
	for i, j := range jobs {
		depIdx[j.JobKey], depIdx[j.Name], depIdx[j.Key] = i, i, i
	}
	// longest needs-chain by finish time — the critical path.
	memo := map[int]struct {
		t time.Duration
		n []int
	}{}
	var chain func(i int) (time.Duration, []int)
	chain = func(i int) (time.Duration, []int) {
		if hit, ok := memo[i]; ok {
			return hit.t, hit.n
		}
		memo[i] = struct {
			t time.Duration
			n []int
		}{}
		var bestT time.Duration
		var bestN []int
		for _, k := range jobs[i].Needs {
			d, ok := depIdx[k]
			if !ok || d == i {
				continue
			}
			ct, cn := chain(d)
			if ct > bestT {
				bestT, bestN = ct, cn
			}
		}
		j := jobs[i]
		end := j.CompletedAt
		if end == nil {
			end = j.StartedAt
		}
		var dur time.Duration
		if j.QueuedAt != nil && end != nil {
			dur = end.Sub(*j.QueuedAt)
		}
		r := struct {
			t time.Duration
			n []int
		}{bestT + dur, append(append([]int{}, bestN...), i)}
		memo[i] = r
		return r.t, r.n
	}
	critSet := map[int]bool{}
	bi, bt := -1, time.Duration(0)
	for i := range jobs {
		if ct, _ := chain(i); ct > bt {
			bi, bt = i, ct
		}
	}
	if bi >= 0 {
		_, path := chain(bi)
		for _, i := range path {
			critSet[i] = true
		}
	}
	var totalWait, totalExec time.Duration
	for i, j := range jobs {
		wait := span(j.QueuedAt, j.StartedAt)
		exec := span(j.StartedAt, j.CompletedAt)
		totalWait += wait
		totalExec += exec
		end := "-"
		if j.CompletedAt != nil && j.QueuedAt != nil {
			end = fmtDur(j.CompletedAt.Sub(*t0))
		}
		note := ""
		if critSet[i] {
			note = "critical path"
		}
		if j.Conclusion == "failure" {
			note = strings.TrimSpace(note + " FAILED")
		}
		fmt.Printf("%-28s %8s %8s %8s  %s\n",
			trunc(j.Name, 28), fmtDur(wait), fmtDur(exec), end, note)
	}
	if len(critSet) > 0 {
		var names []string
		for i := range critSet {
			names = append(names, jobs[i].Name)
		}
		sort.Strings(names)
		fmt.Printf("critical path: %s\n", strings.Join(names, " → "))
	}
	if fg := opts["flamegraph"]; fg != "" {
		return writeSpeedscope(fg, out.Run.WorkflowName+" #"+itoa(out.Run.RunNumber), jobs, *t0)
	}
	return nil
}

// writeSpeedscope emits a speedscope "evented" profile: each job is a frame
// opened at queued_at and closed at completed_at, so queue wait shows inside
// the job's span.
func writeSpeedscope(path, name string, jobs []job, t0 time.Time) error {
	type frame struct{ Name string }
	frames := make([]frame, 0, len(jobs))
	type event struct {
		Type  string  `json:"type"`
		At    float64 `json:"at"`
		Frame int     `json:"frame"`
	}
	var evs []event
	end := 0.0
	for i, j := range jobs {
		frames = append(frames, frame{Name: j.Name + " (" + j.Status + ")"})
		if j.QueuedAt != nil {
			evs = append(evs, event{"O", j.QueuedAt.Sub(t0).Seconds(), i})
		}
		cl := j.CompletedAt
		if cl == nil {
			cl = j.StartedAt
		}
		if cl != nil {
			at := cl.Sub(t0).Seconds()
			evs = append(evs, event{"C", at, i})
			if at > end {
				end = at
			}
		}
	}
	profile := map[string]any{
		"$schema": "https://www.speedscope.app/file-format-schema.json",
		"shared":  map[string]any{"frames": frames},
		"profiles": []any{map[string]any{
			"type": "evented", "name": name, "unit": "seconds",
			"startValue": 0, "endValue": end, "events": evs,
		}},
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("flamegraph: %s (open in https://www.speedscope.app)\n", path)
	return nil
}

func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%.0fm%02ds", d.Minutes(), int(d.Seconds())%60)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }
