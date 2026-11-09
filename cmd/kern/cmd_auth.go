package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword prompts on stderr and reads a line from stdin with echo off
// when stdin is a terminal; piped input is read verbatim.
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// cmdInstall is the first-run wizard: it asks for the server URL (defaulting
// to the configured one), warns when a config already exists, then runs the
// login flow to mint and save a token. Piped usage passes --server/--email/
// --password like login.
func (c *cli) cmdInstall(ctx context.Context, args []string) error {
	opts, _ := parseFlags(args, "server", "email", "password", "name", "days", "project")
	if (c.cfg.Server != "" || c.cfg.Token != "") && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "config exists at %s (server %q) — overwrite? [y/N] ", configPath(), c.cfg.Server)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
			return fmt.Errorf("aborted")
		}
	}
	server := opts["server"]
	if server == "" {
		server = c.cfg.Server
	}
	if server == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "server URL: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		server = strings.TrimSpace(line)
	}
	if server == "" {
		return fmt.Errorf("server URL is required — pass --server")
	}
	// Hand the resolved server to login; an explicit --server in args still wins.
	return c.cmdLogin(ctx, append([]string{"--server", server}, args...))
}

// cmdLogin exchanges credentials for an API token via POST /auth/token and
// saves it to the config file. No session cookie is involved.
func (c *cli) cmdLogin(ctx context.Context, args []string) error {
	opts, _ := parseFlags(args, "server", "email", "password", "name", "days", "project")
	if v := opts["server"]; v != "" {
		c.server = v
	}
	if err := c.resolve(); err != nil {
		return err
	}
	email := opts["email"]
	if email == "" {
		fmt.Fprint(os.Stderr, "email: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		email = strings.TrimSpace(line)
	}
	password := opts["password"]
	if password == "" {
		pw, err := readPassword("password: ")
		if err != nil {
			return err
		}
		password = pw
	}
	var out struct {
		Token  string   `json:"token"`
		Record apiToken `json:"record"`
		User   user     `json:"user"`
	}
	cl := newClient(c.server, "")
	err := cl.post(ctx, "/auth/token", map[string]any{
		"email": email, "password": password, "name": opts["name"],
		"days": atoi(opts["days"]),
	}, &out)
	if err != nil {
		return err
	}
	c.cfg.Server = c.server
	c.cfg.Token = out.Token
	c.cfg.TokenID = out.Record.ID
	if v := opts["project"]; v != "" {
		c.cfg.Project = v
	}
	if err := saveConfig(c.cfg); err != nil {
		return fmt.Errorf("token issued but config write failed: %w", err)
	}
	fmt.Printf("logged in as %s; token %s… saved to %s\n", out.User.Email, out.Record.Prefix, configPath())
	return nil
}

func (c *cli) cmdLogout(ctx context.Context) error {
	if c.cfg.TokenID != "" && c.cfg.Token != "" {
		cl := newClient(c.serverOrConfig(), c.cfg.Token)
		_ = cl.del(ctx, "/auth/tokens/"+c.cfg.TokenID) // best effort
	}
	c.cfg.Token, c.cfg.TokenID = "", ""
	if err := saveConfig(c.cfg); err != nil {
		return err
	}
	fmt.Println("logged out; local credentials cleared")
	return nil
}

func (c *cli) serverOrConfig() string {
	if c.server != "" {
		return c.server
	}
	if v := os.Getenv("KERN_SERVER"); v != "" {
		return v
	}
	return c.cfg.Server
}

func (c *cli) cmdWhoami(ctx context.Context) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	var out struct {
		User user `json:"user"`
	}
	if err := c.api().get(ctx, "/auth/me", nil, &out); err != nil {
		return err
	}
	c.orJSON(out.User, func() {
		table([]string{"EMAIL", "NAME", "ROLE", "ORG"},
			[][]string{{out.User.Email, out.User.Name, out.User.Role, out.User.OrgID}})
	})
	return nil
}

func (c *cli) cmdToken(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern token list|create|delete")
	}
	switch args[0] {
	case "list":
		if err := c.requireAuth(); err != nil {
			return err
		}
		var out struct {
			Tokens []apiToken `json:"tokens"`
		}
		if err := c.api().get(ctx, "/auth/tokens", nil, &out); err != nil {
			return err
		}
		c.orJSON(out.Tokens, func() {
			rows := make([][]string, 0, len(out.Tokens))
			for _, t := range out.Tokens {
				rows = append(rows, []string{t.ID, t.Name, t.Prefix,
					strings.Join(t.Scopes, ","), relT(t.CreatedAt), rel(t.ExpiresAt)})
			}
			table([]string{"ID", "NAME", "PREFIX", "SCOPES", "CREATED", "EXPIRES"}, rows)
		})
	case "create":
		if err := c.requireAuth(); err != nil {
			return err
		}
		opts, _ := parseFlags(args[1:], "name", "scopes", "days")
		var out struct {
			Token  string   `json:"token"`
			Record apiToken `json:"record"`
		}
		err := c.api().post(ctx, "/auth/tokens", map[string]any{
			"name":   opts["name"],
			"scopes": splitCSV(opts["scopes"]),
			"days":   atoi(opts["days"]),
		}, &out)
		if err != nil {
			return err
		}
		if c.json {
			printJSON(out)
		} else {
			fmt.Printf("token %s created; save it now — it is shown once:\n%s\n", out.Record.Name, out.Token)
		}
	case "delete":
		id, err := strarg(args, 1, "token id")
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		if err := c.api().del(ctx, "/auth/tokens/"+id); err != nil {
			return err
		}
		fmt.Println("token deleted")
	default:
		return fmt.Errorf("unknown token subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern config list|get|set|unset")
	}
	keys := map[string]*string{
		"server": &c.cfg.Server, "token": &c.cfg.Token, "project": &c.cfg.Project,
	}
	switch args[0] {
	case "list", "get":
		if len(args) == 1 {
			rows := [][]string{}
			for k, p := range keys {
				v := *p
				if k == "token" && len(v) > 12 {
					v = v[:12] + "…"
				}
				rows = append(rows, []string{k, v})
			}
			table([]string{"KEY", "VALUE"}, rows)
			return nil
		}
		p, ok := keys[args[1]]
		if !ok {
			return fmt.Errorf("unknown config key %q", args[1])
		}
		fmt.Println(*p)
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: kern config set <key> <value>")
		}
		p, ok := keys[args[1]]
		if !ok {
			return fmt.Errorf("unknown config key %q", args[1])
		}
		*p = args[2]
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
	case "unset":
		if len(args) < 2 {
			return fmt.Errorf("usage: kern config unset <key>")
		}
		p, ok := keys[args[1]]
		if !ok {
			return fmt.Errorf("unknown config key %q", args[1])
		}
		*p = ""
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
	return nil
}

func (c *cli) api() *client { return newClient(c.server, c.token) }

// parseFlags handles --key value and --key=value for a fixed key set; the
// remaining positional args are returned separately.
func parseFlags(args []string, keys ...string) (map[string]string, []string) {
	return parseFlagsEx(args, nil, keys...)
}

// parseFlagsEx is parseFlags plus boolean keys: a bool key never consumes
// the next arg — presence sets "true", --key=false sets "false".
func parseFlagsEx(args []string, bools []string, keys ...string) (map[string]string, []string) {
	known := map[string]bool{}
	for _, k := range keys {
		known[k] = true
	}
	isBool := map[string]bool{}
	for _, k := range bools {
		isBool[k] = true
		known[k] = true
	}
	out := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !known[key] {
			fatalf("unknown flag --%s", key)
		}
		if isBool[key] {
			if hasVal {
				out[key] = val
			} else {
				out[key] = "true"
			}
			continue
		}
		if hasVal {
			out[key] = val
			continue
		}
		if i+1 >= len(args) {
			fatalf("--%s requires a value", key)
		}
		i++
		out[key] = args[i]
	}
	return out, pos
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
