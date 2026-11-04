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

Auth:
  login                 exchange email+password for an API token and save it
  logout                revoke the saved token and clear local credentials
  whoami                show the authenticated user
  token list|create|delete       manage API tokens

Config:
  config list|get|set|unset      local CLI config (~/.config/kern/config.json)

Projects:
  projects list|show|create|sync|workflows

Runs:
  runs list [--status S] [--limit N]
  runs show <run-id>
  runs trigger [<project>] <workflow-id|path|name> [--event E] [--ref R] [--sha S]
  runs cancel|rerun <run-id>
  runs logs <run-id> [--job KEY] [--step N] [--follow]
  runs remediate <job-id> --step N        approve a remediation proposal

Remote offload:
  exec <workflow> [--dir DIR] [--pull DIR] [--ref R] [--sha S]
                        bundle the working tree, run on the fleet, stream logs

Settings:
  settings get|set self_heal off|auto|approve [--project S]

Runners:
  runners list
  runners drain|undrain|delete <runner-id>
  runners enroll [--name N] [--ttl MIN]   mint a one-time join token

Artifacts / cache / secrets:
  artifacts list|download <id> [--out FILE]
  cache list|delete <id>
  secrets list|set|delete [--project S] <name>

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

Globals: --server, --token, --project override config and KERN_SERVER,
KERN_TOKEN, KERN_PROJECT env vars. --json prints raw JSON.
`

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
	var err error
	switch name {
	case "version", "--version", "-v":
		fmt.Println("kern " + version.Version)
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
	case "runners":
		err = c.cmdRunners(ctx, rest)
	case "artifacts":
		err = c.cmdArtifacts(ctx, rest)
	case "cache":
		err = c.cmdCache(ctx, rest)
	case "secrets":
		err = c.cmdSecrets(ctx, rest)
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
	case "split":
		err = c.cmdSplit(rest)
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
