package runner

import (
	"strings"
	"testing"
)

func TestDiagnose(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		tail     string
		stepErr  string
		wantKind string
		fixable  bool
	}{
		{"command not found bash", 127, "bash: line 2: jq: command not found\n", "", "missing_binary", true},
		{"command not found sh", 127, "sh: curl: not found\n", "", "missing_binary", true},
		{"command not found script header", 1, "/kern/scripts/step-6-9.body.sh: line 3: curl: command not found\n/kern/scripts/step-6-9.body.sh: line 3: curl: command not found\n", "", "missing_binary", true},
		{"shared lib", 127, "node: error while loading shared libraries: libatomic.so.1: cannot open shared object file\n", "", "missing_library", true},
		{"timeout", 124, "", "step timed out after 30 seconds", "step_timeout", false},
		{"permission", 1, "touch: cannot touch '/etc/x': Permission denied\n", "", "permission_denied", false},
		{"ghcr install token", 1, "denied: permission_denied: installation not allowed to Write organization package\n", "", "ghcr_token", false},
		{"token scope", 1, "Error: Resource not accessible by integration - https://docs.github.com/rest/actions/workflows\n", "", "github_token_scope", false},
		{"network", 1, "curl: (6) Could not resolve host: api.example.com\n", "", "network", false},
		{"disk", 1, "write failed: No space left on device\n", "", "disk_full", false},
		{"disk in stepErr", 1, "", "prepare workspace: no space left on device", "disk_full", false},
		{"oom", 137, "", "", "oom", false},
		{"gradle daemon oom", 1, "Gradle build daemon disappeared unexpectedly (it may have been killed or may have crashed)\n", "", "oom", false},
		{"daemon exception", 1, "org.gradle.launcher.daemon.client.DaemonDisappearedException: Gradle build daemon disappeared\n", "", "oom", false},
		{"go compile killed", 1, "# github.com/ugorji/go/codec\ncompile: signal: killed\n", "", "oom", false},
		{"java oom", 1, "java.lang.OutOfMemoryError: Metaspace\n", "", "oom", false},
		{"maven 429", 1, "Could not GET 'https://repo.maven.apache.org/maven2/x/y.jar'. Received status code 429 from server: Too Many Requests\n", "", "network", false},
		{"registry 502", 1, "Get \"https://registry-1.docker.io/v2/\": received status code 502\n", "", "network", false},
		{"conn reset", 1, "read tcp 10.0.0.5:443: read: connection reset by peer\n", "", "network", false},
		{"npm socket timeout", 1, "npm ERR! network request to https://registry.npmjs.org failed, reason: ESOCKETTIMEDOUT\n", "", "network", false},
		{"generic", 2, "make: *** [all] Error 1\n", "", "script_failure", false},
		{"gh curated fix", 127, "bash: line 4: gh: command not found\n", "", "missing_binary", true},
		{"minisign curated fix", 127, "sh: minisign: not found\n", "", "missing_binary", true},
		{"tauri-driver hint", 127, "sh: tauri-driver: not found\n", "", "missing_binary", false},
		{"pnpm hint", 127, "sh: pnpm: not found\n", "", "missing_binary", false},
		{"ployz build kill", 1, "build resource cleanup could not be confirmed\n", "", "build_killed", false},
		{"ployz stale lock", 1, "quarantined: stale lock /var/tmp/ployz-build-1701 held by dead pid\n", "", "stale_lock", true},
		{"pnpm store corrupt", 1, "ENOENT: no such file or directory, open '/root/.local/share/pnpm/store/v3/files/ab/cdef'\n", "", "pnpm_store", true},
	}
	for _, tc := range cases {
		d := diagnose(StepSpec{}, tc.code, tc.tail, tc.stepErr, "6g", func() bool { return true })
		if d.Kind != tc.wantKind {
			t.Errorf("%s: kind=%s want %s", tc.name, d.Kind, tc.wantKind)
		}
		if d.Fixable != tc.fixable {
			t.Errorf("%s: fixable=%v want %v", tc.name, d.Fixable, tc.fixable)
		}
		if d.AgentPrompt == "" || d.Suggestion == "" {
			t.Errorf("%s: diagnosis missing agent prompt or suggestion", tc.name)
		}
	}
}

func TestDiagnoseDaemonGone(t *testing.T) {
	tail := "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"
	dead := func() bool { return false }
	live := func() bool { return true }

	if d := diagnose(StepSpec{}, 1, tail, "", "6g", dead); d.Kind != "daemon_gone" {
		t.Fatalf("dead daemon = %q, want daemon_gone", d.Kind)
	}
	// The same stderr text from a job's own docker client is user error:
	// the host daemon still answers the probe, so it must not claim infra.
	if d := diagnose(StepSpec{}, 1, tail, "", "6g", live); d.Kind == "daemon_gone" {
		t.Fatalf("live daemon must not classify daemon_gone, got %q", d.Kind)
	}
	// A live probe also keeps docker-daemon text from stealing real classes.
	if d := diagnose(StepSpec{}, 137, tail, "", "6g", live); d.Kind != "oom" {
		t.Fatalf("137 with live daemon = %q, want oom", d.Kind)
	}
}

func TestDiagnoseOOMSurfacesCeiling(t *testing.T) {
	d := diagnose(StepSpec{}, 137, "", "", "6g", func() bool { return true })
	if !strings.Contains(d.Summary, "6g") || !strings.Contains(d.Suggestion, "6g") {
		t.Fatalf("oom diagnosis must name the memory ceiling: %+v", d)
	}
}

func TestMissingBinaryName(t *testing.T) {
	if got := missingBinary("bash: line 1: /usr/bin/jq: command not found"); got != "jq" {
		t.Fatalf("got %q", got)
	}
	if got := missingBinary("some other failure"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	cmds := installCommands("jq")
	if len(cmds) != 1 || !strings.Contains(cmds[0], "apt-get") || !strings.Contains(cmds[0], "apk") {
		t.Fatalf("unexpected fix plan: %v", cmds)
	}
}
