package runner

import (
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
