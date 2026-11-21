package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// resolveRunner accepts a runner id, id prefix, or name and returns the row.
func (c *cli) resolveRunner(ctx context.Context, arg string) (runnerRow, error) {
	var out struct {
		Runners []runnerRow `json:"runners"`
	}
	if err := c.api().get(ctx, "/runners", nil, &out); err != nil {
		return runnerRow{}, err
	}
	var match *runnerRow
	for i, r := range out.Runners {
		if r.ID == arg || r.Name == arg {
			return r, nil
		}
		if len(arg) >= 4 && strings.HasPrefix(r.ID, arg) {
			if match != nil {
				return runnerRow{}, fmt.Errorf("id prefix %q matches more than one runner", arg)
			}
			match = &out.Runners[i]
		}
	}
	if match != nil {
		return *match, nil
	}
	return runnerRow{}, fmt.Errorf("no runner named or matching %q", arg)
}

// selectRunners resolves batch targets (#376): positional runner names/ids,
// or selector flags — --all, --label L, --outdated (below the fleet's newest
// version), --offline (no heartbeat in the online window). Selectors may
// combine; with none and no positionals it errors rather than acting on an
// implicit fleet-wide set.
func (c *cli) selectRunners(ctx context.Context, args []string) ([]runnerRow, map[string]string, error) {
	opts, pos := parseFlagsEx(args, []string{"all", "outdated", "offline", "dry-run"}, "label")
	var out struct {
		Runners []runnerRow `json:"runners"`
	}
	if err := c.api().get(ctx, "/runners", nil, &out); err != nil {
		return nil, nil, err
	}
	if len(pos) > 0 {
		var sel []runnerRow
		for _, a := range pos {
			var match *runnerRow
			for i, r := range out.Runners {
				if r.ID == a || r.Name == a {
					sel = append(sel, r)
					match = nil
					break
				}
				if len(a) >= 4 && strings.HasPrefix(r.ID, a) {
					if match != nil {
						return nil, nil, fmt.Errorf("id prefix %q matches more than one runner", a)
					}
					match = &out.Runners[i]
				}
			}
			if match == nil {
				return nil, nil, fmt.Errorf("no runner named or matching %q", a)
			}
			sel = append(sel, *match)
		}
		return sel, opts, nil
	}
	var sel []runnerRow
	newest := fleetNewestVersion(out.Runners)
	online := func(r runnerRow) bool {
		return r.LastHeartbeatAt != nil && time.Since(*r.LastHeartbeatAt) < 45*time.Second
	}
	labeled := func(r runnerRow) bool {
		for _, l := range r.Labels {
			if l == opts["label"] {
				return true
			}
		}
		return false
	}
	for _, r := range out.Runners {
		if opts["all"] != "true" {
			if opts["label"] != "" && !labeled(r) {
				continue
			}
			if opts["outdated"] == "true" && (r.Version == "" || r.Version == newest) {
				continue
			}
			if opts["offline"] == "true" && online(r) {
				continue
			}
			if opts["label"] == "" && opts["outdated"] != "true" && opts["offline"] != "true" {
				continue // no selector matched this runner — skip
			}
		}
		sel = append(sel, r)
	}
	if len(sel) == 0 && opts["all"] != "true" && opts["label"] == "" && opts["outdated"] != "true" && opts["offline"] != "true" {
		return nil, nil, fmt.Errorf("give runner names/ids or a selector (--all, --label, --outdated, --offline)")
	}
	if len(sel) == 0 {
		return nil, nil, fmt.Errorf("no runners match the selector")
	}
	return sel, opts, nil
}

func (c *cli) cmdRunners(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern runners list|drain|undrain|self-update|wake|rollout|install|delete|enroll|env|jit")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		var out struct {
			Runners []runnerRow `json:"runners"`
		}
		if err := c.api().get(ctx, "/runners", nil, &out); err != nil {
			return err
		}
		c.orJSON(out.Runners, func() {
			rows := make([][]string, 0, len(out.Runners))
			for _, r := range out.Runners {
				cpu, mem, disk := "-", "-", "-"
				if r.CPUPercent != nil {
					cpu = fmt.Sprintf("%.0f%%", *r.CPUPercent)
				}
				if r.MemUsedBytes != nil && r.MemoryBytes > 0 {
					mem = fmt.Sprintf("%d%%", *r.MemUsedBytes*100/r.MemoryBytes)
				}
				if r.DiskFreeBytes != nil {
					disk = humanBytes(*r.DiskFreeBytes) + " free"
				}
				state := r.Status
				if r.Draining {
					state = "draining"
				}
				version := r.Version
				if version == "" {
					version = "-"
				}
				rows = append(rows, []string{shortID(r.ID), r.Name, state, version,
					fmt.Sprintf("%d", r.ActiveJobs), cpu, mem, disk, rel(r.LastHeartbeatAt)})
			}
			table([]string{"ID", "NAME", "STATE", "VERSION", "JOBS", "CPU", "MEM", "DISK", "BEAT"}, rows)
			if drift := versionDrift(out.Runners); drift != "" {
				fmt.Println(drift)
			}
			for _, r := range out.Runners {
				if len(r.MissingTools) > 0 {
					fmt.Printf("%s toolset drift: missing %s\n", r.Name, strings.Join(r.MissingTools, ", "))
				}
			}
		})
	case "drain", "undrain", "delete":
		runners, opts, err := c.selectRunners(ctx, rest)
		if err != nil {
			return err
		}
		for _, r := range runners {
			if opts["dry-run"] == "true" {
				fmt.Println(r.Name, "would", sub)
				continue
			}
			var err error
			if sub == "delete" {
				err = c.api().del(ctx, "/runners/"+r.ID)
			} else {
				err = c.api().post(ctx, "/runners/"+r.ID+"/drain",
					map[string]any{"draining": sub == "drain"}, nil)
			}
			if err != nil {
				return err
			}
			fmt.Println(r.Name, sub+"ed")
		}
	case "self-update", "wake":
		// Legacy form `<runner> on|off` still works; batch form takes
		// --on/--off plus targets or selectors (#376).
		bools := []string{"on", "off", "all", "outdated", "offline", "dry-run"}
		raw, pos := parseFlagsEx(rest, bools, "label")
		on := ""
		if len(pos) > 0 && (pos[len(pos)-1] == "on" || pos[len(pos)-1] == "off") {
			on = pos[len(pos)-1]
			pos = pos[:len(pos)-1]
		}
		if raw["on"] == "true" {
			on = "on"
		}
		if raw["off"] == "true" {
			on = "off"
		}
		if on == "" {
			return fmt.Errorf("%s needs on|off (or --on/--off)", sub)
		}
		targets := append([]string{}, pos...)
		if raw["label"] != "" {
			targets = append(targets, "--label", raw["label"])
		}
		for _, b := range []string{"all", "outdated", "offline", "dry-run"} {
			if raw[b] == "true" {
				targets = append(targets, "--"+b)
			}
		}
		runners, opts, err := c.selectRunners(ctx, targets)
		if err != nil {
			return err
		}
		for _, r := range runners {
			if opts["dry-run"] == "true" {
				fmt.Println(r.Name, "would", sub, on)
				continue
			}
			var err error
			if sub == "wake" {
				err = c.api().patch(ctx, "/runners/"+r.ID,
					map[string]any{"wake_on_demand": on == "on"}, nil)
			} else {
				err = c.api().post(ctx, "/runners/"+r.ID+"/auto-update",
					map[string]any{"enabled": on == "on"}, nil)
			}
			if err != nil {
				return err
			}
			fmt.Printf("%s %s %s\n", r.Name, map[string]string{"wake": "wake_on_demand", "self-update": "self-update"}[sub], on)
		}
	case "rollout":
		return c.runnerRollout(ctx, rest)
	case "env":
		return c.runnerEnv(ctx, rest)
	case "jit":
		return c.runnerJIT(ctx, rest)
	case "install":
		return c.runnerInstall(ctx, rest)
	case "enroll":
		opts, _ := parseFlags(rest, "name", "ttl")
		var out struct {
			Token  string `json:"token"`
			Record struct {
				ID        string `json:"id"`
				ExpiresAt string `json:"expires_at"`
			} `json:"record"`
		}
		err := c.api().post(ctx, "/enroll-tokens", map[string]any{
			"name": opts["name"], "ttl_minutes": atoi(opts["ttl"]),
		}, &out)
		if err != nil {
			return err
		}
		if c.json {
			printJSON(out)
			return nil
		}
		fmt.Printf("enrollment token (shown once, expires %s):\n%s\n\n", out.Record.ExpiresAt, out.Token)
		fmt.Printf("on the runner host:\n  kern-runner join --control %s --token %s\n\n", c.server, out.Token)
		fmt.Printf("or one container, everything included:\n  docker run -d --name kern-runner --restart always \\\n    -e KERN_CONTROL_URL=%s -e KERN_RUNNER_JOIN_TOKEN=%s \\\n    -e KERN_RUNNER_UPDATE=1 -e KERN_RUNNER_CONCURRENCY=1 \\\n    -e KERN_WORK_ROOT=/opt/kern/work -e HOME=/root \\\n    -v /var/run/docker.sock:/var/run/docker.sock -v /opt/kern/work:/opt/kern/work \\\n    kern-runner:local\n", c.server, out.Token)
	default:
		return fmt.Errorf("unknown runners subcommand %q", sub)
	}
	return nil
}

// runnerEnv manages the operator env-override map on a runner:
//
//	kern runners env <runner>                 list overrides
//	kern runners env <runner> set K=V [K=V…]  merge overrides
//	kern runners env <runner> unset K [K…]    remove keys
//
// PATCH replaces the whole map, so set/unset read-modify-write against the
// current row (#376).
func (c *cli) runnerEnv(ctx context.Context, args []string) error {
	arg, err := strarg(args, 0, "runner id|name")
	if err != nil {
		return err
	}
	r, err := c.resolveRunner(ctx, arg)
	if err != nil {
		return err
	}
	op := "get"
	if len(args) > 1 {
		op = args[1]
	}
	env := r.Env
	if env == nil {
		env = map[string]string{}
	}
	switch op {
	case "get", "list":
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: kern runners env <runner> set KEY=VALUE…")
		}
		for _, kv := range args[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("expected KEY=VALUE, got %q", kv)
			}
			env[k] = v
		}
	case "unset":
		if len(args) < 3 {
			return fmt.Errorf("usage: kern runners env <runner> unset KEY…")
		}
		for _, k := range args[2:] {
			delete(env, k)
		}
	default:
		return fmt.Errorf("env takes get|set|unset, got %q", op)
	}
	if op == "set" || op == "unset" {
		if err := c.api().patch(ctx, "/runners/"+r.ID,
			map[string]any{"env": env}, nil); err != nil {
			return err
		}
		fmt.Println(r.Name, "env updated")
	}
	if c.json {
		printJSON(env)
		return nil
	}
	if len(env) == 0 {
		fmt.Println(r.Name, "has no env overrides")
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%s\n", k, env[k])
	}
	return nil
}

// runnerRollout does a sequential rolling upgrade (#376): per runner —
// drain, wait for active jobs to empty, enable auto-update, wait for the
// reported version to move, undrain. Sequential keeps one runner's worth of
// capacity out at a time; --wait bounds each phase (default 10m).
func (c *cli) runnerRollout(ctx context.Context, args []string) error {
	opts, pos := parseFlags(args, "wait")
	wait := time.Duration(atoi(opts["wait"])) * time.Minute
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	runners, dry, err := c.selectRunners(ctx, pos)
	if err != nil {
		return err
	}
	fresh := func(id string) (runnerRow, error) {
		var out struct {
			Runners []runnerRow `json:"runners"`
		}
		if err := c.api().get(ctx, "/runners", nil, &out); err != nil {
			return runnerRow{}, err
		}
		for _, r := range out.Runners {
			if r.ID == id {
				return r, nil
			}
		}
		return runnerRow{}, fmt.Errorf("runner %s disappeared", shortID(id))
	}
	poll := func(id string, what string, ok func(runnerRow) bool) error {
		deadline := time.Now().Add(wait)
		for {
			r, err := fresh(id)
			if err != nil {
				return err
			}
			if ok(r) {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for %s", what)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}
	for _, r := range runners {
		if dry["dry-run"] == "true" {
			fmt.Println(r.Name, "would roll")
			continue
		}
		fmt.Printf("%s: draining\n", r.Name)
		if err := c.api().post(ctx, "/runners/"+r.ID+"/drain",
			map[string]any{"draining": true}, nil); err != nil {
			return err
		}
		if err := poll(r.ID, "active jobs to finish", func(x runnerRow) bool {
			return x.ActiveJobs == 0
		}); err != nil {
			return err
		}
		want := r.Version
		if err := c.api().post(ctx, "/runners/"+r.ID+"/auto-update",
			map[string]any{"enabled": true}, nil); err != nil {
			return err
		}
		verr := poll(r.ID, "version bump", func(x runnerRow) bool {
			return x.Version != "" && x.Version != want
		})
		if uerr := c.api().post(ctx, "/runners/"+r.ID+"/drain",
			map[string]any{"draining": false}, nil); uerr != nil {
			return uerr
		}
		if verr != nil {
			fmt.Printf("%s: update not observed within %s — undrained\n", r.Name, wait)
			continue
		}
		now, _ := fresh(r.ID)
		fmt.Printf("%s: %s -> %s\n", r.Name, want, now.Version)
	}
	return nil
}

// runnerJIT mints an ephemeral JIT runner credential for external scalers —
// POST /api/v1/runners (handlers_runners.go jitRunner). The plaintext token
// is shown once (#376).
func (c *cli) runnerJIT(ctx context.Context, args []string) error {
	opts, _ := parseFlags(args, "name", "labels")
	var labels []string
	for _, l := range strings.Split(opts["labels"], ",") {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	var out struct {
		RunnerID   string `json:"runner_id"`
		Name       string `json:"name"`
		Token      string `json:"token"`
		ControlURL string `json:"control_url"`
	}
	if err := c.api().post(ctx, "/runners", map[string]any{
		"name": opts["name"], "labels": labels,
	}, &out); err != nil {
		return err
	}
	if c.json {
		printJSON(out)
		return nil
	}
	fmt.Printf("jit runner %s (%s) — token shown once:\n%s\n\n", out.Name, shortID(out.RunnerID), out.Token)
	fmt.Printf("kern-runner --control %s --token %s\n", out.ControlURL, out.Token)
	return nil
}

// runnerInstall turns the local host into a runner: mint an enroll token,
// run `kern-runner join` (register → env file → systemd user unit), then
// enable and start the unit. Linux only — systemd is the contract.
func (c *cli) runnerInstall(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("runners install writes a systemd unit (linux only) — on Windows register a scheduled task per docs/porting.md, or use the docker one-liner from `kern runners enroll`")
	}
	opts, pos := parseFlags(args, "name", "labels", "ttl")
	name := opts["name"]
	if name == "" && len(pos) > 0 {
		name = pos[0]
	}
	if name == "" {
		host, _ := os.Hostname()
		name = host
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.api().post(ctx, "/enroll-tokens", map[string]any{
		"name": name, "ttl_minutes": atoi(opts["ttl"]),
	}, &out); err != nil {
		return err
	}
	exe, err := exec.LookPath("kern-runner")
	if err != nil {
		return fmt.Errorf("kern-runner not on PATH — install it from the release tarball (kern-<tag>-linux-%s.tar.gz) or use the docker one-liner from `kern runners enroll`", runtime.GOARCH)
	}
	join := []string{"join", "--control", c.server, "--token", out.Token, "--name", name}
	if v := opts["labels"]; v != "" {
		join = append(join, "--labels", v)
	}
	cmd := exec.Command(exe, join...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kern-runner join: %w", err)
	}
	for _, a := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "kern-runner"},
	} {
		sc := exec.Command("systemctl", a...)
		sc.Stdout, sc.Stderr = os.Stdout, os.Stderr
		if err := sc.Run(); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(a, " "), err)
		}
	}
	// Without lingering, a user unit dies with the login session.
	if err := exec.Command("loginctl", "enable-linger").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "kern: loginctl enable-linger failed — the unit stops at logout")
	}
	fmt.Println("runner installed and started")
	return nil
}

func (c *cli) cmdArtifacts(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern artifacts list|download|delete")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		opts, _ := parseFlags(args[1:], "limit", "run")
		q := url.Values{}
		if v := opts["limit"]; v != "" {
			q.Set("limit", v)
		}
		// A project-scoped token is refused unless it names the run.
		if v := opts["run"]; v != "" {
			q.Set("run_id", v)
		}
		var out struct {
			Artifacts []artifact `json:"artifacts"`
		}
		if err := c.api().get(ctx, "/artifacts", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Artifacts, func() {
			rows := make([][]string, 0, len(out.Artifacts))
			for _, a := range out.Artifacts {
				rows = append(rows, []string{shortID(a.ID), a.Name, a.ProjectSlug,
					fmt.Sprintf("%d", a.RunNumber), humanBytes(a.SizeBytes), relT(a.CreatedAt)})
			}
			table([]string{"ID", "NAME", "PROJECT", "RUN#", "SIZE", "AGE"}, rows)
		})
	case "download":
		opts, pos := parseFlags(args[1:], "out")
		id, err := strarg(pos, 0, "artifact id")
		if err != nil {
			return err
		}
		name := opts["out"]
		if name == "" {
			name = id + ".tar.gz"
		}
		fh, err := os.Create(name)
		if err != nil {
			return err
		}
		defer fh.Close()
		if err := c.api().download(ctx, "/artifacts/"+id+"/download", fh); err != nil {
			return err
		}
		fmt.Println("saved to", name)
	case "delete":
		id, err := strarg(args, 1, "artifact id")
		if err != nil {
			return err
		}
		if err := c.api().del(ctx, "/artifacts/"+id); err != nil {
			return err
		}
		fmt.Println("artifact deleted")
	default:
		return fmt.Errorf("unknown artifacts subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdCache(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern cache list|delete")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		opts, _ := parseFlags(args[1:], "limit")
		q := url.Values{}
		if v := opts["limit"]; v != "" {
			q.Set("limit", v)
		}
		var out struct {
			Entries []cacheEntry `json:"entries"`
		}
		if err := c.api().get(ctx, "/cache", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Entries, func() {
			rows := make([][]string, 0, len(out.Entries))
			for _, e := range out.Entries {
				rows = append(rows, []string{shortID(e.ID), e.Key, e.Scope,
					humanBytes(e.SizeBytes), relT(e.LastUsedAt)})
			}
			table([]string{"ID", "KEY", "SCOPE", "SIZE", "LAST USED"}, rows)
		})
	case "delete":
		id, err := strarg(args, 1, "cache id")
		if err != nil {
			return err
		}
		if err := c.api().del(ctx, "/cache/"+id); err != nil {
			return err
		}
		fmt.Println("cache entry deleted")
	default:
		return fmt.Errorf("unknown cache subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdSettings(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern settings get|set <key> [value] [--project S]")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args[1:], "project")
	path := "/settings"
	if first(opts["project"], c.project) != "" {
		path = "/projects/" + first(opts["project"], c.project) + "/settings"
	}
	switch args[0] {
	case "get":
		var out struct {
			SelfHeal            string `json:"self_heal"`
			PublicCI            string `json:"public_ci"`
			AutoUpdate          string `json:"auto_update"`
			RunnerAutostart     string `json:"runner_autostart"`
			ForkGate            string `json:"fork_gate"`
			PrTrust             string `json:"pr_trust"`
			MaxConcurrentJobs   string `json:"max_concurrent_jobs"`
			MaxTransientRetries string `json:"max_transient_retries"`
			Toolset             string `json:"toolset"`
		}
		if err := c.api().get(ctx, path, nil, &out); err != nil {
			return err
		}
		if c.json {
			printJSON(out)
		} else {
			fmt.Println("self_heal:", out.SelfHeal)
			if path == "/settings" {
				v := out.PublicCI
				if v == "" {
					v = "off"
				}
				fmt.Println("public_ci:", v)
				v = out.AutoUpdate
				if v == "" {
					v = "off"
				}
				fmt.Println("auto_update:", v)
				v = out.RunnerAutostart
				if v == "" {
					v = "on"
				}
				fmt.Println("runner_autostart:", v)
			} else {
				v := out.ForkGate
				if v == "" {
					v = "off"
				}
				fmt.Println("fork_gate:", v)
				v = out.PrTrust
				if v == "" {
					v = "off"
				}
				fmt.Println("pr_trust:", v)
				v = out.MaxConcurrentJobs
				if v == "" {
					v = "0"
				}
				fmt.Println("max_concurrent_jobs:", v)
			}
			v := out.MaxTransientRetries
			if v == "" {
				v = "1"
			}
			fmt.Println("max_transient_retries:", v)
			if path == "/settings" {
				fmt.Println("toolset:", out.Toolset)
			}
		}
	case "set":
		if len(pos) < 2 {
			return fmt.Errorf("usage: kern settings set self_heal off|auto|approve [--project S] | public_ci on|off | auto_update on|off | fork_gate off|all|first-time --project S | pr_trust off|trusted --project S")
		}
		body := map[string]any{}
		switch pos[0] {
		case "self_heal":
			body["self_heal"] = pos[1]
		case "public_ci":
			if path != "/settings" {
				return fmt.Errorf("public_ci is an org setting")
			}
			if pos[1] != "on" && pos[1] != "off" {
				return fmt.Errorf("usage: kern settings set public_ci on|off")
			}
			body["public_ci"] = pos[1]
		case "auto_update":
			if path != "/settings" {
				return fmt.Errorf("auto_update is an org setting")
			}
			if pos[1] != "on" && pos[1] != "off" {
				return fmt.Errorf("usage: kern settings set auto_update on|off")
			}
			body["auto_update"] = pos[1]
		case "disk_alert_pct":
			if path != "/settings" {
				return fmt.Errorf("disk_alert_pct is an org setting")
			}
			if _, err := strconv.ParseFloat(pos[1], 64); err != nil {
				return fmt.Errorf("usage: kern settings set disk_alert_pct <0-100>")
			}
			body["disk_alert_pct"] = pos[1]
		case "runner_autostart":
			if path != "/settings" {
				return fmt.Errorf("runner_autostart is an org setting")
			}
			if pos[1] != "on" && pos[1] != "off" {
				return fmt.Errorf("usage: kern settings set runner_autostart on|off")
			}
			body["runner_autostart"] = pos[1]
		case "fork_gate":
			if path == "/settings" {
				return fmt.Errorf("fork_gate is a project setting — pass --project S")
			}
			switch pos[1] {
			case "off", "all", "first-time":
			default:
				return fmt.Errorf("usage: kern settings set fork_gate off|all|first-time --project S")
			}
			body["fork_gate"] = pos[1]
		case "pr_trust":
			if path == "/settings" {
				return fmt.Errorf("pr_trust is a project setting — pass --project S")
			}
			if pos[1] != "off" && pos[1] != "trusted" {
				return fmt.Errorf("usage: kern settings set pr_trust off|trusted --project S")
			}
			body["pr_trust"] = pos[1]
		case "max_concurrent_jobs":
			if path == "/settings" {
				return fmt.Errorf("max_concurrent_jobs is a project setting — pass --project S")
			}
			n, err := strconv.Atoi(pos[1])
			if err != nil || n < 0 {
				return fmt.Errorf("usage: kern settings set max_concurrent_jobs <n> --project S (0 disables the quota)")
			}
			body["max_concurrent_jobs"] = n
		case "toolset":
			if path != "/settings" {
				return fmt.Errorf("toolset is an org setting")
			}
			body["toolset"] = pos[1]
		case "max_transient_retries":
			if pos[1] != "inherit" {
				n, err := strconv.Atoi(pos[1])
				if err != nil || n < 0 || n > 10 {
					return fmt.Errorf("usage: kern settings set max_transient_retries <0-10|inherit> [--project S]")
				}
			}
			if path == "/settings" && pos[1] == "inherit" {
				return fmt.Errorf("inherit is only valid with --project S (org default is 1)")
			}
			body["max_transient_retries"] = pos[1]
		default:
			return fmt.Errorf("usage: kern settings set self_heal off|auto|approve [--project S] | public_ci on|off | auto_update on|off | fork_gate off|all|first-time --project S | pr_trust off|trusted --project S")
		}
		var out map[string]string
		if err := c.api().put(ctx, path, body, &out); err != nil {
			return err
		}
		fmt.Println(pos[0], "=", pos[1])
	default:
		return fmt.Errorf("unknown settings subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdSecrets(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern secrets list|set|delete [--project S]\n       kern secrets set NAME [--value V] — omit --value to be prompted, or pipe via --value -")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args[1:], "project", "value")
	project := first(opts["project"], c.project)
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	switch args[0] {
	case "list":
		var out struct {
			Secrets []secretSummary `json:"secrets"`
		}
		if err := c.api().get(ctx, "/secrets", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Secrets, func() {
			rows := make([][]string, 0, len(out.Secrets))
			for _, s := range out.Secrets {
				scope := "org"
				if s.ProjectID != nil {
					scope = "project"
				}
				rows = append(rows, []string{s.Name, scope, rel(s.UpdatedAt)})
			}
			table([]string{"NAME", "SCOPE", "UPDATED"}, rows)
		})
	case "set":
		name, err := strarg(pos, 0, "secret name")
		if err != nil {
			return err
		}
		value := opts["value"]
		switch {
		case value == "-":
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read secret from stdin: %w", err)
			}
			value = strings.TrimSuffix(string(data), "\n")
			if value == "" {
				return fmt.Errorf("empty secret on stdin")
			}
		case value != "":
			fmt.Fprintln(os.Stderr, "kern: warning: --value exposes the secret in shell history and the process list; prefer --value - with stdin")
		default:
			v, err := readPassword("secret value: ")
			if err != nil {
				return err
			}
			value = v
		}
		path := "/secrets/" + name
		if project != "" {
			path += "?project=" + url.QueryEscape(project)
		}
		if err := c.api().put(ctx, path, map[string]any{"value": value}, nil); err != nil {
			return err
		}
		fmt.Println("secret", name, "stored")
	case "delete":
		name, err := strarg(pos, 0, "secret name")
		if err != nil {
			return err
		}
		path := "/secrets/" + name
		if project != "" {
			path += "?project=" + url.QueryEscape(project)
		}
		if err := c.api().del(ctx, path); err != nil {
			return err
		}
		fmt.Println("secret", name, "deleted")
	default:
		return fmt.Errorf("unknown secrets subcommand %q", args[0])
	}
	return nil
}

// cmdVars manages project configuration variables — the GitHub `vars`
// context. Unlike secrets, values are plaintext configuration and are
// delivered to every run including untrusted pull_request runs.
func (c *cli) cmdVars(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern vars list|set|delete <name> [value] [--project S]")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args[1:], "project", "value")
	slug, err := c.projectSlug(opts)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("project", slug)
	switch args[0] {
	case "list":
		var out struct {
			Vars []varRow `json:"vars"`
		}
		if err := c.api().get(ctx, "/vars", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Vars, func() {
			rows := make([][]string, 0, len(out.Vars))
			for _, v := range out.Vars {
				rows = append(rows, []string{v.Name, v.Value, relT(v.UpdatedAt)})
			}
			table([]string{"NAME", "VALUE", "UPDATED"}, rows)
		})
	case "set":
		name, err := strarg(pos, 0, "variable name")
		if err != nil {
			return err
		}
		value := opts["value"]
		if value == "" && len(pos) > 1 {
			value = pos[1]
		}
		if value == "" {
			return fmt.Errorf("variable value required: kern vars set %s <value> --project %s", name, slug)
		}
		path := "/vars/" + url.PathEscape(name) + "?" + q.Encode()
		if err := c.api().put(ctx, path, map[string]any{"value": value}, nil); err != nil {
			return err
		}
		fmt.Println("var", name, "stored")
	case "delete":
		name, err := strarg(pos, 0, "variable name")
		if err != nil {
			return err
		}
		path := "/vars/" + url.PathEscape(name) + "?" + q.Encode()
		if err := c.api().del(ctx, path); err != nil {
			return err
		}
		fmt.Println("var", name, "deleted")
	default:
		return fmt.Errorf("unknown vars subcommand %q", args[0])
	}
	return nil
}

type varRow struct {
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type environmentRow struct {
	Name              string    `json:"name"`
	Protected         bool      `json:"protected"`
	RequiredApprovals int       `json:"required_approvals"`
	ReviewerEmails    []string  `json:"reviewer_emails"`
	WaitMinutes       int       `json:"wait_minutes"`
	BranchPolicy      string    `json:"branch_policy"`
	BranchPatterns    []string  `json:"branch_patterns"`
	CreatedAt         time.Time `json:"created_at"`
}

func (c *cli) projectSlug(opts map[string]string) (string, error) {
	slug := first(opts["project"], c.project)
	if slug == "" {
		return "", fmt.Errorf("set --project or kern config set project <slug>")
	}
	return slug, nil
}

func (c *cli) cmdEnvironments(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern environments list|set|delete [--project S]")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args[1:], "project", "protected", "approvals", "reviewers", "wait", "branches")
	slug, err := c.projectSlug(opts)
	if err != nil {
		return err
	}
	base := "/projects/" + url.PathEscape(slug) + "/environments"
	switch args[0] {
	case "list":
		var out struct {
			Environments []environmentRow `json:"environments"`
		}
		if err := c.api().get(ctx, base, nil, &out); err != nil {
			return err
		}
		c.orJSON(out.Environments, func() {
			rows := make([][]string, 0, len(out.Environments))
			for _, e := range out.Environments {
				gate := "open"
				if e.Protected {
					gate = "protected"
				}
				branches := e.BranchPolicy
				if branches == "selected" {
					branches = strings.Join(e.BranchPatterns, ",")
				}
				rows = append(rows, []string{
					e.Name, gate, strconv.Itoa(e.RequiredApprovals), strconv.Itoa(e.WaitMinutes) + "m",
					branches, strings.Join(e.ReviewerEmails, ","), relT(e.CreatedAt),
				})
			}
			table([]string{"NAME", "GATE", "APPROVALS", "WAIT", "BRANCHES", "REVIEWERS", "CREATED"}, rows)
		})
	case "set":
		name, err := strarg(pos, 0, "environment name")
		if err != nil {
			return err
		}
		protected := false
		approvals := 0
		waitMin := 0
		policy := "all"
		var reviewers, patterns []string
		var cur struct {
			Environments []environmentRow `json:"environments"`
		}
		if err := c.api().get(ctx, base, nil, &cur); err != nil {
			return err
		}
		for _, e := range cur.Environments {
			if e.Name == name {
				protected = e.Protected
				approvals = e.RequiredApprovals
				waitMin = e.WaitMinutes
				reviewers = e.ReviewerEmails
				policy = e.BranchPolicy
				patterns = e.BranchPatterns
				if policy == "" {
					policy = "all"
				}
				break
			}
		}
		if v, ok := opts["protected"]; ok {
			switch v {
			case "true", "1", "yes":
				protected = true
			case "false", "0", "no":
				protected = false
			default:
				return fmt.Errorf("--protected expects true or false, got %q", v)
			}
		}
		if v, ok := opts["approvals"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--approvals expects an integer, got %q", v)
			}
			approvals = n
		}
		if v, ok := opts["reviewers"]; ok {
			reviewers = splitCSV(v)
		}
		if v, ok := opts["wait"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--wait expects minutes, got %q", v)
			}
			waitMin = n
		}
		if v, ok := opts["branches"]; ok {
			if v == "all" || v == "" {
				policy = "all"
				patterns = nil
			} else {
				policy = "selected"
				patterns = splitCSV(v)
			}
		}
		if protected && approvals < 1 && waitMin < 1 && policy != "selected" {
			approvals = 1
		}
		var out struct {
			Environment environmentRow `json:"environment"`
		}
		body := map[string]any{
			"protected": protected, "required_approvals": approvals,
			"reviewer_emails": reviewers, "wait_minutes": waitMin,
			"branch_policy": policy, "branch_patterns": patterns,
		}
		if err := c.api().put(ctx, base+"/"+url.PathEscape(name), body, &out); err != nil {
			return err
		}
		c.orJSON(out.Environment, func() {
			fmt.Printf("%s protected=%t approvals=%d wait=%dm branches=%s reviewers=%s\n",
				out.Environment.Name, out.Environment.Protected, out.Environment.RequiredApprovals,
				out.Environment.WaitMinutes, out.Environment.BranchPolicy, strings.Join(out.Environment.ReviewerEmails, ","))
		})
	case "delete":
		name, err := strarg(pos, 0, "environment name")
		if err != nil {
			return err
		}
		if err := c.api().del(ctx, base+"/"+url.PathEscape(name)); err != nil {
			return err
		}
		fmt.Println("environment", name, "deleted")
	default:
		return fmt.Errorf("unknown environments subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdDeployments(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern deployments list|approve|reject")
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args[1:], "project")
	switch args[0] {
	case "list":
		q := url.Values{}
		if slug := first(opts["project"], c.project); slug != "" {
			q.Set("project", slug)
		}
		var out struct {
			Deployments []struct {
				EnvironmentName string `json:"environment_name"`
				Status          string `json:"status"`
				JobID           string `json:"job_id"`
				JobName         string `json:"job_name"`
				RunNumber       int    `json:"run_number"`
				ProjectSlug     string `json:"project_slug"`
				Ref             string `json:"ref"`
				Approvals       []any  `json:"approvals"`
				Required        int    `json:"required_approvals"`
			} `json:"deployments"`
		}
		if err := c.api().get(ctx, "/deployments", q, &out); err != nil {
			return err
		}
		c.orJSON(out.Deployments, func() {
			rows := make([][]string, 0, len(out.Deployments))
			for _, d := range out.Deployments {
				rows = append(rows, []string{
					d.ProjectSlug, d.EnvironmentName, d.Status, d.JobName,
					fmt.Sprintf("#%d", d.RunNumber),
					fmt.Sprintf("%d/%d", len(d.Approvals), d.Required),
					d.JobID,
				})
			}
			table([]string{"PROJECT", "ENV", "STATUS", "JOB", "RUN", "APPROVALS", "JOB ID"}, rows)
		})
	case "approve", "reject":
		id, err := strarg(pos, 0, "job id")
		if err != nil {
			return err
		}
		var out map[string]any
		if err := c.api().post(ctx, "/jobs/"+id+"/"+args[0], map[string]any{}, &out); err != nil {
			return err
		}
		c.orJSON(out, func() {
			fmt.Println(args[0], id)
		})
	default:
		return fmt.Errorf("unknown deployments subcommand %q", args[0])
	}
	return nil
}

// versionDrift summarizes runner version spread for `runners list`. Returns
// "" when the fleet is uniform (or empty); otherwise a one-line histogram so
// operators can spot stale runners without opening the UI (#181).
func versionDrift(runners []runnerRow) string {
	counts := map[string]int{}
	for _, r := range runners {
		v := r.Version
		if v == "" {
			v = "unknown"
		}
		counts[v]++
	}
	if len(counts) <= 1 {
		return ""
	}
	versions := make([]string, 0, len(counts))
	for v := range counts {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	parts := make([]string, 0, len(versions))
	for _, v := range versions {
		parts = append(parts, fmt.Sprintf("%s×%d", v, counts[v]))
	}
	return fmt.Sprintf("versions: %s — fleet is split, check 'kern runners self-update'",
		strings.Join(parts, " "))
}
