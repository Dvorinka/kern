// kern is the operator CLI for the KERN control plane. It speaks the same
// /api/v1 contract as the web app; every command accepts --json for agents.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dvorinka/kern/internal/version"
)

const usage = `kern — KERN control plane CLI

Usage: kern <command> [args] [--server URL] [--token T] [--project P] [--json]

Setup:
  install               first-run wizard — server URL + login, writes the config
  status [--watch]      control-plane dashboard: meta, queue, runner counts

Auth:
  login                 exchange email+password for an API token and save it
  logout                revoke the saved token and clear local credentials
  whoami                show the authenticated user
  token list|create|delete       manage API tokens

Config:
  config list|get|set|unset      local CLI config (~/.config/kern/config.json)

Projects:
  projects list|show|create|sync|workflows
  migrate <owner/name|github-url>        import workflows into a new/existing project

Runs:
  runs list [--status S] [--limit N] [--ref R|BRANCH] [--pr N] [--workflow W]
  runs show <run-id>
  runs trigger [<project>] <workflow-id|path|name> [--event E] [--ref R] [--sha S] [--inputs JSON]
  runs cancel|approve|rerun <run-id>
  runs logs <run-id> [--job KEY] [--step N] [--follow] [--out DIR]
  runs wait <run-id> [--interval 5s] [--timeout 30m]   block until the run concludes
  runs remediate <job-id> --step N        approve a remediation proposal
  logs <run-id> [-f]                      alias for runs logs; -f follows
  diagnose <run-id> [--tail N] [--job K]  pasteable failure bundle
  doctor                                  control-plane + fleet health check

Remote offload:
  exec <workflow> [--dir DIR] [--pull DIR] [--ref R] [--sha S]
                        bundle the working tree, run on the fleet, stream logs
  ssh <job-id|run-id> [--job KEY] [-c CMD]
                        live shell into a running job's container

Settings:
  settings get
  settings set self_heal off|auto|approve [--project S]
  settings set public_ci on|off
  settings set auto_update on|off
  settings set fork_gate off|all|first-time --project S

Runners:
  runners list
  runners drain|undrain|self-update|wake|delete <id|name>
  runners install [--name N] [--labels a,b] [--ttl MIN]
                        enroll this host and install the systemd user unit
  runners enroll [--name N] [--ttl MIN]   mint a one-time join token

Artifacts / cache / secrets:
  artifacts list|download <id> [--out FILE]
  cache list|delete <id>
  secrets list|set|delete [--project S] <name>
  vars list|set|delete [--project S] <name> [value]   project config vars (vars context)

Environments:
  environments list [--project S]
  environments set <name> [--protected true|false] [--approvals N]
                       [--reviewers a@b,c@d] [--wait MIN]
                       [--branches all|PATTERN,PATTERN] [--project S]
  environments delete <name> [--project S]
  deployments list [--project S]
  deployments approve|reject <job-id>

Advisory:
  split --pattern GLOB --shards N [--root DIR]   suggest strategy.split and file buckets
  lint [dir ...]               offline compile of .github/workflows — will KERN run it?
  hooks install [--dry-run] [--force]             pre-push hook that runs kern lint
  completion <bash|zsh|fish>  print a shell completion script

Globals: --server, --token, --project override config and KERN_SERVER,
KERN_TOKEN, KERN_PROJECT env vars. --json prints raw JSON.
`

// commandUsage returns focused help for `kern <cmd> --help`; unknown or
// bare commands fall back to the full usage.
func commandUsage(name string) string {
	switch name {
	case "runs", "logs":
		return `kern runs — workflow runs

  runs list [--status S[,S..]] [--limit N] [--offset N]
            [--project S] [--ref REF|BRANCH] [--pr N] [--workflow NAME|PATH]
  runs show <run-id>                  run + job/step detail (short ids work)
  runs wait <run-id> [--interval 5s] [--timeout 30m]
  runs trigger [<project>] <workflow> [--event E] [--ref R] [--sha S]
            [--inputs '{"k":"v"}']
  runs cancel|approve|rerun <run-id>
  runs logs <run-id> [--job KEY] [--step N] [--follow] [--out DIR]
  runs remediate <job-id> --step N     approve a remediation proposal
  logs <run-id> [-f]                   alias for runs logs
  diagnose <run-id> [--tail N] [--job K]

Run ids accept the full uuid or the unambiguous short prefix printed by
runs list. --ref matches refs/heads/<branch> or a full ref; --pr matches
pull_request runs by number; --workflow matches name or path.
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

  ` + name + ` list
  artifacts download <id> [--out FILE]
  cache delete <id>
`
	case "token":
		return `kern token — API tokens

  token list|create|delete [id]
`
	case "config":
		return `kern config — local CLI config (~/.config/kern/config.json)

  config list|get|set|unset <key> [value]
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
		"token":   func(v string) { c.token = v },
		"project": func(v string) { c.project = v },
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			c.json = true
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
		if a == "--help" || a == "-h" {
			fmt.Print(commandUsage(name))
			return 0
		}
	}
	var err error
	switch name {
	case "version", "--version", "-v":
		fmt.Println("kern " + version.Version)
	case "install":
		err = c.cmdInstall(ctx, rest)
	case "status":
		// --watch refreshes until Ctrl-C; give status its own signal context
		// instead of the default request budget.
		sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = c.cmdStatus(sctx, rest)
	case "login":
		err = c.cmdLogin(ctx, rest)
	case "logout":
		err = c.cmdLogout(ctx)
	case "whoami":
		err = c.cmdWhoami(ctx)
	case "token":
		err = c.cmdToken(ctx, rest)
	case "config":
		err = c.cmdConfig(rest)
	case "projects":
		err = c.cmdProjects(ctx, rest)
	case "runs":
		err = c.cmdRuns(ctx, rest)
	case "logs":
		err = c.cmdRuns(ctx, append([]string{"logs"}, rest...))
	case "diagnose":
		err = c.cmdDiagnose(ctx, rest)
	case "doctor":
		err = c.cmdDoctor(ctx, rest)
	case "migrate":
		err = c.cmdMigrate(ctx, rest)
	case "runners":
		err = c.cmdRunners(ctx, rest)
	case "artifacts":
		err = c.cmdArtifacts(ctx, rest)
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
	default:
		err = fmt.Errorf("unknown command %q — see kern help", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern:", err)
		return 1
	}
	return 0
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "kern: "+format+"\n", args...)
	os.Exit(2)
}
