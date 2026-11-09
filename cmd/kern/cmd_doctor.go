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
		c.doctorFleet(ctx, add)
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
func (c *cli) doctorFleet(ctx context.Context, add func(string, string, string)) {
	var me struct {
		User user `json:"user"`
	}
	if err := c.api().get(ctx, "/auth/me", nil, &me); err != nil {
		add("auth", "fail", "token rejected: "+err.Error())
		return
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
		return
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
