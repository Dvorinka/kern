package runner

import (
	"regexp"
	"strings"
)

// diagnosis is the structured failure record every failed step reports. It is
// deterministic: same log tail and exit code always produce the same record.
type diagnosis struct {
	Kind        string       `json:"kind"`
	Summary     string       `json:"summary"`
	Detail      string       `json:"detail,omitempty"`
	Suggestion  string       `json:"suggestion"`
	AgentPrompt string       `json:"agent_prompt"`
	Fixable     bool         `json:"fixable"`
	Remediation *remediation `json:"remediation,omitempty"`
}

// remediation carries an allow-listed command plan. Commands are built from
// templates with a sanitized package name — never interpolated from logs.
type remediation struct {
	Fix      []string `json:"fix"`
	Applied  bool     `json:"applied,omitempty"`
	Approved bool     `json:"approved,omitempty"`
	Result   string   `json:"result,omitempty"`
}

var (
	// Matches both `bash: foo: command not found` and a script header form:
	// `/kern/scripts/step-6-9.body.sh: line 3: curl: command not found`.
	cmdNotFoundRe  = regexp.MustCompile(`(?m)(?:^|[\s:])([a-zA-Z0-9._+\-/]+): (?:command )?not found`)
	execNotFoundRe = regexp.MustCompile(`executable file not found`)
	sharedLibRe    = regexp.MustCompile(`error while loading shared libraries: ([a-zA-Z0-9._+\-]+\.so[0-9.]*)`)
	daemonSigRe    = regexp.MustCompile(`(?i)cannot connect to the docker daemon|is the docker daemon running|error during connect|docker\.sock: connect:`)
	pkgNameRe      = regexp.MustCompile(`^[a-zA-Z0-9._+-]{1,64}$`)
)

// unfixablePkg lists missing commands whose package install cannot fix the
// failure: they need a daemon, socket, or privileged service the job container
// does not have. Auto-remediation would burn a retry and fail identically.
var unfixablePkg = map[string]bool{
	"docker": true, "dockerd": true, "containerd": true, "buildkitd": true,
	"podman": true, "buildah": true, "nerdctl": true, "sudo": true,
	"systemctl": true, "service": true,
}

// libPackage maps the shared libraries job images most often lack onto their
// Debian package. Not derivable in general — kept deliberately small.
var libPackage = map[string]string{
	"libatomic.so.1":       "libatomic1",
	"libstdc++.so.6":       "libstdc++6",
	"libgcc_s.so.1":        "libgcc-s1",
	"libz.so.1":            "zlib1g",
	"libssl.so.3":          "libssl3",
	"libcrypto.so.3":       "libssl3",
	"libncursesw.so.6":     "libncursesw6",
	"libncurses.so.6":      "libncurses6",
	"libpython3.11.so.1.0": "libpython3.11",
	"libpython3.12.so.1.0": "libpython3.12",
}

// diagnose maps a step failure onto a rule. Order matters: runner-side errors
// first, then exit-code signatures, then log patterns, then generic.
// jobMemory is the job container's cgroup ceiling (KERN_JOB_MEMORY); it is
// surfaced in OOM diagnoses so the operator sees the limit that was hit.
// daemonAlive probes the host docker daemon lazily — it runs only when the
// log carries a docker-daemon signature, to tell a dead daemon (infra)
// apart from a job image that lacks a working docker client (user).
func diagnose(step StepSpec, exitCode int, logTail, stepErr, jobMemory string, daemonAlive func() bool) *diagnosis {
	d := &diagnosis{}
	fail := func(kind, summary, suggestion string) *diagnosis {
		d.Kind, d.Summary, d.Suggestion = kind, summary, suggestion
		d.AgentPrompt = "A CI step failed. " + summary + " Suggested fix: " + suggestion
		if logTail != "" {
			d.Detail = logTail
			d.AgentPrompt += "\n\nLog tail:\n" + logTail
		}
		return d
	}
	// stepErr is scanned alongside the log tail: runner-side failures
	// (container create, workspace write, image pull) produce no log, so an
	// ENOSPC or daemon error would otherwise fall through to script_failure.
	lower := strings.ToLower(logTail + "\n" + stepErr)
	switch {
	case strings.Contains(stepErr, "timed out"):
		return fail("step_timeout", "Step exceeded timeout-minutes and was killed",
			"raise timeout-minutes or split the work in the step")
	case strings.Contains(stepErr, "not supported"):
		return fail("unsupported_action",
			"The step uses an action KERN does not support yet",
			"replace the uses: step with a run: equivalent; see docs/compatibility.md")
	case daemonSigRe.MatchString(lower) && daemonAlive != nil && !daemonAlive():
		// The daemon probe is the discriminator: the same stderr text
		// appears when a job's own docker CLI has no socket, but then
		// dockerd still answers the runner.
		return fail("daemon_gone",
			"The host docker daemon stopped answering mid-step — infrastructure failure, not the script",
			"the job is requeued onto a healthy runner; check dockerd on the affected runner if this repeats")
	case exitCode == 137 || strings.Contains(lower, "killed\n") ||
		strings.Contains(lower, "out of memory") || strings.Contains(lower, "outofmemoryerror") ||
		strings.Contains(lower, "daemon disappeared") || strings.Contains(lower, "signal: killed"):
		// Gradle's "daemon disappeared unexpectedly" and the Go compiler's
		// "signal: killed" are the cgroup OOM-killer's calling cards — the
		// kernel kills the fattest process in the container's memory group.
		return fail("oom",
			"Process was killed — likely cgroup OOM or SIGKILL (job memory ceiling "+jobMemory+")",
			"raise KERN_JOB_MEMORY on the runner (currently "+jobMemory+") or reduce the step's memory footprint")
	case strings.Contains(lower, "no space left on device"):
		return fail("disk_full", "The job container ran out of disk space",
			"free disk on the runner host or raise the work-root quota")
	case strings.Contains(lower, "resource not accessible by integration"):
		// The per-run GITHUB_TOKEN hit an endpoint its installation lacks:
		// most often `gh workflow run` needing actions:write (#322). The
		// manifest grant exists only for apps created after it shipped.
		return fail("github_token_scope",
			"GITHUB_TOKEN lacks the permission this call needs — the GitHub App was installed without the required scope",
			"grant the app the missing permission (e.g. Actions: write for workflow dispatch) in its GitHub settings, or declare a narrower permissions: ceiling")
	case strings.Contains(lower, "installation not allowed to write"):
		// GHCR refuses GitHub App installation tokens outright — the only
		// GITHUB_TOKEN a KERN run can mint. The fix is a credential, not a
		// retry: a PAT with write:packages stored as a project secret.
		return fail("ghcr_token",
			"GHCR denied the push — GITHUB_TOKEN is a GitHub App installation token, which GHCR rejects for package writes",
			"create a classic PAT with write:packages, store it as a project secret (e.g. GHCR_TOKEN), and use it for docker login instead of GITHUB_TOKEN")
	case strings.Contains(lower, "permission denied"):
		return fail("permission_denied", "A file or operation returned permission denied",
			"check file ownership in the workspace or run the command with the right user")
	case sharedLibRe.MatchString(logTail):
		lib := sharedLibRe.FindStringSubmatch(logTail)[1]
		d = fail("missing_library",
			"Shared library `"+lib+"` is missing in the job image",
			"install the package that provides it, or use an image that ships it")
		if pkg := libPackage[lib]; pkg != "" {
			d.Suggestion = "install `" + pkg + "` in a prior step or use an image that ships it"
			d.Fixable = true
			d.Remediation = &remediation{Fix: installCommands(pkg)}
		}
		return d
	case exitCode == 127 || cmdNotFoundRe.MatchString(logTail) || execNotFoundRe.MatchString(logTail):
		// Checked before the network signatures: a missing command makes every
		// downstream call fail, and the app log that follows it is noise.
		pkg := missingBinary(logTail)
		d = fail("missing_binary", "A required command is missing in the job image",
			"install it in a prior step or switch to an image that ships it")
		if pkg != "" && unfixablePkg[pkg] {
			// Daemon/socket tools: installing the CLI does not provide the
			// service, so an auto-fix would just fail again on the next run.
			d.Summary = "Command `" + pkg + "` needs a daemon or socket the job image does not have"
			d.Suggestion = "install it in a prior step or use an image that ships it"
			d.AgentPrompt = "CI step failed: `" + pkg + "` requires a daemon or socket unavailable in the job container. " +
				"Suggested fix: " + d.Suggestion
			return d
		}
		if pkg != "" {
			d.Summary = "Command `" + pkg + "` is missing in the job image"
			d.Suggestion = "install `" + pkg + "` in a prior step or use an image that ships it"
			d.AgentPrompt = "CI step failed: `" + pkg + "` is not installed in the container. " +
				"Suggested fix: " + d.Suggestion
			d.Fixable = true
			d.Remediation = &remediation{Fix: installCommands(pkg)}
			if pkg == "gh" {
				// gh is not in Debian/Ubuntu main repos; apt remediation
				// fails. The kern baseline image ships it.
				d.Suggestion = "use the kern baseline image (KERN_DEFAULT_IMAGE=ghcr.io/dvorinka/kern-ubuntu:24.04) or install gh from cli.github.com in a prior step"
				d.AgentPrompt = "CI step failed: `gh` is not installed in the container. Suggested fix: " + d.Suggestion
				d.Fixable = false
				d.Remediation = nil
			}
		}
		return d
	case strings.Contains(lower, "could not resolve host") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "tls handshake timeout") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "esockettimedout") ||
		strings.Contains(lower, "etimedout") ||
		strings.Contains(lower, "read timed out") ||
		strings.Contains(lower, "connect timed out") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "status code 408") ||
		strings.Contains(lower, "status code 409") ||
		strings.Contains(lower, "status code 425") ||
		strings.Contains(lower, "status code 429") ||
		strings.Contains(lower, "status code 500") ||
		strings.Contains(lower, "status code 502") ||
		strings.Contains(lower, "status code 503") ||
		strings.Contains(lower, "status code 504"):
		// Registry 429s, reset connections and gateway 5xx are the fleet's
		// dominant flake — external, transient, retryable (unlike 4xx
		// auth/not-found, which stay script_failure).
		return fail("network", "A network call failed in the step",
			"transient registry/network flake — retry usually resolves it; verify outbound connectivity from the runner host if persistent")
	default:
		return fail("script_failure",
			"Step exited with code "+itoa(exitCode),
			"inspect the failing command's output above for the root cause")
	}
}

// missingBinary extracts the command name from shell "not found" messages.
func missingBinary(tail string) string {
	if m := cmdNotFoundRe.FindStringSubmatch(tail); len(m) == 2 {
		name := m[1]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if pkgNameRe.MatchString(name) {
			return name
		}
	}
	return ""
}

// installCommands is the only remediation plan the runner may execute. The
// package manager is resolved at apply time inside the container; each command
// is a fixed template — the package name is the only variable and is regex-gated.
func installCommands(pkg string) []string {
	if pkg == "java" {
		// `java` is a binary name, not a package on any distro — each manager
		// ships the JDK under a different name. Headless variants suffice:
		// steps need the runtime, not the demos or src.zip.
		return []string{
			"if command -v apt-get >/dev/null; then apt-get update -qq && apt-get install -y -qq default-jdk-headless" +
				"; elif command -v apk >/dev/null; then apk add --no-cache openjdk21" +
				"; elif command -v microdnf >/dev/null; then microdnf install -y java-21-openjdk-headless" +
				"; else echo 'no supported package manager' >&2; exit 1; fi",
		}
	}
	return []string{
		"if command -v apt-get >/dev/null; then apt-get update -qq && apt-get install -y -qq " + pkg +
			"; elif command -v apk >/dev/null; then apk add --no-cache " + pkg +
			"; elif command -v microdnf >/dev/null; then microdnf install -y " + pkg +
			"; else echo 'no supported package manager' >&2; exit 1; fi",
	}
}
