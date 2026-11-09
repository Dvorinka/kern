package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dvorinka/kern/internal/workflows"
)

// cmdOptimize scans the workflows under a directory and prints ranked
// suggestions for making CI faster and better fitted to this fleet — the
// agentic side of `kern lint`. Rules are conservative: only things the
// runner actually supports get suggested.
func (c *cli) cmdOptimize(args []string) error {
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
