package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
)

func (c *cli) cmdRunners(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern runners list|drain|undrain|delete|enroll")
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
		id, err := strarg(rest, 0, "runner id")
		if err != nil {
			return err
		}
		if err := c.api().post(ctx, "/runners/"+id+"/drain",
			map[string]any{"draining": sub == "drain"}, nil); err != nil {
			return err
		}
		fmt.Println(sub + "ed")
	case "delete":
		id, err := strarg(rest, 0, "runner id")
		if err != nil {
			return err
		}
		if err := c.api().del(ctx, "/runners/"+id); err != nil {
			return err
		}
		fmt.Println("runner deleted")
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
		fmt.Printf("on the runner host:\n  kern-runner join --control %s --token %s\n", c.server, out.Token)
	default:
		return fmt.Errorf("unknown runners subcommand %q", sub)
	}
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
			SelfHeal string `json:"self_heal"`
		}
		if err := c.api().get(ctx, path, nil, &out); err != nil {
			return err
		}
		if c.json {
			printJSON(out)
		} else {
			fmt.Println("self_heal:", out.SelfHeal)
		}
	case "set":
		if len(pos) < 2 || pos[0] != "self_heal" {
			return fmt.Errorf("usage: kern settings set self_heal off|auto|approve [--project S]")
		}
		var out map[string]string
		if err := c.api().put(ctx, path, map[string]any{"self_heal": pos[1]}, &out); err != nil {
			return err
		}
		fmt.Println("self_heal =", pos[1])
	default:
		return fmt.Errorf("unknown settings subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdSecrets(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern secrets list|set|delete [--project S]")
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
		if value == "" {
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
