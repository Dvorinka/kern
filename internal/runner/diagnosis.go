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
	pkgNameRe      = regexp.MustCompile(`^[a-zA-Z0-9._+-]{1,64}$`)
)

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
func diagnose(step StepSpec, exitCode int, logTail, stepErr string) *diagnosis {
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
	lower := strings.ToLower(logTail)
	switch {
	case strings.Contains(stepErr, "timed out"):
		return fail("step_timeout", "Step exceeded timeout-minutes and was killed",
			"raise timeout-minutes or split the work in the step")
	case strings.Contains(stepErr, "not supported"):
		return fail("unsupported_action",
			"The step uses an action KERN does not support yet",
			"replace the uses: step with a run: equivalent; see docs/compatibility.md")
	case exitCode == 137 || strings.Contains(lower, "killed\n") || strings.Contains(lower, "out of memory"):
		return fail("oom", "Process was killed, likely OOM or SIGKILL (exit 137)",
			"raise KERN_JOB_MEMORY on the runner or reduce the step's memory footprint")
	case strings.Contains(lower, "no space left on device"):
		return fail("disk_full", "The job container ran out of disk space",
			"free disk on the runner host or raise the work-root quota")
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
		if pkg != "" {
			d.Summary = "Command `" + pkg + "` is missing in the job image"
			d.Suggestion = "install `" + pkg + "` in a prior step or use an image that ships it"
			d.AgentPrompt = "CI step failed: `" + pkg + "` is not installed in the container. " +
				"Suggested fix: " + d.Suggestion
			d.Fixable = true
			d.Remediation = &remediation{Fix: installCommands(pkg)}
		}
		return d
	case strings.Contains(lower, "could not resolve host") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "tls handshake timeout"):
		return fail("network", "A network call failed in the step",
			"verify outbound connectivity from the runner host; retry if transient")
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
	return []string{
		"if command -v apt-get >/dev/null; then apt-get update -qq && apt-get install -y -qq " + pkg +
			"; elif command -v apk >/dev/null; then apk add --no-cache " + pkg +
			"; elif command -v microdnf >/dev/null; then microdnf install -y " + pkg +
			"; else echo 'no supported package manager' >&2; exit 1; fi",
	}
}
