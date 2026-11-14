package main

// kern doctor — one-shot health check of the control plane and the runner
// fleet. Public probes (healthz/readyz/meta) run without a token; fleet checks
// need one with read scope. Exit code is 1 when any check fails.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail,omitempty"`
}

type doctorReport struct {
	Server string        `json:"server"`
	Checks []doctorCheck `json:"checks"`
}

func (c *cli) cmdDoctor(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: kern doctor")
	}
	if err := c.resolve(); err != nil {
		return err
	}
	rep := doctorReport{Server: c.server}
	add := func(name, status, detail string) {
		rep.Checks = append(rep.Checks, doctorCheck{Name: name, Status: status, Detail: detail})
	}

	// Public probes — reachable before auth is even considered.
	root := strings.TrimRight(c.server, "/")
	hc := &http.Client{Timeout: 5 * time.Second}
	for _, p := range []struct{ name, path string }{
		{"api", "/healthz"}, {"database", "/readyz"},
	} {
		if err := probeGET(ctx, hc, root+p.path); err != nil {
			add(p.name, "fail", err.Error())
		} else {
			add(p.name, "ok", p.path)
		}
	}

	var meta struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Env     string `json:"env"`
	}
	if err := c.api().get(ctx, "/meta", nil, &meta); err != nil {
		add("version", "fail", err.Error())
	} else {
		add("version", "ok", fmt.Sprintf("%s %s (%s)", meta.Name, meta.Version, meta.Env))
		warnVersionSkew(meta.Version)
	}

	if c.token == "" {
		add("auth", "warn", "no API token — fleet checks skipped (kern login)")
	} else {
		runners := c.doctorFleet(ctx, add)
		c.doctorLint(ctx, add, runners)
	}

	if c.json {
		printJSON(rep)
	} else {
		for _, ch := range rep.Checks {
			line := fmt.Sprintf("%-4s %-10s %s", ch.Status, ch.Name, ch.Detail)
			fmt.Println(strings.TrimRight(line, " "))
		}
	}
	fails := 0
	for _, ch := range rep.Checks {
		if ch.Status == "fail" {
			fails++
		}
	}
	if fails > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", fails)
	}
	return nil
}

// doctorFleet runs the authenticated half: identity, queue pressure, webhook
// failures and per-runner liveness.
func (c *cli) doctorFleet(ctx context.Context, add func(string, string, string)) []runnerRow {
	var me struct {
		User user `json:"user"`
	}
	if err := c.api().get(ctx, "/auth/me", nil, &me); err != nil {
		add("auth", "fail", "token rejected: "+err.Error())
		return nil
	}
	add("auth", "ok", fmt.Sprintf("%s (%s)", me.User.Email, me.User.Role))

	var ov struct {
		Overview struct {
			JobsQueued      int64   `json:"jobs_queued"`
			JobsRunning     int64   `json:"jobs_running"`
			RunnersOnline   int64   `json:"runners_online"`
			RunnersTotal    int64   `json:"runners_total"`
			P95QueueSeconds float64 `json:"p95_queue_seconds"`
		} `json:"overview"`
		Webhooks struct {
			Failed24h int64  `json:"failed_24h"`
			LastError string `json:"last_error"`
		} `json:"webhooks"`
	}
	if err := c.api().get(ctx, "/overview", nil, &ov); err != nil {
		add("overview", "fail", err.Error())
	} else {
		o := ov.Overview
		switch {
		case o.RunnersTotal == 0:
			add("runners", "warn", "no runners enrolled")
		case o.RunnersOnline == 0 && o.JobsQueued > 0:
			add("queue", "fail", fmt.Sprintf("%d job(s) queued, no runners online", o.JobsQueued))
		case o.RunnersOnline < o.RunnersTotal:
			add("runners", "warn", fmt.Sprintf("%d/%d runners online", o.RunnersOnline, o.RunnersTotal))
		default:
			add("runners", "ok", fmt.Sprintf("%d/%d online, %d queued, %d running",
				o.RunnersOnline, o.RunnersTotal, o.JobsQueued, o.JobsRunning))
		}
		if o.P95QueueSeconds > 300 {
			add("queue", "warn", fmt.Sprintf("p95 queue wait %.0fs over 24h", o.P95QueueSeconds))
		} else if o.JobsQueued == 0 {
			add("queue", "ok", "queue empty")
		}
		if ov.Webhooks.Failed24h > 0 {
			detail := fmt.Sprintf("%d webhook deliveries failed in 24h", ov.Webhooks.Failed24h)
			if ov.Webhooks.LastError != "" {
				detail += ": " + ov.Webhooks.LastError
			}
			add("webhooks", "warn", detail)
		} else {
			add("webhooks", "ok", "no failures in 24h")
		}
	}

	var runners struct {
		Runners []runnerRow `json:"runners"`
	}
	if err := c.api().get(ctx, "/runners", nil, &runners); err != nil {
		add("fleet", "warn", "runner list unavailable: "+err.Error())
		return nil
	}
	for _, r := range runners.Runners {
		if r.Draining {
			continue // intentional
		}
		if r.Status != "online" {
			d := fmt.Sprintf("runner %s is %s", r.Name, r.Status)
			if r.LastHeartbeatAt != nil {
				d += " (last heartbeat " + rel(r.LastHeartbeatAt) + ")"
			}
			add("runner "+r.Name, "warn", d)
			continue
		}
		if r.ClaimsPaused != "" {
			add("runner "+r.Name, "warn", "claims paused: "+r.ClaimsPaused)
		}
		if len(r.MissingTools) > 0 {
			add("runner "+r.Name, "warn", "toolset drift: missing "+strings.Join(r.MissingTools, ", "))
		}
		// Recommended minimums: a docker runner carries job images, the
		// persistent _cache tree, toolcache installs and per-job workspaces;
		// below these floors the claim gates flap and GC churn costs more
		// than it saves. 40 GiB total / 10 GiB free / 4 GiB RAM is the
		// smallest host that stays healthy under a 2-job concurrency.
		if r.DiskBytes > 0 && r.DiskBytes < recommendedRunnerDiskBytes {
			add("runner "+r.Name, "warn", fmt.Sprintf(
				"disk %s total below recommended %s minimum for a docker runner",
				humanBytes(r.DiskBytes), humanBytes(recommendedRunnerDiskBytes)))
		}
		if r.DiskFreeBytes != nil && *r.DiskFreeBytes < recommendedRunnerFreeBytes {
			add("runner "+r.Name, "warn", fmt.Sprintf(
				"only %s disk free — recommend %s+ so reclaim sweeps do not churn",
				humanBytes(*r.DiskFreeBytes), humanBytes(recommendedRunnerFreeBytes)))
		}
		if r.MemoryBytes > 0 && r.MemoryBytes < recommendedRunnerMemBytes {
			add("runner "+r.Name, "warn", fmt.Sprintf(
				"memory %s below recommended %s minimum; job containers need ~2 GiB each",
				humanBytes(r.MemoryBytes), humanBytes(recommendedRunnerMemBytes)))
		}
	}
	return runners.Runners
}

const (
	recommendedRunnerDiskBytes = 40 << 30
	recommendedRunnerFreeBytes = 10 << 30
	recommendedRunnerMemBytes  = 4 << 30
)

// riskyEnvKeys lists runner env overrides that rewrite fundamentals every
// job inherits — the workflow never asked for them and the contract keys
// (GITHUB_*/RUNNER_*) already win over overrides.
func riskyEnvKeys(env map[string]string) []string {
	risky := map[string]bool{
		"PATH": true, "HOME": true, "DOCKER_HOST": true,
		"HTTP_PROXY": true, "http_proxy": true, "HTTPS_PROXY": true, "https_proxy": true,
		"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "NODE_PATH": true,
	}
	var keys []string
	for k := range env {
		if risky[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// doctorLint covers project/fleet hygiene the API can see: risky runner env
// overrides, project workflow health, and failure classifications that point
// at broken runner state (empty toolcache is the demo case).
func (c *cli) doctorLint(ctx context.Context, add func(string, string, string), runners []runnerRow) {
	for _, r := range runners {
		if keys := riskyEnvKeys(r.Env); len(keys) > 0 {
			add("env "+r.Name, "warn",
				fmt.Sprintf("env override shadows %s — merges into every job", strings.Join(keys, ", ")))
		}
	}

	// Project workflow lint, when a project is configured.
	if c.project != "" {
		var wfs struct {
			Workflows []struct {
				Name        string          `json:"name"`
				Path        string          `json:"path"`
				Disabled    bool            `json:"disabled"`
				Diagnostics json.RawMessage `json:"diagnostics"`
			} `json:"workflows"`
		}
		if err := c.api().get(ctx, "/projects/"+c.project+"/workflows", nil, &wfs); err != nil {
			add("workflows", "warn", "unavailable: "+err.Error())
		} else {
			disabled, diags := 0, 0
			var diagNames []string
			for _, w := range wfs.Workflows {
				if w.Disabled {
					disabled++
				}
				var d []any
				if len(w.Diagnostics) > 0 && json.Unmarshal(w.Diagnostics, &d) == nil && len(d) > 0 {
					diags += len(d)
					if len(diagNames) < 3 {
						diagNames = append(diagNames, w.Name)
					}
				}
			}
			switch {
			case diags > 0:
				add("workflows", "warn", fmt.Sprintf("%d compile diagnostic(s) across %s — kern lint shows detail",
					diags, strings.Join(diagNames, ", ")))
			case disabled > 0:
				add("workflows", "warn", fmt.Sprintf("%d of %d workflows disabled — dispatch and webhooks refuse them",
					disabled, len(wfs.Workflows)))
			default:
				add("workflows", "ok", fmt.Sprintf("%d synced, no diagnostics", len(wfs.Workflows)))
			}
		}
	}

	// missing_binary is the signature of a toolcache dir that lost its
	// contents (interrupted fetch, GC mishap — the setup action reports
	// success, the binary probe then fails).
	var runs struct {
		Total int `json:"total"`
	}
	day := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if err := c.api().get(ctx, "/runs?classification=missing_binary&limit=1&day="+day, nil, &runs); err == nil {
		if runs.Total > 0 {
			add("toolcache", "warn", fmt.Sprintf(
				"%d run(s) hit missing_binary in 24h — a toolcache install may be empty; kern runs list --classification missing_binary",
				runs.Total))
		} else {
			add("toolcache", "ok", "no missing_binary jobs in 24h")
		}
	}
}

// probeGET is a bare liveness probe for the unauthenticated endpoints.
func probeGET(ctx context.Context, hc *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s → %d: %s", url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var body struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &body) == nil && body.Status != "" && body.Status != "ok" && body.Status != "ready" {
		return fmt.Errorf("%s reports %q", url, body.Status)
	}
	return nil
}
