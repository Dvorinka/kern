// kern-runner is the control-plane runner agent. It registers with a join token,
// then long-polls for jobs and executes each one in an ephemeral Docker container.
// All connectivity is outbound from the runner host.
//
// Two entry modes:
//
//	kern-runner --control URL --join-token T   register (and re-register on boot)
//	kern-runner --config envfile               run on credentials written by join
//	kern-runner join --control URL --token T   one-time enroll → env file + unit
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
  kern-runner --config <env-file> [options]
  kern-runner join --control <url> --token <enroll-token> [options]

Flags (each has an environment fallback, listed with it):
  --control URL         control plane base URL (KERN_CONTROL_URL)
  --join-token token    shared join token or one-time enroll token (KERN_RUNNER_JOIN_TOKEN)
  --token token         existing runner credential, skips registration (KERN_RUNNER_TOKEN)
  --config path         env file written by 'join'; loaded like environment (KERN_RUNNER_CONFIG)
  --name name           runner name, defaults to hostname (KERN_RUNNER_NAME)
  --labels a,b,c        extra matching labels (KERN_RUNNER_LABELS)
  --concurrency n       concurrent jobs on this host (KERN_RUNNER_CONCURRENCY, default one per CPU)
  --work-root dir       scratch directory (KERN_WORK_ROOT, default /tmp/kern-runner)
  --env level           development | production (KERN_ENV)

Environment only:
  KERN_RUNNER_LOAD_GATE     pause claims while loadavg1 > n * cpu count (0=off)
  KERN_RUNNER_IMAGE_MAX_AGE GC pulled job images unused this long (default 168h, 0=off)
  KERN_RUNNER_PREPULL       comma-separated images to keep pulled (job pulls hit local cache)
  KERN_RUNNER_DISK_MIN_FREE_PCT  prune docker + pause claims when work-root fs free drops below % (default 10, 0=off)
  KERN_RUNNER_DISK_CRITICAL_FREE_PCT  escalate reclaim under critical pressure (default 5, 0=off)
  KERN_RUNNER_DISK_CRITICAL_FLOOR_BYTES  absolute floor that also marks critical (default 2GiB, 0=off)
  KERN_RUNNER_MEM_MIN_FREE_BYTES  pause claims while host MemAvailable below floor (default 1GiB, 0=off)
  KERN_RUNNER_CACHE_MAX_BYTES    cap the persistent _cache tree, evicting oldest first (default 8GiB, 0=off)
  KERN_ACTIONS_GIT_BASE     git base URL for remote actions (default: the run's
                            repo host, or https://github.com)
  KERN_ACTIONS_PRESEED      comma-separated owner/repo@ref actions warmed into
                            the _actions cache (default: built-in fleet set,
                            off=disabled)
  KERN_JOB_CPUS / KERN_JOB_MEMORY   per-job container limits (unset CPUs share the host)
  KERN_RUNNER_EXECUTOR    docker|host — execution substrate (default: docker on
                          linux, host on darwin; host runs steps as local
                          processes and only claims trusted-tier jobs)
  KERN_RUNNER_UPDATE=1      replace this container when the control plane
                            offers a signed ghcr.io/dvorinka/kern-runner image.
                            Leave unset on the compose runner; the host updater
                            rebuilds that one.
  KERN_RUNNER_EPHEMERAL=1   JIT mode: deregister and exit after this long with
                            no active jobs (KERN_RUNNER_EPHEMERAL_IDLE,
                            default 2m). Pair with POST /api/v1/runners.

join flags:
  --env-file path       credential file destination (default ~/.config/kern-runner/env)
  --no-unit             skip writing the systemd user unit
`

type options struct {
	control     string
	joinToken   string
	token       string
	config      string
	name        string
	labels      []string
	env         string
	concurrency int
	workRoot    string
	envFile     string
	noUnit      bool
}

// loadConfigFile pre-scans argv for --config and applies KEY=VALUE lines to the
// environment without overriding anything already set. It must run before
// parse() so file values participate in the normal env fallbacks.
func loadConfigFile(argv []string) {
	path := ""
	for i, arg := range argv {
		if arg == "--config" && i+1 < len(argv) {
			path = argv[i+1]
		} else if strings.HasPrefix(arg, "--config=") {
			path = strings.TrimPrefix(arg, "--config=")
		}
	}
	if path == "" {
		path = os.Getenv("KERN_RUNNER_CONFIG")
	}
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kern-runner: read config %s: %v\n", path, err)
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || strings.HasPrefix(key, "#") || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, strings.Trim(value, `"`))
	}
}

// parse takes both flag args and environment variables; flags win.
func parse(argv []string) (options, bool) {
	var o options
	values := map[string]*string{
		"control": &o.control, "join-token": &o.joinToken, "token": &o.token,
		"config": &o.config, "name": &o.name, "env": &o.env,
		"work-root": &o.workRoot, "env-file": &o.envFile,
	}
	counts := map[string]*int{
		"concurrency": &o.concurrency,
	}
	bools := map[string]*bool{
		"no-unit": &o.noUnit,
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
		if b, found := bools[key]; found {
			*b = !hasValue || value == "true" || value == "1"
			continue
		}
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
	if o.token == "" {
		o.token = os.Getenv("KERN_RUNNER_TOKEN")
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
	if o.envFile == "" {
		o.envFile = os.Getenv("KERN_RUNNER_ENV_FILE")
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
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "join" {
		loadConfigFile(args[1:])
		o, ok := parse(args[1:])
		if !ok {
			return
		}
		os.Exit(runJoin(o))
	}
	if len(args) > 0 && args[0] == "egress-proxy" {
		os.Exit(runEgressProxy(args[1:]))
	}
	if len(args) > 0 && args[0] == "port-forward" {
		os.Exit(runPortForward(args[1:]))
	}
	loadConfigFile(args)
	o, ok := parse(args)
	if !ok {
		return
	}
	os.Exit(run(o))
}

// runEgressProxy is the internal entrypoint the executor packages into the
// scratch egress image. Not a user-facing mode — fork jobs reach it by
// container alias on their isolated network.
func runEgressProxy(args []string) int {
	listen := ":3128"
	var allow []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--listen" && i+1 < len(args):
			i++
			listen = args[i]
		case strings.HasPrefix(args[i], "--listen="):
			listen = strings.TrimPrefix(args[i], "--listen=")
		case args[i] == "--allow" && i+1 < len(args):
			i++
			allow = append(allow, strings.Split(args[i], ",")...)
		case strings.HasPrefix(args[i], "--allow="):
			allow = append(allow, strings.Split(strings.TrimPrefix(args[i], "--allow="), ",")...)
		}
	}
	if err := runner.RunEgressProxy(listen, allow); err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner egress-proxy:", err)
		return 1
	}
	return 0
}

// runPortForward is the internal entrypoint the executor packages into the
// kern-egress image for service host:container port remaps on bridge jobs.
// Not a user-facing mode — the forwarder lives inside the job's netns.
func runPortForward(args []string) int {
	var listen, to string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--listen" && i+1 < len(args):
			i++
			listen = args[i]
		case strings.HasPrefix(args[i], "--listen="):
			listen = strings.TrimPrefix(args[i], "--listen=")
		case args[i] == "--to" && i+1 < len(args):
			i++
			to = args[i]
		case strings.HasPrefix(args[i], "--to="):
			to = strings.TrimPrefix(args[i], "--to=")
		}
	}
	if err := runner.RunPortForward(listen, to); err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner port-forward:", err)
		return 1
	}
	return 0
}

func agentConfig(o options) runner.Config {
	workers, cpus := runner.SizeWorkers(runtime.NumCPU(), o.concurrency, os.Getenv("KERN_JOB_CPUS"))
	cfg := runner.Config{
		ControlURL:     o.control,
		JoinToken:      o.joinToken,
		Name:           o.name,
		Labels:         o.labels,
		WorkRoot:       o.workRoot,
		Concurrency:    workers,
		JobCPUs:        cpus,
		JobMemory:      os.Getenv("KERN_JOB_MEMORY"),
		ActionsGitBase: os.Getenv("KERN_ACTIONS_GIT_BASE"),
		SelfUpdate:     os.Getenv("KERN_RUNNER_UPDATE") == "1",
		Ephemeral: os.Getenv("KERN_RUNNER_EPHEMERAL") == "1" ||
			strings.EqualFold(os.Getenv("KERN_RUNNER_EPHEMERAL"), "true"),
		LeaseDuration:   90 * time.Second,
		EgressMode:      os.Getenv("KERN_RUNNER_EGRESS"),
		EgressAllowlist: os.Getenv("KERN_EGRESS_ALLOWLIST"),
	}
	if v := os.Getenv("KERN_RUNNER_EPHEMERAL_IDLE"); v != "" {
		cfg.EphemeralIdle, _ = time.ParseDuration(v)
	}
	if v := os.Getenv("KERN_RUNNER_LOAD_GATE"); v != "" {
		cfg.MaxLoadPerCPU, _ = strconv.ParseFloat(v, 64)
	}
	if v := os.Getenv("KERN_RUNNER_IMAGE_MAX_AGE"); v != "" {
		cfg.ImageMaxAge, _ = time.ParseDuration(v)
	} else {
		cfg.ImageMaxAge = 7 * 24 * time.Hour
	}
	if v := os.Getenv("KERN_RUNNER_PREPULL"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.Prepull = append(cfg.Prepull, p)
			}
		}
	}
	// Unset seeds the built-in fleet set; an explicit list replaces it;
	// "off" disables warming entirely.
	if v := os.Getenv("KERN_ACTIONS_PRESEED"); v == "" {
		cfg.ActionsPreseed = runner.DefaultActionsPreseed
	} else if !strings.EqualFold(v, "off") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.ActionsPreseed = append(cfg.ActionsPreseed, p)
			}
		}
	}
	if v := os.Getenv("KERN_RUNNER_DISK_MIN_FREE_PCT"); v != "" {
		cfg.DiskMinFreePct, _ = strconv.ParseFloat(v, 64)
	} else {
		cfg.DiskMinFreePct = 10
	}
	if v := os.Getenv("KERN_RUNNER_DISK_CRITICAL_FREE_PCT"); v != "" {
		cfg.DiskCriticalFreePct, _ = strconv.ParseFloat(v, 64)
	} else {
		cfg.DiskCriticalFreePct = 5
	}
	if v := os.Getenv("KERN_RUNNER_DISK_CRITICAL_FLOOR_BYTES"); v != "" {
		cfg.DiskCriticalFloorBytes, _ = strconv.ParseInt(v, 10, 64)
	} else {
		cfg.DiskCriticalFloorBytes = 2 << 30
	}
	if v := os.Getenv("KERN_RUNNER_MEM_MIN_FREE_BYTES"); v != "" {
		cfg.MemMinFreeBytes, _ = strconv.ParseInt(v, 10, 64)
	} else {
		cfg.MemMinFreeBytes = 1 << 30
	}
	if v := os.Getenv("KERN_JOB_DISK"); v != "" {
		cfg.JobDiskBytes = parseHumanBytes(v)
	}
	if v := os.Getenv("KERN_BUILDX_KEEP"); v != "" {
		cfg.BuildxKeepBytes = parseHumanBytes(v)
	}
	if v := os.Getenv("KERN_STEP_SILENCE_TIMEOUT"); v != "" {
		cfg.StepSilenceSeconds, _ = strconv.Atoi(v)
	} else {
		cfg.StepSilenceSeconds = 600
	}
	cfg.Executor = os.Getenv("KERN_RUNNER_EXECUTOR")
	if cfg.Executor == "" {
		// Host executors are the only substrate on platforms without a
		// compatible container runtime: darwin has no docker image contract
		// and windows job containers would need LCOW.
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			cfg.Executor = "host"
		} else {
			cfg.Executor = "docker"
		}
	}
	if cfg.Executor != "docker" && cfg.Executor != "host" {
		fmt.Fprintf(os.Stderr, "kern-runner: KERN_RUNNER_EXECUTOR must be docker or host, got %q\n", cfg.Executor)
		os.Exit(2)
	}
	if v := os.Getenv("KERN_RUNNER_CACHE_MAX_BYTES"); v != "" {
		cfg.CacheMaxBytes, _ = strconv.ParseInt(v, 10, 64)
	} else {
		cfg.CacheMaxBytes = 8 << 30
	}
	return cfg
}

func run(o options) int {
	if o.control == "" {
		fmt.Fprintln(os.Stderr, "kern-runner: --control or KERN_CONTROL_URL is required")
		return 2
	}
	if o.joinToken == "" && o.token == "" {
		fmt.Fprintln(os.Stderr, "kern-runner: --join-token, --token or --config is required")
		return 2
	}

	log, err := telemetry.NewLogger(o.env, "runner")
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner: logger:", err)
		return 2
	}
	defer log.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent := runner.New(agentConfig(o), log)
	if o.token != "" {
		agent.SetToken(o.token)
	}
	err = agent.Run(ctx) // blocks until ctx is cancelled; claims retry internally
	if err != nil && ctx.Err() == nil {
		log.Error("runner exited", zap.Error(err))
		return 1
	}
	return 0
}

// runJoin exchanges a one-time enroll token for a runner credential, persists
// it to an env file and writes a systemd user unit so the agent survives reboot.
func runJoin(o options) int {
	if o.control == "" {
		fmt.Fprintln(os.Stderr, "kern-runner join: --control is required")
		return 2
	}
	token := o.joinToken
	if token == "" {
		token = o.token
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "kern-runner join: --token (enroll token) is required")
		return 2
	}
	log, err := telemetry.NewLogger(o.env, "runner")
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner: logger:", err)
		return 2
	}
	defer log.Sync()

	cfg := agentConfig(o)
	cfg.JoinToken = token
	agent := runner.New(cfg, log)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agent.Register(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner join:", err)
		return 1
	}

	name := o.name
	if name == "" {
		name, _ = os.Hostname()
	}
	envFile := o.envFile
	if envFile == "" {
		home, _ := os.UserHomeDir()
		envFile = filepath.Join(home, ".config", "kern-runner", "env")
	}
	if err := writeEnvFile(envFile, map[string]string{
		"KERN_CONTROL_URL":        o.control,
		"KERN_RUNNER_TOKEN":       agent.Token(),
		"KERN_RUNNER_NAME":        name,
		"KERN_RUNNER_LABELS":      strings.Join(o.labels, ","),
		"KERN_WORK_ROOT":          cfg.WorkRoot,
		"KERN_RUNNER_CONCURRENCY": strconv.Itoa(cfg.Concurrency),
	}); err != nil {
		fmt.Fprintln(os.Stderr, "kern-runner join:", err)
		return 1
	}
	fmt.Printf("registered; credentials written to %s\n", envFile)

	if !o.noUnit {
		exe, _ := os.Executable()
		unit, err := writeUserUnit(exe, envFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kern-runner join: systemd unit:", err)
			return 1
		}
		fmt.Printf("systemd user unit written to %s\n", unit)
		fmt.Println("start it with: systemctl --user daemon-reload && systemctl --user enable --now kern-runner")
	} else {
		fmt.Printf("start the agent with: kern-runner --config %s\n", envFile)
	}
	return 0
}

func writeEnvFile(path string, kv map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var b strings.Builder
	for _, key := range []string{
		"KERN_CONTROL_URL", "KERN_RUNNER_TOKEN", "KERN_RUNNER_NAME",
		"KERN_RUNNER_LABELS", "KERN_WORK_ROOT", "KERN_RUNNER_CONCURRENCY",
	} {
		v := kv[key]
		if strings.ContainsAny(v, " \t") {
			v = `"` + v + `"`
		}
		fmt.Fprintf(&b, "%s=%s\n", key, v)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

const unitTemplate = `[Unit]
Description=KERN runner agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
EnvironmentFile=%s
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

func writeUserUnit(exe, envFile string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "kern-runner.service")
	body := fmt.Sprintf(unitTemplate, envFile, exe)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// parseHumanBytes reads "20g"/"512m"/"1024k" style sizes (case-insensitive,
// binary units) — the runner-side mirror of metrics.go's parser, kept here
// so cmd stays free of internal helpers.
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	n, _ := strconv.ParseFloat(s[:i], 64)
	mult := float64(1)
	switch strings.TrimSpace(s[i:]) {
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	case "", "b":
	default:
		return 0
	}
	return int64(n * mult)
}
