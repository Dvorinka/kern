package main

// kern diagnose <run-id> — a pasteable failure bundle: run metadata, every
// job's runner identity, step diagnoses and the tail of each failed step's
// log. Built entirely from the public /api/v1 surface; no server changes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const diagnoseTailDefault = 8 << 10 // bytes of log kept per failed step

type diagStep struct {
	step
	LogTail string `json:"log_tail,omitempty"`
	LogSize int64  `json:"log_size,omitempty"`
}

type diagJob struct {
	Job    job        `json:"job"`
	Runner *runnerRow `json:"runner,omitempty"`
	Steps  []diagStep `json:"steps"`
}

type diagnoseBundle struct {
	Server string    `json:"server"`
	At     string    `json:"at"`
	Run    run       `json:"run"`
	Jobs   []diagJob `json:"jobs"`
}

func (c *cli) cmdDiagnose(ctx context.Context, args []string) error {
	opts, pos := parseFlags(args, "tail", "job")
	if err := c.requireAuth(); err != nil {
		return err
	}
	id, err := strarg(pos, 0, "run id")
	if err != nil {
		return err
	}
	tail := int64(diagnoseTailDefault)
	if v := opts["tail"]; v != "" {
		tail = int64(atoi(v))
		if tail <= 0 {
			return fmt.Errorf("--tail must be a positive byte count")
		}
	}

	var detail runDetail
	if err := c.api().get(ctx, "/runs/"+id, nil, &detail); err != nil {
		return err
	}
	var runners struct {
		Runners []runnerRow `json:"runners"`
	}
	_ = c.api().get(ctx, "/runners", nil, &runners) // identity is nice-to-have
	byID := map[string]runnerRow{}
	for _, r := range runners.Runners {
		byID[r.ID] = r
	}

	bundle := diagnoseBundle{
		Server: c.server, At: time.Now().UTC().Format(time.RFC3339),
		Run: detail.Run, Jobs: []diagJob{},
	}
	for _, j := range detail.Jobs {
		if opts["job"] != "" && j.Key != opts["job"] && j.JobKey != opts["job"] && j.Name != opts["job"] {
			continue
		}
		dj := diagJob{Job: j}
		if r, ok := byID[j.RunnerID]; ok {
			dj.Runner = &r
		}
		for _, st := range j.Steps {
			ds := diagStep{step: st}
			if needsLogTail(st) {
				text, size, err := c.logTail(ctx, st.ID, tail)
				if err == nil {
					ds.LogTail, ds.LogSize = text, size
				}
			}
			dj.Steps = append(dj.Steps, ds)
		}
		bundle.Jobs = append(bundle.Jobs, dj)
	}

	if c.json {
		printJSON(bundle)
		return nil
	}
	printDiagnose(bundle, tail)
	return nil
}

// needsLogTail selects the steps worth tailing: terminal failures plus
// anything carrying a diagnosis. Success and skipped steps have nothing to
// diagnose.
func needsLogTail(st step) bool {
	switch st.Conclusion {
	case "failure", "cancelled", "timed_out":
		return true
	}
	return len(st.Diagnosis) > 0 && string(st.Diagnosis) != "null"
}

// logTail returns up to tail bytes from the end of a step's log plus the
// full size, so the reader knows what was cut.
func (c *cli) logTail(ctx context.Context, stepID string, tail int64) (string, int64, error) {
	var probe stepLog
	if err := c.api().get(ctx, "/steps/"+stepID+"/logs",
		url.Values{"offset": {"0"}, "limit": {"1"}}, &probe); err != nil {
		return "", 0, err
	}
	offset := int64(0)
	if probe.Size > tail {
		offset = probe.Size - tail
	}
	var chunk stepLog
	if err := c.api().get(ctx, "/steps/"+stepID+"/logs",
		url.Values{"offset": {fmt.Sprint(offset)}, "limit": {fmt.Sprint(tail)}}, &chunk); err != nil {
		return "", probe.Size, err
	}
	return chunk.Text, probe.Size, nil
}

func printDiagnose(b diagnoseBundle, tail int64) {
	r := b.Run
	fmt.Printf("kern diagnose — run %s (#%d) %s %s/%s\n", shortID(r.ID), r.RunNumber,
		r.WorkflowName, r.Status, r.Conclusion)
	fmt.Printf("server:   %s   at: %s\n", b.Server, b.At)
	fmt.Printf("project:  %s   event: %s   ref: %s   sha: %s\n",
		r.ProjectSlug, r.Event, r.Ref, short(r.SHA, 12))
	fmt.Printf("actor:    %s   trust: %s   created: %s\n",
		r.Actor, r.TrustLevel, r.CreatedAt.Format(time.RFC3339))
	for _, dj := range b.Jobs {
		j := dj.Job
		concl := j.Conclusion
		if concl == "" {
			concl = j.Status
		}
		line := fmt.Sprintf("\n== job %q (%s) — %s", j.Name, j.JobKey, concl)
		if dj.Runner != nil {
			rn := dj.Runner
			line += fmt.Sprintf("  @%s (%s %s %s v%s)", rn.Name, rn.OS, rn.Arch, rn.Hostname, rn.Version)
		}
		if j.Attempt > 1 {
			line += fmt.Sprintf("  attempt %d", j.Attempt)
		}
		fmt.Println(line)
		if j.Error != "" {
			fmt.Printf("   error: %s\n", j.Error)
		}
		for _, st := range dj.Steps {
			sconcl := st.Conclusion
			if sconcl == "" {
				sconcl = st.Status
			}
			mark := " "
			switch sconcl {
			case "success":
				mark = "✓"
			case "failure", "timed_out":
				mark = "✗"
			case "cancelled":
				mark = "!"
			case "skipped":
				mark = "-"
			}
			extra := ""
			if st.ExitCode != nil {
				extra = fmt.Sprintf(" exit=%d", *st.ExitCode)
			}
			if st.LogBytes > 0 {
				extra += " log=" + humanBytes(st.LogBytes)
			}
			fmt.Printf("   %s step %-3d %-32s %s%s\n", mark, st.Number, st.Name, sconcl, extra)
			if len(st.Diagnosis) > 0 {
				var d struct {
					Kind       string `json:"kind"`
					Summary    string `json:"summary"`
					Suggestion string `json:"suggestion"`
				}
				if json.Unmarshal(st.Diagnosis, &d) == nil && d.Kind != "" {
					fmt.Printf("       diagnosis: %s — %s\n", d.Kind, d.Summary)
					if d.Suggestion != "" {
						fmt.Printf("       fix:        %s\n", d.Suggestion)
					}
				}
			}
			if st.LogTail != "" {
				fmt.Printf("   --- log tail (last %s of %s) ---\n", humanBytes(tail), humanBytes(st.LogSize))
				fmt.Print(st.LogTail)
				if !strings.HasSuffix(st.LogTail, "\n") {
					fmt.Println()
				}
			}
		}
	}
}
