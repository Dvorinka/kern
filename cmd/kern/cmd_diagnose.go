package main

// kern diagnose <run-id> — a pasteable failure bundle: run metadata, every
// job's runner identity, step diagnoses and the tail of each failed step's
// log. Built entirely from the public /api/v1 surface; no server changes.

import (
	"context"
	"encoding/json"
	"bytes"
	"fmt"
	"io"
	"os"
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
	opts, pos := parseFlagsEx(args, []string{"i", "interactive"}, "tail", "job")
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

	// One endpoint carries the whole bundle — the old path fanned out to
	// 2+2N sequential calls (#376).
	var resp struct {
		Run     run `json:"run"`
		Jobs    []struct {
			Job   job        `json:"job"`
			Steps []diagStep `json:"steps"`
		} `json:"jobs"`
		Runners map[string]runnerRow `json:"runners"`
	}
	q := url.Values{}
	q.Set("tail", fmt.Sprintf("%d", tail))
	if err := c.api().get(ctx, "/runs/"+id+"/diagnose", q, &resp); err != nil {
		return err
	}

	bundle := diagnoseBundle{
		Server: c.server, At: time.Now().UTC().Format(time.RFC3339),
		Run: resp.Run, Jobs: []diagJob{},
	}
	for _, j := range resp.Jobs {
		if opts["job"] != "" && j.Job.Key != opts["job"] && j.Job.JobKey != opts["job"] && j.Job.Name != opts["job"] {
			continue
		}
		dj := diagJob{Job: j.Job}
		if r, ok := resp.Runners[j.Job.RunnerID]; ok && j.Job.RunnerID != "" {
			dj.Runner = &r
		}
		dj.Steps = j.Steps
		bundle.Jobs = append(bundle.Jobs, dj)
	}

	if c.json {
		printJSON(bundle)
		return nil
	}
	if opts["i"] == "true" || opts["interactive"] == "true" {
		var buf bytes.Buffer
		writeDiagnose(&buf, bundle, tail)
		return runDiagnosePager(strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"))
	}
	writeDiagnose(os.Stdout, bundle, tail)
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

func writeDiagnose(w io.Writer, b diagnoseBundle, tail int64) {
	f := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	r := b.Run
	f("kern diagnose — run %s (#%d) %s %s/%s\n", shortID(r.ID), r.RunNumber,
		r.WorkflowName, r.Status, r.Conclusion)
	f("server:   %s   at: %s\n", b.Server, b.At)
	f("project:  %s   event: %s   ref: %s   sha: %s\n",
		r.ProjectSlug, r.Event, r.Ref, short(r.SHA, 12))
	f("actor:    %s   trust: %s   created: %s\n",
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
		f("%s\n", line)
		if j.Error != "" {
			f("   error: %s\n", j.Error)
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
			f("   %s step %-3d %-32s %s%s\n", mark, st.Number, st.Name, sconcl, extra)
			if len(st.Diagnosis) > 0 {
				var d struct {
					Kind       string `json:"kind"`
					Summary    string `json:"summary"`
					Suggestion string `json:"suggestion"`
				}
				if json.Unmarshal(st.Diagnosis, &d) == nil && d.Kind != "" {
					f("       diagnosis: %s — %s\n", d.Kind, d.Summary)
					if d.Suggestion != "" {
						f("       fix:        %s\n", d.Suggestion)
					}
				}
			}
			if st.LogTail != "" {
				shown := min(tail, st.LogSize)
				f("   --- log tail (last %s of %s) ---\n", humanBytes(shown), humanBytes(st.LogSize))
				f("%s", st.LogTail)
				if !strings.HasSuffix(st.LogTail, "\n") {
					f("\n")
				}
			}
		}
	}
}
