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
	}
	for _, tc := range cases {
		d := diagnose(StepSpec{}, tc.code, tc.tail, tc.stepErr, "6g")
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

func TestDiagnoseOOMSurfacesCeiling(t *testing.T) {
	d := diagnose(StepSpec{}, 137, "", "", "6g")
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
