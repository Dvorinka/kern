// kern-runner is the control-plane runner agent. It registers with a join token,
// then long-polls for jobs and executes each one in an ephemeral Docker container.
// All connectivity is outbound from the runner host.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/runner"
	"github.com/dvorinka/kern/internal/telemetry"
)

const usage = `kern-runner — KERN runner agent

Usage:
  kern-runner --control https://kern.example.com --join-token <token> [options]

Flags (each has an environment fallback, listed with it):
  --control URL         control plane base URL (KERN_CONTROL_URL)
  --join-token token    runner join token (KERN_RUNNER_JOIN_TOKEN)
  --name name           runner name, defaults to hostname (KERN_RUNNER_NAME)
  --labels a,b,c        extra matching labels (KERN_RUNNER_LABELS)
  --concurrency n       concurrent jobs on this host (KERN_RUNNER_CONCURRENCY)
  --work-root dir       scratch directory (KERN_WORK_ROOT, default /tmp/kern-runner)
  --env level           development | production (KERN_ENV)
`

type options struct {
	control     string
	joinToken   string
	name        string
	labels      []string
	env         string
	concurrency int
	workRoot    string
}

// parse takes both flag args and environment variables; flags win.
func parse(argv []string) (options, bool) {
	var o options
	values := map[string]*string{
		"control": &o.control, "join-token": &o.joinToken, "name": &o.name,
		"env": &o.env, "work-root": &o.workRoot,
	}
	counts := map[string]*int{
		"concurrency": &o.concurrency,
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "-h" || arg == "--help" {
			fmt.Print(usage)
			return o, false
		}
		if !strings.HasPrefix(arg, "--") {
			fmt.Fprintf(os.Stderr, "kern-runner: unexpected argument %q\n", arg)
			return options{}, false
		}
		key, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !hasValue {
			if i+1 < len(argv) {
				i++
				value = argv[i]
			} else {
				fmt.Fprintf(os.Stderr, "kern-runner: --%s requires a value\n", key)
				return options{}, false
			}
		}
		if p, found := values[key]; found {
			*p = value
			continue
		}
		if n, found := counts[key]; found {
			*n, _ = strconv.Atoi(value)
			continue
		}
		fmt.Fprintf(os.Stderr, "kern-runner: unknown flag --%s\n", key)
		return options{}, false
	}
	if o.control == "" && os.Getenv("KERN_CONTROL_URL") != "" {
		o.control = os.Getenv("KERN_CONTROL_URL")
	}
	if o.joinToken == "" && os.Getenv("KERN_RUNNER_JOIN_TOKEN") != "" {
		o.joinToken = os.Getenv("KERN_RUNNER_JOIN_TOKEN")
	}
	if o.name == "" {
		o.name = os.Getenv("KERN_RUNNER_NAME")
	}
	if o.env == "" {
		o.env = os.Getenv("KERN_ENV")
	}
	if o.env == "" {
		o.env = "production"
	}
	if o.workRoot == "" {
		o.workRoot = os.Getenv("KERN_WORK_ROOT")
	}
	if l := os.Getenv("KERN_RUNNER_LABELS"); l != "" {
		o.labels = splitLabels(l)
	}
	if o.concurrency == 0 {
		if v := os.Getenv("KERN_RUNNER_CONCURRENCY"); v != "" {
			o.concurrency, _ = strconv.Atoi(v)
		}
	}
	return o, true
}

func splitLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func main() {
	o, ok := parse(os.Args[1:])
	if !ok {
		return
	}
	os.Exit(run(o))
}

func run(o options) int {
	if o.control == "" {
		fmt.Fprintln(os.Stderr, "kern-runner: --control or KERN_CONTROL_URL is required")
		return 2
	}
	if o.joinToken == "" {
		fmt.Fprintln(os.Stderr, "kern-runner: --join-token or KERN_RUNNER_JOIN_TOKEN is required")
		return 2
	}

	log, err := telemetry.NewLogger(o.env, "runner")
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner: logger:", err)
		return 2
	}
	defer log.Sync()

	cfg := runner.Config{
		ControlURL:    o.control,
		JoinToken:     o.joinToken,
		Name:          o.name,
		Labels:        o.labels,
		WorkRoot:      o.workRoot,
		Concurrency:   maxInt(o.concurrency, 1),
		JobCPUs:       os.Getenv("KERN_JOB_CPUS"),
		JobMemory:     os.Getenv("KERN_JOB_MEMORY"),
		LeaseDuration: 90 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent := runner.New(cfg, log)
	err = agent.Run(ctx) // blocks until ctx is cancelled; claims retry internally
	if err != nil && ctx.Err() == nil {
		log.Error("runner exited", zap.Error(err))
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
