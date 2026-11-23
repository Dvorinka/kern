// kern is the operator CLI for the KERN control plane. It speaks the same
// /api/v1 contract as the web app; every command accepts --json for agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dvorinka/kern/internal/version"
)

const usage = `kern — KERN control plane CLI

Usage: kern <command> [args] [-u|--url URL] [-c|--context NAME] [-p|--project P] [-j|--json]

Setup & Navigation:
  auth login [--url URL] [--web|--no-browser|--with-token|--email E]
                        sign in via browser (default), token, or password; picks/saves server URL
  auth status           show active context, server health, account, and token info
  auth switch [NAME]    switch between saved server contexts
  auth logout [--all]   revoke saved token and clear credentials
  context list|use|current|set-url|rm   manage multiple KERN servers/profiles (alias: ctx)
  open [runs|runners|projects|settings] open active KERN web UI in your browser (alias: web)
  status [--watch]      control-plane dashboard: meta, queue, runner counts
  doctor                control-plane + fleet health check

Auth shortcuts (aliases):
  login | logout | whoami | install
  token list|create|delete       manage API tokens (--projects, --labels)
  config list|get|set|unset|path local CLI config (~/.config/kern/config.json)

Projects:
  projects list|show|create|sync|workflows
  migrate <owner/name|github-url>        import workflows into a new/existing project

Runs:
  runs list [--status S] [--branch B] [--cause C] [--pr N] [--workflow W] [--limit N]
  runs show <run-id>
  runs trigger [<project>] <workflow> [--ref R] [-f k=v] [--each-runner] [--runners a,b]
               [--split N | --split-auto] [--watch|-w] [--open]
  runs cancel|approve|rerun <run-id> [--failed | --job K]
  runs logs <run-id> [--job KEY] [--step N] [--follow|-f] [--out DIR]
  runs wait <run-id> [--interval 5s] [--timeout 30m]   block until the run concludes
  runs remediate <job-id> --step N [--dismiss]         approve or dismiss autoheal fix
  logs <run-id> [-f]                      alias for runs logs
  diagnose <run-id> [--tail N] [--job K] [-i]  pasteable failure bundle

Remote offload:
  exec <workflow> [--dir DIR] [--pull DIR] [--ref R] [--sha S]
                        bundle the working tree, run on the fleet, stream logs
  ssh <job-id|run-id> [--job KEY] [-c CMD]
                        live shell into a running job's container
  testbox [workflow] [--job KEY] [--env-file F]
                        run a workflow job locally — no push, no server

Settings:
  settings get [--project S]
  settings set <key> <value> [--project S]
                        keys: self_heal, auto_concurrency, max_transient_retries,
                              public_ci, auto_update, runner_autostart, disk_alert_pct,
                              toolset, fork_gate, pr_trust, max_concurrent_jobs,
                              prime_workflow, prime_auto

Runners:
  runners list
  runners drain|undrain|self-update|wake|delete <id|name|--all|--label L|--outdated|--offline>
  runners install [--name N] [--labels a,b] [--ttl MIN]
                        enroll this host and install the systemd user unit
  runners enroll [--name N] [--ttl MIN]   mint a one-time join token

Artifacts / cache / secrets:
  artifacts list [--run ID] | download|delete <id> [--out FILE]
  cache list|delete <id>
  secrets list|set|delete [--project S] <name>
  vars list|set|delete [--project S] <name> [value]   project config vars (vars context)

Environments:
  environments list|set|delete [--project S]
  deployments list|approve|reject [--project S]

Advisory:
  split --pattern GLOB --shards N [--root DIR]   suggest strategy.split and file buckets
  optimize [dir|file]          audit workflows for speed, caching, and runner fit
  lint [dir ...]               offline compile of .github/workflows — will KERN run it?
  hooks install [--dry-run] [--force]             pre-push hook that runs kern lint
  completion <bash|zsh|fish>  print a shell completion script

Globals: -u/--url (--server), -c/--context, -p/--project, --token, -j/--json.
Env vars: KERN_URL (or KERN_SERVER), KERN_CONTEXT, KERN_TOKEN, KERN_PROJECT.
`

// commandUsage returns focused help for `kern <cmd> --help`; unknown or
// bare commands fall back to the full usage.
func commandUsage(name string) string {
	switch name {
	case "auth", "login", "logout", "install":
		return `kern auth — authenticate with one or more KERN servers

  auth login [URL] [--url URL] [--context NAME] [--days N] [--name LABEL]
             [--web | --no-browser | --with-token | --email E --password P]
             Interactive default: choose or enter server URL, open browser to authorize.
  auth status              show active context, server reachability, account, and token
  auth switch [NAME]       switch active server context
  auth logout [--all]      revoke saved token and clear local credentials
  auth token list|create|delete

Shortcuts: kern login, kern logout, kern whoami, kern token.
`
	case "context", "ctx":
		return `kern context — manage multiple KERN server profiles (alias: ctx)

  context list                       list saved server contexts (* = active)
  context use [NAME]                 switch active context (interactive if omitted)
  context current                    show active context and server URL
  context set-url [NAME] <URL>       change the server URL for a context
  context rm <NAME>                  delete a saved context

Use -c/--context <name> on any command to target a context without switching.
`
	case "open", "web":
		return `kern open — open the active KERN web dashboard in your browser (alias: web)

  open                     open overview dashboard
  open runs [ID]           open runs list or a specific run
  open projects [SLUG]     open projects list or project detail
  open runners             open fleet runners page
  open usage|secrets|cache|artifacts|environments|webhooks|settings
  open --print             print the URL without opening a browser
`
	case "runs", "logs":
		return `kern runs — workflow runs

  runs list [--status S[,S..]] [--limit N] [--offset N] [--project S]
            [--branch|--ref REF] [--cause|--classification C] [--pr N] [--workflow W]
  runs show <run-id>                  run + job/step detail (short ids work)
  runs wait <run-id> [--interval 5s] [--timeout 30m]
  runs trigger [<project>] <workflow> [--event E] [--ref R] [--sha S]
            [-f key=value] [--inputs '{"k":"v"}'] [--each-runner] [--runners a,b]
            [--split N | --split-auto] [--watch|-w] [--open]
  runs cancel|approve|rerun <run-id> [--failed | --job KEY]
  runs logs <run-id> [--job KEY] [--step N] [--follow|-f] [--out DIR]
  runs remediate <job-id> --step N [--dismiss]   approve or dismiss autoheal fix
  logs <run-id> [-f]                   alias for runs logs
  diagnose <run-id> [--tail N] [--job K] [-i]
`
	case "config":
		return `kern config — local CLI config (~/.config/kern/config.json)

  config list
  config get <server|url|token|project|context>
  config set <server|url|token|project|context> <value>
  config unset <server|url|token|project>
  config path
`
	case "settings":
		return `kern settings — org and project automation policy

  settings get [--project S]
  settings set <key> <value> [--project S]

Org keys:     self_heal (off|auto|approve), auto_concurrency (on|off),
              max_transient_retries (0-10), public_ci (on|off), auto_update (on|off),
              runner_autostart (on|off), disk_alert_pct (0-100), toolset (csv)
Project keys: self_heal (inherit|off|auto|approve), auto_concurrency (inherit|on|off),
              max_transient_retries (inherit|0-10), fork_gate (off|all|first-time),
              pr_trust (off|trusted), max_concurrent_jobs (N),
              prime_workflow (path), prime_auto (on|off)
`
	case "projects":
		return `kern projects — repositories and workflows

  projects list|show <slug>
  projects create <owner/name|github-url>
  projects sync <slug>                re-read workflows from the repo
  projects workflows <slug>           list discovered workflows
  migrate <owner/name|github-url>     import workflows into a project
`
	case "runners":
		return `kern runners — self-hosted runner fleet

  runners list                        online/offline, load, disk, version
  runners drain <id>                  stop claiming new jobs
  runners delete <id>
`
	case "secrets", "vars":
		return `kern ` + name + ` — project configuration

  ` + name + ` list [--project S]
  ` + name + ` set [--project S] <name> [value]   value from stdin when omitted
  ` + name + ` delete [--project S] <name>
`
	case "artifacts", "cache":
		return `kern ` + name + ` — run artifacts / action cache

  artifacts list [--run ID]
  cache list
  artifacts download <id> [--out FILE]
  artifacts delete <id>
  cache delete <id>
`
	case "token":
		return `kern token — API tokens

  token list
  token create --name NAME [--scopes read,write] [--projects SLUG] [--labels LABEL] [--days N] [--concurrency N]
  token delete <id>
`
	case "environments":
		return `kern environments — deployment protection rules

  environments list [--project S]
  environments set <name> [--protected true|false] [--approvals N]
                   [--reviewers a@b,c@d] [--wait MIN]
                   [--branches all|PATTERN,PATTERN] [--project S]
  environments delete <name> [--project S]
`
	case "deployments":
		return `kern deployments — gated job approvals

  deployments list [--project S]
  deployments approve|reject <job-id>
`
	case "exec":
		return `kern exec <workflow> [--dir DIR] — run a workflow on the fleet from a local tree
`
	case "ssh":
		return `kern ssh <job-id|run-id> [--job KEY] [-c CMD] — live shell into a running job's container
`
	case "testbox":
		return `kern testbox [workflow] [--job KEY] [--dir DIR] [--env-file F] [--with-needs] — run a job locally in the docker executor; --with-needs runs the transitive needs: DAG in dependency order first
`
	case "doctor":
		return `kern doctor — check CLI config, auth and control-plane reachability
`
	case "status":
		return `kern status [--watch] — control-plane dashboard
`
	case "hooks":
		return `kern hooks install [--dry-run] [--force] — pre-push hook that runs kern lint
`
	case "lint":
		return `kern lint [dir ...] — offline compile of .github/workflows
`
	case "split":
		return `kern split --pattern GLOB --shards N [--root DIR]
`
	case "optimize":
		return `kern optimize [dir] — ranked CI/CD suggestions from compiled workflows + live fleet
`
	case "migrate":
		return `kern migrate <owner/name|github-url> [--project S]
`
	case "completion":
		return `kern completion <bash|zsh|fish> — print a shell completion script
`
	}
	return usage
}

// cli carries global options resolved from flags, env and the config file.
type cli struct {
	cfg     configFile
	context string
	server  string
	token   string
	project string
	json    bool
	out     *client
}

// extractGlobals pulls global flags out of argv wherever they appear and
// returns the remaining args.
func extractGlobals(args []string, c *cli) []string {
	var rest []string
	takesValue := map[string]func(string){
		"server":  func(v string) { c.server = v },
		"url":     func(v string) { c.server = v },
		"context": func(v string) { c.context = v },
		"token":   func(v string) { c.token = v },
		"project": func(v string) { c.project = v },
	}
	shortValue := map[string]string{
		"-u": "url",
		"-c": "context",
		"-p": "project",
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Everything after -- belongs to the subcommand (remote flags,
			// curl payloads, positional args) — never consume it as globals.
			rest = append(rest, args[i:]...)
			break
		}
		if a == "--json" || a == "-j" {
			c.json = true
			continue
		}
		if longKey, ok := shortValue[a]; ok {
			// Keep -c untouched for `kern ssh -c CMD`!
			if a == "-c" && len(rest) > 0 && rest[0] == "ssh" {
				rest = append(rest, a)
				continue
			}
			if i+1 >= len(args) {
				fatalf("kern: %s requires a value", a)
			}
			i++
			takesValue[longKey](args[i])
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		set, ok := takesValue[key]
		if !ok {
			rest = append(rest, a)
			continue
		}
		if hasVal {
			set(val)
			continue
		}
		if i+1 >= len(args) {
			fatalf("kern: --%s requires a value", key)
		}
		i++
		set(args[i])
	}
	return rest
}

func main() {
	c := &cli{cfg: loadConfig()}
	args := extractGlobals(os.Args[1:], c)
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return
	}
	code := dispatch(c, args)
	os.Exit(code)
}

// dispatch routes the command name to its handler. Handlers return an exit code.
func dispatch(c *cli, args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name, rest := args[0], args[1:]
	// `kern <cmd> ... --help` prints the command's usage instead of dying in
	// flag parsing with "unknown flag" — the discoverability papercut from
	// #179. Checked before dispatch so every subcommand inherits it.
	for _, a := range rest {
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" {
			fmt.Print(commandUsage(name))
			return 0
		}
	}
	var err error
	switch name {
	case "version", "--version", "-v":
		c.orJSON(map[string]any{"version": version.Version}, func() {
			fmt.Println("kern " + version.Version)
		})
	case "install":
		ictx, istop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer istop()
		err = c.cmdInstall(ictx, rest)
	case "auth":
		actx, astop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer astop()
		err = c.cmdAuth(actx, rest)
	case "context", "ctx":
		err = c.cmdContext(rest)
	case "open", "web":
		err = c.cmdOpen(rest)
	case "status":
		// --watch refreshes until Ctrl-C; give status its own signal context
		// instead of the default request budget.
		sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = c.cmdStatus(sctx, rest)
	case "login":
		lctx, lstop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer lstop()
		err = c.cmdLogin(lctx, rest)
	case "logout":
		err = c.cmdLogout(ctx, rest)
	case "whoami":
		err = c.cmdWhoami(ctx)
	case "token":
		err = c.cmdToken(ctx, rest)
	case "config":
		err = c.cmdConfig(rest)
	case "projects":
		err = c.cmdProjects(ctx, rest)
	case "runs", "logs":
		// wait/logs -f can run for the full --timeout/--follow budget.
		rctx, rstop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer rstop()
		if name == "logs" {
			err = c.cmdRuns(rctx, append([]string{"logs"}, rest...))
		} else {
			err = c.cmdRuns(rctx, rest)
		}
	case "diagnose":
		err = c.cmdDiagnose(ctx, rest)
	case "doctor":
		err = c.cmdDoctor(ctx, rest)
	case "migrate":
		err = c.cmdMigrate(ctx, rest)
	case "runners":
		err = c.cmdRunners(ctx, rest)
	case "artifacts":
		// downloads can be large; outlive the default request budget.
		actx, astop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer astop()
		err = c.cmdArtifacts(actx, rest)
	case "cache":
		err = c.cmdCache(ctx, rest)
	case "secrets":
		err = c.cmdSecrets(ctx, rest)
	case "vars":
		err = c.cmdVars(ctx, rest)
	case "environments":
		err = c.cmdEnvironments(ctx, rest)
	case "deployments":
		err = c.cmdDeployments(ctx, rest)
	case "settings":
		err = c.cmdSettings(ctx, rest)
	case "exec":
		// Runs can take far longer than the default request budget; give exec
		// its own context bounded only by Ctrl-C.
		ectx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = c.cmdExec(ectx, rest)
	case "ssh":
		// Same long-session budget as exec — a shell into a live job can
		// outlast any request timeout.
		sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = c.cmdSSH(sctx, rest)
	case "testbox":
		// Local job execution needs no auth; same Ctrl-C budget as exec.
		tctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = c.cmdTestbox(tctx, rest)
	case "split":
		err = c.cmdSplit(rest)
	case "optimize":
		err = c.cmdOptimize(rest)
	case "lint":
		err = c.cmdLint(rest)
	case "hooks":
		err = c.cmdHooks(rest)
	case "completion":
		err = c.cmdCompletion(rest)
	case "init":
		err = c.cmdInit(ctx, rest)
	case "top":
		err = c.cmdTop(ctx, rest)
	case "upgrade":
		uctx, ustop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer ustop()
		err = c.cmdUpgrade(uctx, rest)
	default:
		if hint := suggestCommand(name, topLevelCommands); hint != "" {
			err = fmt.Errorf("unknown command %q — did you mean %q? (see `kern help`)", name, hint)
		} else {
			err = fmt.Errorf("unknown command %q — see `kern help`", name)
		}
	}
	if err != nil {
		var ec exitCodeError
		if errors.As(err, &ec) {
			if ec.msg != "" {
				fmt.Fprintln(os.Stderr, "kern:", ec.msg)
			}
			return ec.code
		}
		fmt.Fprintln(os.Stderr, "kern:", err)
		return 1
	}
	return 0
}

// exitCodeError carries a process exit code up to dispatch so deferred cleanup
// runs instead of being bypassed by os.Exit.
type exitCodeError struct {
	code int
	msg  string
}

func (e exitCodeError) Error() string { return e.msg }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "kern: "+format+"\n", args...)
	os.Exit(2)
}
