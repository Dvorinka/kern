package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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

func promptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// cmdAuth is the unified authentication command group (`kern auth <subcommand>`),
// matching `gh auth` / `vercel` conventions while keeping top-level `kern login`,
// `kern logout`, `kern whoami`, and `kern token` as full aliases.
func (c *cli) cmdAuth(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.cmdAuthStatus(ctx)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "login":
		return c.cmdLogin(ctx, rest)
	case "logout":
		return c.cmdLogout(ctx, rest)
	case "status":
		return c.cmdAuthStatus(ctx)
	case "whoami":
		return c.cmdWhoami(ctx)
	case "switch":
		return c.cmdContext(append([]string{"use"}, rest...))
	case "token":
		return c.cmdToken(ctx, rest)
	default:
		return fmt.Errorf("unknown auth subcommand %q — use login, status, switch, logout, or token", sub)
	}
}

// cmdInstall is the guided first-run wizard: picks server URL + browser login.
func (c *cli) cmdInstall(ctx context.Context, args []string) error {
	return c.cmdLogin(ctx, args)
}

// cmdLogin authenticates against a KERN server and saves the token into a named
// context. Interactive sessions default to browser-based authorization (no
// password in the terminal); --with-token and --email/--password remain
// available for scripts and headless automation.
func (c *cli) cmdLogin(ctx context.Context, args []string) error {
	bools := []string{"web", "no-browser", "with-token", "password-stdin"}
	opts, pos := parseFlagsEx(args, bools,
		"server", "url", "context", "email", "password", "name", "days", "project")

	// Allow positional server URL: `kern login https://kern.example.com`
	explicitServer := first(opts["server"], opts["url"], c.server)
	if explicitServer == "" && len(pos) > 0 {
		explicitServer = pos[0]
	}

	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Resolve server URL (interactive picker when omitted on a TTY).
	server, err := c.pickServerForLogin(explicitServer, isTTY, reader)
	if err != nil {
		return err
	}
	c.server = normalizeServerURL(server)

	// Probe the server immediately so typos / unreachable hosts fail fast
	// before opening a browser or asking for credentials.
	serverVer, probeErr := probeServerMeta(ctx, c.server)
	if probeErr != nil {
		return fmt.Errorf("cannot reach KERN server at %s: %w\nHint: pass --url https://your-kern-host or run `kern context set-url <url>`", c.server, probeErr)
	}
	if !c.json {
		if serverVer != "" {
			fmt.Fprintf(os.Stderr, "%s Connected to %s (%s)\n", styleGreen("✓"), styleBold(c.server), serverVer)
		} else {
			fmt.Fprintf(os.Stderr, "%s Connected to %s\n", styleGreen("✓"), styleBold(c.server))
		}
	}

	ctxName := first(opts["context"], c.context)
	if ctxName == "" {
		if existing := c.cfg.findContextByServer(c.server); existing != "" {
			ctxName = existing
		} else {
			ctxName = contextNameForURL(c.server)
		}
	}

	// Step 2: Choose auth mode.
	usePassword := opts["email"] != "" || opts["password"] != "" || opts["password-stdin"] == "true"
	useToken := opts["with-token"] == "true"

	if useToken {
		return c.loginWithToken(ctx, ctxName, first(opts["project"], c.project), reader, isTTY)
	}
	if usePassword {
		return c.loginWithPassword(ctx, ctxName, opts, reader)
	}
	if !isTTY && opts["web"] != "true" {
		return fmt.Errorf("non-interactive login requires --with-token, --email/--password, or --web")
	}
	return c.loginWithBrowser(ctx, ctxName, opts)
}

func (c *cli) pickServerForLogin(explicit string, isTTY bool, reader *bufio.Reader) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	envServer := first(os.Getenv("KERN_SERVER"), os.Getenv("KERN_URL"))
	if !isTTY {
		if envServer != "" {
			return envServer, nil
		}
		if c.cfg.Server != "" {
			return c.cfg.Server, nil
		}
		return "", fmt.Errorf("server URL is required — pass --url <url> or set KERN_URL")
	}

	names := c.cfg.contextNames()
	defaultURL := first(envServer, c.cfg.Server)

	if len(names) > 0 {
		fmt.Fprintln(os.Stderr, styleBold("Select a KERN server or enter a new URL:"))
		for i, name := range names {
			cc := c.cfg.Contexts[name]
			mark := " "
			if name == c.cfg.CurrentContext {
				mark = "*"
			}
			who := ""
			if cc.Email != "" {
				who = " (" + cc.Email + ")"
			}
			fmt.Fprintf(os.Stderr, "  %d) %s %s  %s%s\n", i+1, mark, styleBold(name), cc.Server, styleDim(who))
		}
		fmt.Fprintf(os.Stderr, "  %d) Enter a new server URL\n", len(names)+1)
		prompt := fmt.Sprintf("Choice [1-%d or URL] (Enter = %s): ", len(names)+1, defaultURL)
		choice, err := promptLine(reader, prompt)
		if err != nil {
			return "", err
		}
		if choice == "" && defaultURL != "" {
			return defaultURL, nil
		}
		if n, err := strconv.Atoi(choice); err == nil {
			if n >= 1 && n <= len(names) {
				c.context = names[n-1]
				return c.cfg.Contexts[names[n-1]].Server, nil
			}
			if n == len(names)+1 {
				entered, err := promptLine(reader, "KERN server URL (e.g. https://kern.example.com): ")
				if err != nil {
					return "", err
				}
				if entered == "" {
					return "", fmt.Errorf("server URL is required")
				}
				return entered, nil
			}
		}
		if choice != "" {
			return choice, nil
		}
	}

	prompt := "KERN server URL (e.g. https://kern.example.com or http://localhost:8080): "
	if defaultURL != "" {
		prompt = fmt.Sprintf("KERN server URL [%s]: ", defaultURL)
	}
	entered, err := promptLine(reader, prompt)
	if err != nil {
		return "", err
	}
	if entered == "" {
		entered = defaultURL
	}
	if entered == "" {
		return "", fmt.Errorf("server URL is required")
	}
	return entered, nil
}

func probeServerMeta(ctx context.Context, server string) (string, error) {
	var meta struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Env     string `json:"env"`
	}
	cl := newClient(server, "")
	cl.http.Timeout = 8 * time.Second
	if err := cl.get(ctx, "/meta", nil, &meta); err != nil {
		return "", err
	}
	if meta.Version != "" {
		return meta.Version, nil
	}
	return meta.Name, nil
}

// loginWithBrowser runs the device-style browser authorization flow:
// POST /auth/cli/start -> open web approval page -> poll /auth/cli/poll.
func (c *cli) loginWithBrowser(ctx context.Context, ctxName string, opts map[string]string) error {
	host, _ := os.Hostname()
	name := opts["name"]
	if name == "" {
		if host != "" {
			name = "kern-cli (" + host + ")"
		} else {
			name = "kern-cli"
		}
	}
	days := 90
	if opts["days"] != "" {
		days = atoi(opts["days"])
	}

	cl := newClient(c.server, "")
	var started struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := cl.post(ctx, "/auth/cli/start", map[string]any{
		"name": name, "hostname": host, "days": days,
	}, &started); err != nil {
		return fmt.Errorf("browser login unavailable on %s (%w) — pass --email/--password or --with-token", c.server, err)
	}

	verifyURL := started.VerificationURL
	if verifyURL == "" {
		verifyURL = c.server + "/auth/cli?code=" + url.QueryEscape(started.UserCode)
	}

	if !c.json {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "  Verification code:  %s\n", styleBold(styleCyan(started.UserCode)))
		fmt.Fprintf(os.Stderr, "  Authorization URL:  %s\n", verifyURL)
		fmt.Fprintln(os.Stderr)
	}

	if opts["no-browser"] != "true" {
		if err := openBrowser(verifyURL); err != nil {
			if !c.json {
				fmt.Fprintf(os.Stderr, "  Open the URL above in your browser to approve this CLI.\n")
			}
		} else if !c.json {
			fmt.Fprintf(os.Stderr, "  Browser opened. Confirm the code matches and click %s.\n", styleBold("Authorize CLI"))
		}
	} else if !c.json {
		fmt.Fprintf(os.Stderr, "  Open the URL above in any browser and click %s.\n", styleBold("Authorize CLI"))
	}

	interval := time.Duration(started.Interval) * time.Second
	if interval < time.Second {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	if started.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}

	if !c.json {
		fmt.Fprint(os.Stderr, "  Waiting for browser approval (Ctrl-C to cancel)...")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if time.Now().After(deadline) {
			if !c.json {
				fmt.Fprintln(os.Stderr)
			}
			return fmt.Errorf("login timed out — run `kern auth login` to try again")
		}
		select {
		case <-ctx.Done():
			if !c.json {
				fmt.Fprintln(os.Stderr)
			}
			return fmt.Errorf("login cancelled")
		case <-ticker.C:
			var res struct {
				Status string   `json:"status"`
				Token  string   `json:"token"`
				Record apiToken `json:"record"`
				User   user     `json:"user"`
			}
			err := cl.post(ctx, "/auth/cli/poll", map[string]any{
				"device_code": started.DeviceCode,
			}, &res)
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) {
					if ae.Status == http.StatusForbidden || ae.Status == http.StatusGone {
						if !c.json {
							fmt.Fprintln(os.Stderr)
						}
						return err
					}
				}
				continue
			}
			if res.Status == "approved" && res.Token != "" {
				if !c.json {
					fmt.Fprintln(os.Stderr, " "+styleGreen("approved"))
				}
				return c.finishLogin(ctxName, res.Token, res.Record.ID, res.Record.Prefix, res.User.Email, first(opts["project"], c.project))
			}
		}
	}
}

func (c *cli) loginWithToken(ctx context.Context, ctxName, project string, reader *bufio.Reader, isTTY bool) error {
	tok := strings.TrimSpace(c.token)
	if tok == "" {
		if isTTY {
			var err error
			tok, err = readPassword("API token: ")
			if err != nil {
				return err
			}
		} else {
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return fmt.Errorf("read token from stdin: %w", err)
			}
			tok = strings.TrimSpace(line)
		}
	}
	if tok == "" {
		return fmt.Errorf("empty API token")
	}
	var out struct {
		User user `json:"user"`
	}
	if err := newClient(c.server, tok).get(ctx, "/auth/me", nil, &out); err != nil {
		return fmt.Errorf("token rejected by %s: %w", c.server, err)
	}
	prefix := tok
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return c.finishLogin(ctxName, tok, "", prefix, out.User.Email, project)
}

func (c *cli) loginWithPassword(ctx context.Context, ctxName string, opts map[string]string, reader *bufio.Reader) error {
	email := opts["email"]
	if email == "" {
		var err error
		email, err = promptLine(reader, "email: ")
		if err != nil {
			return err
		}
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
	return c.finishLogin(ctxName, out.Token, out.Record.ID, out.Record.Prefix, out.User.Email, first(opts["project"], c.project))
}

func (c *cli) finishLogin(ctxName, token, tokenID, prefix, email, project string) error {
	existing := c.cfg.Contexts[ctxName]
	if project == "" {
		project = existing.Project
	}
	c.cfg.upsertContext(ctxName, contextConfig{
		Server:      c.server,
		Token:       token,
		TokenID:     tokenID,
		TokenPrefix: prefix,
		Email:       email,
		Project:     project,
	}, true)
	if err := saveConfig(c.cfg); err != nil {
		return fmt.Errorf("token issued but config write failed: %w", err)
	}
	c.orJSON(map[string]any{
		"context":      c.cfg.CurrentContext,
		"server":       c.server,
		"email":        email,
		"token_prefix": prefix,
		"config":       configPath(),
	}, func() {
		fmt.Printf("%s Logged in as %s on %s %s\n",
			styleGreen("✓"), styleBold(email), styleBold(c.server),
			styleDim(fmt.Sprintf("[context: %s, token: %s…]", c.cfg.CurrentContext, prefix)))
	})
	return nil
}

func (c *cli) cmdLogout(ctx context.Context, args []string) error {
	opts, pos := parseFlagsEx(args, []string{"all"}, "context")
	if opts["all"] == "true" {
		for name, cc := range c.cfg.Contexts {
			if cc.TokenID != "" && cc.Token != "" && cc.Server != "" {
				_ = newClient(cc.Server, cc.Token).del(ctx, "/auth/tokens/"+cc.TokenID)
			}
			cc.Token, cc.TokenID, cc.TokenPrefix, cc.Email = "", "", "", ""
			c.cfg.Contexts[name] = cc
		}
		c.cfg.Token, c.cfg.TokenID = "", ""
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"ok": true, "all": true}, func() {
			fmt.Println("logged out of all contexts; local credentials cleared")
		})
		return nil
	}

	target := first(opts["context"], c.context)
	if target == "" && len(pos) > 0 {
		target = pos[0]
	}
	name, cc, _ := c.cfg.activeContext(target)
	if cc.TokenID != "" && cc.Token != "" && cc.Server != "" {
		_ = newClient(cc.Server, cc.Token).del(ctx, "/auth/tokens/"+cc.TokenID) // best effort
	}
	cc.Token, cc.TokenID, cc.TokenPrefix, cc.Email = "", "", "", ""
	c.cfg.upsertContext(name, cc, name == c.cfg.CurrentContext)
	if err := saveConfig(c.cfg); err != nil {
		return err
	}
	c.orJSON(map[string]any{"ok": true, "context": name}, func() {
		fmt.Printf("logged out of context %s; local credentials cleared\n", styleBold(name))
	})
	return nil
}

func (c *cli) cmdAuthStatus(ctx context.Context) error {
	name, cc, _ := c.cfg.activeContext(c.context)
	server := normalizeServerURL(first(c.server, os.Getenv("KERN_SERVER"), os.Getenv("KERN_URL"), cc.Server))
	tok := first(c.token, os.Getenv("KERN_TOKEN"), cc.Token)
	proj := first(c.project, os.Getenv("KERN_PROJECT"), cc.Project)

	status := map[string]any{
		"context":     name,
		"server":      server,
		"project":     proj,
		"config_path": configPath(),
		"logged_in":   false,
	}

	var versionStr string
	var authUser *user
	var authErr string
	if server != "" {
		if v, err := probeServerMeta(ctx, server); err == nil {
			versionStr = v
			status["server_version"] = v
			status["server_reachable"] = true
		} else {
			status["server_reachable"] = false
			status["server_error"] = err.Error()
		}
		if tok != "" {
			var out struct {
				User user `json:"user"`
			}
			if err := newClient(server, tok).get(ctx, "/auth/me", nil, &out); err == nil {
				authUser = &out.User
				status["logged_in"] = true
				status["user"] = out.User
			} else {
				authErr = err.Error()
				status["auth_error"] = authErr
			}
		}
	}

	c.orJSON(status, func() {
		fmt.Printf("Context:  %s", styleBold(name))
		if len(c.cfg.Contexts) > 1 {
			fmt.Printf("  %s", styleDim(fmt.Sprintf("(%d total — `kern context list`)", len(c.cfg.Contexts))))
		}
		fmt.Println()
		if server == "" {
			fmt.Printf("Server:   %s\n", styleYellow("not configured — run `kern auth login`"))
		} else if versionStr != "" {
			fmt.Printf("Server:   %s  %s\n", server, styleGreen("reachable ("+versionStr+")"))
		} else {
			fmt.Printf("Server:   %s  %s\n", server, styleRed("unreachable"))
		}
		switch {
		case authUser != nil:
			fmt.Printf("Account:  %s (%s)  %s\n", styleBold(authUser.Email), authUser.Role, styleGreen("authenticated"))
		case tok == "":
			fmt.Printf("Account:  %s\n", styleYellow("not logged in — run `kern auth login`"))
		default:
			fmt.Printf("Account:  %s (%s)\n", styleRed("token invalid"), authErr)
		}
		if tok != "" {
			prefix := cc.TokenPrefix
			if prefix == "" && len(tok) > 12 {
				prefix = tok[:12]
			}
			fmt.Printf("Token:    %s…\n", prefix)
		}
		if proj != "" {
			fmt.Printf("Project:  %s\n", proj)
		}
		fmt.Printf("Config:   %s\n", styleDim(configPath()))
	})
	return nil
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

// cmdContext manages saved KERN server profiles (`kern context list|use|current|set-url|rm`).
func (c *cli) cmdContext(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	c.cfg.normalize()
	switch args[0] {
	case "list", "ls":
		names := c.cfg.contextNames()
		c.orJSON(map[string]any{
			"current":  c.cfg.CurrentContext,
			"contexts": c.cfg.Contexts,
		}, func() {
			if len(names) == 0 {
				fmt.Println("No saved contexts. Run `kern auth login` to connect to a KERN server.")
				return
			}
			rows := make([][]string, 0, len(names))
			for _, name := range names {
				cc := c.cfg.Contexts[name]
				cur := ""
				if name == c.cfg.CurrentContext {
					cur = styleGreen("*")
				}
				tok := "-"
				if cc.Token != "" {
					p := cc.TokenPrefix
					if p == "" && len(cc.Token) > 10 {
						p = cc.Token[:10]
					}
					tok = p + "…"
				}
				email := cc.Email
				if email == "" {
					email = "-"
				}
				proj := cc.Project
				if proj == "" {
					proj = "-"
				}
				rows = append(rows, []string{cur, name, cc.Server, email, proj, tok})
			}
			table([]string{"", "CONTEXT", "SERVER", "ACCOUNT", "PROJECT", "TOKEN"}, rows)
		})
	case "current":
		name, cc, _ := c.cfg.activeContext(c.context)
		c.orJSON(map[string]any{"context": name, "server": cc.Server, "email": cc.Email, "project": cc.Project}, func() {
			if cc.Server == "" {
				fmt.Println("no active context")
				return
			}
			fmt.Printf("%s  %s\n", styleBold(name), cc.Server)
		})
	case "use", "switch":
		name, err := strarg(args, 1, "context name")
		if err != nil {
			// Interactive selection when omitted on a TTY.
			if term.IsTerminal(int(os.Stdin.Fd())) && len(c.cfg.Contexts) > 0 {
				names := c.cfg.contextNames()
				for i, n := range names {
					fmt.Fprintf(os.Stderr, "  %d) %s  %s\n", i+1, styleBold(n), c.cfg.Contexts[n].Server)
				}
				choice, perr := promptLine(bufio.NewReader(os.Stdin), fmt.Sprintf("Switch to [1-%d]: ", len(names)))
				if perr != nil {
					return perr
				}
				idx, convErr := strconv.Atoi(choice)
				if convErr == nil && idx >= 1 && idx <= len(names) {
					name = names[idx-1]
				} else if _, ok := c.cfg.Contexts[choice]; ok {
					name = choice
				} else {
					return fmt.Errorf("invalid selection %q", choice)
				}
			} else {
				return fmt.Errorf("usage: kern context use <name>")
			}
		}
		cc, ok := c.cfg.Contexts[name]
		if !ok {
			return fmt.Errorf("unknown context %q — run `kern context list`", name)
		}
		c.cfg.upsertContext(name, cc, true)
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"context": name, "server": cc.Server}, func() {
			fmt.Printf("Switched to context %s (%s)\n", styleBold(name), cc.Server)
		})
	case "set-url", "url":
		var name, rawURL string
		switch len(args) {
		case 2:
			name = first(c.context, c.cfg.CurrentContext)
			rawURL = args[1]
		case 3:
			name = args[1]
			rawURL = args[2]
		default:
			return fmt.Errorf("usage: kern context set-url [<context>] <url>")
		}
		norm := normalizeServerURL(rawURL)
		if norm == "" {
			return fmt.Errorf("invalid URL %q", rawURL)
		}
		if name == "" {
			name = contextNameForURL(norm)
		}
		cc := c.cfg.Contexts[name]
		cc.Server = norm
		c.cfg.upsertContext(name, cc, name == c.cfg.CurrentContext || c.cfg.CurrentContext == "")
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"context": name, "server": norm}, func() {
			fmt.Printf("Context %s server URL set to %s\n", styleBold(name), norm)
		})
	case "rm", "delete", "remove":
		name, err := strarg(args, 1, "context name")
		if err != nil {
			return err
		}
		if _, ok := c.cfg.Contexts[name]; !ok {
			return fmt.Errorf("unknown context %q", name)
		}
		delete(c.cfg.Contexts, name)
		if c.cfg.CurrentContext == name {
			c.cfg.CurrentContext = ""
			c.cfg.Server, c.cfg.Token, c.cfg.TokenID, c.cfg.Project = "", "", "", ""
		}
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"deleted": true, "context": name}, func() {
			fmt.Printf("Removed context %s\n", styleBold(name))
		})
	default:
		return fmt.Errorf("unknown context subcommand %q — use list, use, current, set-url, or rm", args[0])
	}
	return nil
}

// cmdOpen opens the active KERN server's web console in the default browser.
func (c *cli) cmdOpen(args []string) error {
	opts, pos := parseFlagsEx(args, []string{"print"})
	if err := c.resolve(); err != nil {
		return err
	}
	targetPath := "/"
	if len(pos) > 0 {
		switch pos[0] {
		case "runs", "run":
			if len(pos) > 1 {
				targetPath = "/runs/" + url.PathEscape(pos[1])
			} else {
				targetPath = "/runs"
			}
		case "projects", "project":
			if len(pos) > 1 {
				targetPath = "/projects/" + url.PathEscape(pos[1])
			} else {
				targetPath = "/projects"
			}
		case "runners", "runner":
			targetPath = "/runners"
		case "usage", "artifacts", "cache", "secrets", "environments", "settings":
			targetPath = "/" + pos[0]
		case "webhooks", "deliveries":
			targetPath = "/deliveries"
		default:
			if strings.HasPrefix(pos[0], "/") {
				targetPath = pos[0]
			} else {
				targetPath = "/" + pos[0]
			}
		}
	}
	full := strings.TrimRight(c.server, "/") + targetPath
	if opts["print"] == "true" || c.json {
		c.orJSON(map[string]string{"url": full}, func() {
			fmt.Println(full)
		})
		return nil
	}
	if err := openBrowser(full); err != nil {
		fmt.Println(full)
		return nil
	}
	fmt.Printf("Opened %s\n", full)
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
				capCol := "-"
				if t.MaxConcurrentJobs > 0 {
					capCol = strconv.Itoa(t.MaxConcurrentJobs)
				}
				rows = append(rows, []string{t.ID, t.Name, t.Prefix,
					strings.Join(t.Scopes, ","), grantCol(t.Projects), grantCol(t.Labels), capCol,
					relT(t.CreatedAt), rel(t.ExpiresAt)})
			}
			table([]string{"ID", "NAME", "PREFIX", "SCOPES", "PROJECTS", "LABELS", "CAP", "CREATED", "EXPIRES"}, rows)
		})
	case "create":
		if err := c.requireAuth(); err != nil {
			return err
		}
		opts, _ := parseFlags(args[1:], "name", "scopes", "days", "projects", "labels", "concurrency")
		var out struct {
			Token  string   `json:"token"`
			Record apiToken `json:"record"`
		}
		err := c.api().post(ctx, "/auth/tokens", map[string]any{
			"name":           opts["name"],
			"scopes":         splitCSV(opts["scopes"]),
			"days":           atoi(opts["days"]),
			"projects":       splitCSV(opts["projects"]),
			"labels":         splitCSV(opts["labels"]),
			"max_concurrent": atoi(opts["concurrency"]),
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
		c.orJSON(map[string]any{"deleted": true, "id": id}, func() {
			fmt.Println("token deleted")
		})
	default:
		return fmt.Errorf("unknown token subcommand %q", args[0])
	}
	return nil
}

func (c *cli) cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kern config list|get|set|unset|path")
	}
	name, cc, _ := c.cfg.activeContext(c.context)
	mask := func(k, v string) string {
		if k == "token" && len(v) > 12 {
			return v[:12] + "…"
		}
		return v
	}
	resolveKey := func(k string) string {
		if k == "url" {
			return "server"
		}
		return k
	}
	switch args[0] {
	case "path":
		c.orJSON(map[string]string{"path": configPath()}, func() {
			fmt.Println(configPath())
		})
		return nil
	case "list", "get":
		if len(args) == 1 {
			m := map[string]string{
				"context": name,
				"server":  cc.Server,
				"token":   mask("token", cc.Token),
				"project": cc.Project,
			}
			c.orJSON(m, func() {
				rows := [][]string{
					{"context", m["context"]},
					{"project", m["project"]},
					{"server", m["server"]},
					{"token", m["token"]},
				}
				table([]string{"KEY", "VALUE"}, rows)
			})
			return nil
		}
		key := resolveKey(args[1])
		var v string
		switch key {
		case "server":
			v = cc.Server
		case "token":
			v = mask("token", cc.Token)
		case "project":
			v = cc.Project
		case "context":
			v = name
		default:
			return fmt.Errorf("unknown config key %q — valid keys: server (url), token, project, context", args[1])
		}
		c.orJSON(map[string]string{"key": key, "value": v}, func() {
			fmt.Println(v)
		})
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: kern config set <server|url|token|project|context> <value>")
		}
		key := resolveKey(args[1])
		val := args[2]
		switch key {
		case "server":
			cc.Server = normalizeServerURL(val)
		case "token":
			cc.Token = val
			cc.TokenID = ""
			if len(val) > 12 {
				cc.TokenPrefix = val[:12]
			}
		case "project":
			cc.Project = val
		case "context":
			return c.cmdContext([]string{"use", val})
		default:
			return fmt.Errorf("unknown config key %q — valid keys: server (url), token, project, context", args[1])
		}
		c.cfg.upsertContext(name, cc, name == c.cfg.CurrentContext || c.cfg.CurrentContext == "")
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"ok": true, "key": key, "context": name}, func() {
			fmt.Printf("Set %s for context %s\n", key, styleBold(name))
		})
	case "unset":
		if len(args) < 2 {
			return fmt.Errorf("usage: kern config unset <server|url|token|project>")
		}
		key := resolveKey(args[1])
		switch key {
		case "server":
			cc.Server = ""
		case "token":
			cc.Token, cc.TokenID, cc.TokenPrefix = "", "", ""
		case "project":
			cc.Project = ""
		default:
			return fmt.Errorf("unknown config key %q", args[1])
		}
		c.cfg.upsertContext(name, cc, name == c.cfg.CurrentContext)
		if err := saveConfig(c.cfg); err != nil {
			return err
		}
		c.orJSON(map[string]any{"ok": true, "key": key, "context": name}, func() {})
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
		if a == "--" {
			// Terminator: the rest are positional args for the subcommand.
			pos = append(pos, args[i+1:]...)
			break
		}
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

func grantCol(v []string) string {
	if len(v) == 0 {
		return "-"
	}
	return strings.Join(v, ",")
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
