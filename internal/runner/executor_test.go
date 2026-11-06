package runner

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestJobCAMount(t *testing.T) {
	got := jobCAMount()
	if _, err := os.Stat(hostCABundle); err != nil {
		if got != nil {
			t.Fatal(got)
		}
		return
	}
	if len(got) != 2 || got[1] != hostCABundle+":"+hostCABundle+":ro" {
		t.Fatal(got)
	}
}

func TestImageValidation(t *testing.T) {
	for _, img := range []string{"ubuntu:24.04", "ghcr.io/dvorinka/kern-runner", "alpine", "node:22-bookworm@sha256:abc-_.0"} {
		if !imageRe.MatchString(img) {
			t.Errorf("valid image rejected: %q", img)
		}
	}
	for _, img := range []string{"ubuntu;rm -rf /", "img $(id)", "a b", "${VAR}", "img`id`"} {
		if imageRe.MatchString(img) {
			t.Errorf("unsafe image accepted: %q", img)
		}
	}
}

func TestParseKeyValue(t *testing.T) {
	got, err := parseKeyValue("# comment\nKEY=value\nEMPTY=\nspaced=1\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"KEY": "value", "EMPTY": "", "spaced": "1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: want %q got %q", k, v, got[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected keys: %v", got)
	}
}

func TestPathLinesTrailingNewline(t *testing.T) {
	b := newEnvBuilder(&Claim{}, nil)
	if got := b.pathLines(); got != "" {
		t.Fatalf("empty: %q", got)
	}
	b.persistPaths([]string{"/kern/node/v20.18.1/bin"})
	got := b.pathLines()
	if got != "/kern/node/v20.18.1/bin\n" {
		t.Fatalf("%q", got)
	}
	// @actions/core addPath appends "<dir>\n" onto this file.
	appended := got + "/kern/cache/_toolcache/go/1.26.10/x64/bin\n"
	lines := strings.Split(strings.TrimRight(appended, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "/kern/node/v20.18.1/bin" || lines[1] != "/kern/cache/_toolcache/go/1.26.10/x64/bin" {
		t.Fatal(lines)
	}
}

func TestEnvBuilderRender(t *testing.T) {
	claim := &Claim{}
	claim.Job.Env = map[string]string{"V_JOB": "1"}
	e := newEnvBuilder(claim, map[string]string{"SECRET_TOKEN": "hunter2", "BAD KEY": "x"})
	e.persistKV("PERSISTED", "yes")
	e.persistPaths([]string{"/first", " ", "/second"})
	step := StepSpec{Env: map[string]string{"V_STEP": "2"}}
	content := e.render(step)

	for _, want := range []string{"V_JOB=1", "PERSISTED=yes", "SECRET_TOKEN=hunter2", "V_STEP=2"} {
		if !strings.Contains(content, want) {
			t.Errorf("env file missing %q\n%s", want, content)
		}
	}
	if strings.Contains(content, "BAD KEY=") {
		t.Errorf("env key with space produced invalid line:\n%s", content)
	}
}

func TestCloneArgs(t *testing.T) {
	pub := cloneArgs(ClaimRun{RepoURL: "https://github.com/o/r.git"}, "/ws")
	if got := strings.Join(pub, " "); got != "clone --quiet --depth 1 -- https://github.com/o/r.git /ws" {
		t.Errorf("public: %q", got)
	}
	priv := cloneArgs(ClaimRun{RepoURL: "https://github.com/o/r.git", SHA: strings.Repeat("a", 40), CloneToken: "tok"}, "/ws")
	want := "http.extraHeader=Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))
	if priv[1] != want {
		t.Errorf("missing auth header: %v", priv)
	}
	for _, a := range priv {
		if a == "--depth" {
			t.Errorf("sha checkout must keep full history: %v", priv)
		}
	}
}

func TestPrivilegedExecUID(t *testing.T) {
	e := &executor{claim: &Claim{}}
	e.claim.Run.TrustLevel = "fork"
	if e.execUID() != jobUID {
		t.Error("untrusted runs must stay uid 1000")
	}
	e.claim.Run.TrustLevel = "trusted"
	if e.execUID() != 0 {
		t.Error("trusted runs must run as root")
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("abc/def-123"); got != "abc-def-123" {
		t.Errorf("sanitizeID: got %q", got)
	}
}

func TestJobStatusSoFarAndContext(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-job-1"
	e, err := newJobExecutor(cfg, &client{}, claim, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if e.jobStatusSoFar() != "" {
		t.Errorf("fresh executor status = %q", e.jobStatusSoFar())
	}
	e.stepResults["s1"] = &stepOutcome{Conclusion: "success", Outcome: "success"}
	if got := e.jobStatusSoFar(); got != "" {
		t.Errorf("after success status = %q", got)
	}
	e.stepResults["s2"] = &stepOutcome{Conclusion: "failure", Outcome: "failure"}
	if got := e.jobStatusSoFar(); got != "failure" {
		t.Errorf("after failure status = %q", got)
	}
	ctx := e.runnerContext()
	if _, ok := ctx["github"]; !ok {
		t.Errorf("context missing github")
	}
	if _, ok := ctx["steps"]; !ok {
		t.Errorf("context missing steps")
	}
}
