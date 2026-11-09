package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
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

func (c *cli) cmdRunners(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern runners list|drain|undrain|self-update|wake|install|delete|enroll")
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
				rows = append(rows, []string{shortID(r.ID), r.Name, state,
					fmt.Sprintf("%d", r.ActiveJobs), cpu, mem, disk, rel(r.LastHeartbeatAt)})
			}
			table([]string{"ID", "NAME", "STATE", "JOBS", "CPU", "MEM", "DISK", "BEAT"}, rows)
		})
	case "drain", "undrain":
		arg, err := strarg(rest, 0, "runner id|name")
		if err != nil {
			return err
		}
		r, err := c.resolveRunner(ctx, arg)
		if err != nil {
			return err
		}
		if err := c.api().post(ctx, "/runners/"+r.ID+"/drain",
			map[string]any{"draining": sub == "drain"}, nil); err != nil {
			return err
		}
		fmt.Println(r.Name, sub+"ed")
	case "self-update":
		arg, err := strarg(rest, 0, "runner id|name")
		if err != nil {
			return err
		}
		on, err := strarg(rest, 1, "on|off")
		if err != nil {
			return err
		}
		if on != "on" && on != "off" {
			return fmt.Errorf("self-update takes on|off, got %q", on)
		}
		r, err := c.resolveRunner(ctx, arg)
		if err != nil {
			return err
		}
		if err := c.api().post(ctx, "/runners/"+r.ID+"/auto-update",
			map[string]any{"enabled": on == "on"}, nil); err != nil {
			return err
		}
		fmt.Printf("%s self-update %s\n", r.Name, on)
	case "wake":
		arg, err := strarg(rest, 0, "runner id|name")
		if err != nil {
			return err
		}
		on, err := strarg(rest, 1, "on|off")
		if err != nil {
			return err
		}
		if on != "on" && on != "off" {
			return fmt.Errorf("wake takes on|off, got %q", on)
		}
		r, err := c.resolveRunner(ctx, arg)
		if err != nil {
			return err
		}
		if err := c.api().patch(ctx, "/runners/"+r.ID,
			map[string]any{"wake_on_demand": on == "on"}, nil); err != nil {
			return err
		}
		fmt.Printf("%s wake_on_demand %s\n", r.Name, on)
	case "install":
		return c.runnerInstall(ctx, rest)
	case "delete":
		arg, err := strarg(rest, 0, "runner id|name")
		if err != nil {
			return err
		}
		r, err := c.resolveRunner(ctx, arg)
		if err != nil {
			return err
		}
		if err := c.api().del(ctx, "/runners/"+r.ID); err != nil {
			return err
		}
		fmt.Println(r.Name, "deleted")
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
		return fmt.Errorf("usage: kern artifacts list|download")
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
			SelfHeal        string `json:"self_heal"`
			PublicCI        string `json:"public_ci"`
			AutoUpdate      string `json:"auto_update"`
			RunnerAutostart string `json:"runner_autostart"`
			ForkGate        string `json:"fork_gate"`
			PrTrust         string `json:"pr_trust"`
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
