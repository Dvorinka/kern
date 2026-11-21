package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
)

// runsWatch consumes GET /api/v1/runs/:id/stream — the multiplexed run SSE
// (#376): job/step transitions and interleaved log chunks on one connection,
// rendered as colored [job/step] lines instead of per-step polls.
func (c *cli) runsWatch(ctx context.Context, args []string) error {
	id, err := strarg(args, 0, "run id")
	if err != nil {
		return err
	}
	resolved, err := c.resolveRunID(ctx, id)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.server+"/api/v1/runs/"+resolved+"/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream %s: %s", resolved, resp.Status)
	}

	jobName := map[string]string{}
	stepName := map[string]string{}
	jobColor := map[string]string{}
	colors := []string{"36", "35", "33", "32", "34", "31"}
	colorFor := func(job string) string {
		if col, ok := jobColor[job]; ok {
			return col
		}
		col := colors[len(jobColor)%len(colors)]
		jobColor[job] = col
		return col
	}
	prefix := func(job, stepID, stepNum string, num int) string {
		name := jobName[job]
		if name == "" {
			name = shortID(job)
		}
		step := stepName[stepID]
		if step == "" && num > 0 {
			step = fmt.Sprintf("step %d", num)
		}
		return fmt.Sprintf("\x1b[%sm[%s/%s]\x1b[0m", colorFor(job), name, step)
	}
	var conclusion string
	err = readSSE(resp, func(event string, data []byte) {
		switch event {
		case "job":
			var p struct {
				JobID      string `json:"job_id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}
			if json.Unmarshal(data, &p) == nil {
				jobName[p.JobID] = p.Name
				label := p.Status
				if p.Conclusion != "" {
					label = p.Conclusion
				}
				if p.Status != "queued" {
					fmt.Printf("\x1b[%sm[%s]\x1b[0m %s\n", colorFor(p.JobID), p.Name, label)
				}
			}
		case "step":
			var p struct {
				JobID      string `json:"job_id"`
				StepID     string `json:"step_id"`
				Number     int    `json:"number"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}
			if json.Unmarshal(data, &p) == nil {
				stepName[p.StepID] = p.Name
				if p.Status == "completed" || p.Conclusion != "" {
					label := p.Conclusion
					if label == "" {
						label = p.Status
					}
					fmt.Printf("%s %s\n", prefix(p.JobID, p.StepID, "", p.Number), label)
				}
			}
		case "log":
			var p struct {
				JobID string `json:"job_id"`
				Step  int    `json:"step"`
				StepID string `json:"step_id"`
				Text  string `json:"text"`
			}
			if json.Unmarshal(data, &p) == nil {
				pre := prefix(p.JobID, p.StepID, "", p.Step)
				for _, line := range strings.Split(strings.TrimRight(p.Text, "\n"), "\n") {
					fmt.Printf("%s %s\n", pre, line)
				}
			}
		case "end":
			var p struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}
			if json.Unmarshal(data, &p) == nil {
				conclusion = p.Conclusion
				if conclusion == "" {
					conclusion = p.Status
				}
			}
		case "error":
			var p struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(data, &p) == nil {
				fmt.Fprintln(os.Stderr, "stream error:", p.Message)
			}
		}
	})
	if err != nil {
		return err
	}
	if conclusion != "" {
		fmt.Println("run", conclusion)
	}
	return nil
}

// readSSE drives a Server-Sent-Events stream, calling onEvent per complete
// event block. Lines longer than 64KiB appear in log chunks — the scanner
// buffer is sized accordingly.
func readSSE(resp *http.Response, onEvent func(event string, data []byte)) error {
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var event string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, line[6:]...)
		case line == "":
			if event != "" && len(data) > 0 {
				onEvent(event, data)
			}
			event, data = "", nil
		}
	}
	return sc.Err()
}

// resolveRunID maps a full or short run id to the canonical UUID through
// the runs list — the same prefix resolution the API applies.
func (c *cli) resolveRunID(ctx context.Context, arg string) (string, error) {
	if _, err := uuid.Parse(arg); err == nil {
		return arg, nil
	}
	var out struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := c.api().get(ctx, "/runs", nil, &out); err != nil {
		return "", err
	}
	var match string
	for _, r := range out.Runs {
		if strings.HasPrefix(r.ID, arg) {
			if match != "" {
				return "", fmt.Errorf("run id prefix %q matches more than one run", arg)
			}
			match = r.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("no run matching %q", arg)
	}
	return match, nil
}
