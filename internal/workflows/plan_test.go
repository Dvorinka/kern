package workflows

import (
	"strings"
	"testing"
)

func TestCompilePlanCarriesExecutionFields(t *testing.T) {
	src := `name: Ship
on: push
env:
  SHARED: root
  OVER: workflow
defaults:
  run:
    shell: bash
    working-directory: /src
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 2
    continue-on-error: true
    outputs:
      bin: ${{ steps.make.outputs.bin }}
    env:
      OVER: job
      N: 2
    container: golang:1.26
    defaults:
      run:
        working-directory: /src/cmd
    steps:
      - uses: actions/checkout@v4
      - id: make
        run: make
        timeout-minutes: 1.5
        continue-on-error: true
        working-directory: /src/cmd/kern
        shell: sh
      - run: echo hi
  test:
    needs: build
    if: always()
    runs-on: [linux-x64]
    steps:
      - name: Go
        run: go test ./...
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	if res.Plan.Concurrency != "ci-${{ github.ref }}" || !res.Plan.CancelInProgress {
		t.Fatalf("concurrency = %q cancel=%v", res.Plan.Concurrency, res.Plan.CancelInProgress)
	}
	build := findJob(t, res, "build")
	if build.TimeoutSeconds != 120 || !build.ContinueOnError {
		t.Fatalf("build timeout/coe = %d %v", build.TimeoutSeconds, build.ContinueOnError)
	}
	if build.Image != "golang:1.26" {
		t.Fatalf("container image = %q", build.Image)
	}
	if build.Outputs["bin"] != "${{ steps.make.outputs.bin }}" {
		t.Fatalf("outputs = %v", build.Outputs)
	}
	if build.Env["SHARED"] != "root" || build.Env["OVER"] != "job" || build.Env["N"] != "2" {
		t.Fatalf("env = %v", build.Env)
	}
	if len(build.Steps) != 3 {
		t.Fatalf("steps = %d", len(build.Steps))
	}
	checkout := build.Steps[0]
	if checkout.Name != "Run actions/checkout@v4" || checkout.Shell != "bash" || checkout.WorkingDirectory != "/src/cmd" {
		t.Fatalf("checkout = %+v", checkout)
	}
	makeStep := build.Steps[1]
	if makeStep.ID != "make" || makeStep.TimeoutSeconds != 90 || !makeStep.ContinueOnError {
		t.Fatalf("make step = %+v", makeStep)
	}
	if makeStep.Shell != "sh" || makeStep.WorkingDirectory != "/src/cmd/kern" {
		t.Fatalf("make step overrides = %+v", makeStep)
	}
	if build.Steps[2].TimeoutSeconds != 0 || build.Steps[2].Name != "echo hi" {
		t.Fatalf("plain step = %+v", build.Steps[2])
	}
	testJob := findJob(t, res, "test")
	if testJob.TimeoutSeconds != 3600 {
		t.Fatalf("default timeout = %d, want 3600", testJob.TimeoutSeconds)
	}
	if len(testJob.Needs) != 1 || testJob.Needs[0] != "build" || testJob.If != "always()" {
		t.Fatalf("test job = %+v", testJob)
	}
	if testJob.Image != "ubuntu:24.04" || testJob.Steps[0].Name != "Go" || testJob.Steps[0].Shell != "bash" {
		t.Fatalf("test step = %+v image %q", testJob.Steps[0], testJob.Image)
	}
}

func TestCompileConcurrencyWithoutGroupWarns(t *testing.T) {
	src := `on: push
concurrency:
  cancel-in-progress: true
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: echo a
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || !res.Plan.CancelInProgress || res.Plan.Concurrency != "" {
		t.Fatalf("plan valid=%v concurrency=%q cancel=%v", res.Valid, res.Plan.Concurrency, res.Plan.CancelInProgress)
	}
	if !hasDiagnostic(res, "concurrency") {
		t.Fatalf("expected concurrency warning, got %+v", res.Diagnostics)
	}
}

func TestCompileContainerAndReusableWorkflow(t *testing.T) {
	src := `on: push
jobs:
  boxed:
    runs-on: ubuntu-latest
    container:
      image: alpine:3.20
      credentials: {username: u}
    steps:
      - run: echo a
  call:
    uses: org/repo/.github/workflows/x.yml@main
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("reusable workflow without a resolver should be invalid")
	}
	if !hasDiagnostic(res, "reusable-resolve") {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	boxed := findJob(t, res, "boxed")
	if boxed.Image != "alpine:3.20" {
		t.Fatalf("container.image = %q", boxed.Image)
	}
	if !hasDiagnostic(res, "job-container") {
		t.Fatalf("expected ignored container option, got %+v", res.Diagnostics)
	}
}

func TestCompileRunLineTruncates(t *testing.T) {
	long := "echo " + strings.Repeat("a", 90)
	res, err := Compile("on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: "+long+"\n", testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	name := res.Plan.Jobs[0].Steps[0].Name
	if len(name) != 80 || name[77:] != "..." {
		t.Fatalf("name = %q len %d", name, len(name))
	}
}

func findJob(t *testing.T, res Result, id string) JobPlan {
	t.Helper()
	for _, j := range res.Plan.Jobs {
		if j.JobID == id || j.Key == id {
			return j
		}
	}
	t.Fatalf("no job %s", id)
	return JobPlan{}
}

func hasDiagnostic(res Result, code string) bool {
	for _, d := range res.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestCompileRunsOnMatrixInterpolation(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-latest, self-hosted]
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	var labels []string
	for _, j := range res.Plan.Jobs {
		if len(j.RunsOn) != 1 {
			t.Fatalf("runs-on: %v", j.RunsOn)
		}
		labels = append(labels, j.RunsOn[0])
	}
	joined := labels[0] + "," + labels[1]
	if !strings.Contains(joined, "ubuntu-latest") || !strings.Contains(joined, "self-hosted") {
		t.Fatalf("matrix runs-on not interpolated: %v", labels)
	}
	for _, j := range res.Plan.Jobs {
		if strings.Contains(j.RunsOn[0], "${{") {
			t.Fatalf("literal expression survived: %v", j.RunsOn)
		}
	}
}

func TestCompileRunsOnUnresolvedContextWarns(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: ${{ inputs.runner }}
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dg := range res.Diagnostics {
		if dg.Code == "runs-on" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want runs-on warning, got %+v", res.Diagnostics)
	}
}

func TestCompileContainerEnv(t *testing.T) {
	src := `on: push
env: {WF: wf}
jobs:
  j:
    runs-on: self-hosted
    env: {JOB: job, SHARED: job-wins}
    container:
      image: alpine:3.20
      env: {CENV: c, SHARED: container-loses}
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	j := findJob(t, res, "j")
	if j.Env["CENV"] != "c" || j.Env["WF"] != "wf" || j.Env["JOB"] != "job" {
		t.Fatalf("container.env not merged: %+v", j.Env)
	}
	if j.Env["SHARED"] != "job-wins" {
		t.Fatalf("job env must win over container.env: %+v", j.Env)
	}
	for _, dg := range res.Diagnostics {
		if dg.Code == "job-container" && strings.Contains(dg.Path, "env") {
			t.Fatalf("container.env must not warn: %+v", dg)
		}
	}
}

func TestCompileRunsOnMatrixArrayExpands(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: ${{ matrix.ro }}
    strategy:
      matrix:
        ro:
          - [self-hosted, macos, x64]
          - [self-hosted, linux, x64]
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	if len(res.Plan.Jobs) != 2 {
		t.Fatalf("want 2 matrix jobs, got %d", len(res.Plan.Jobs))
	}
	for _, j := range res.Plan.Jobs {
		if len(j.RunsOn) != 3 {
			t.Fatalf("runs-on should expand to 3 labels, got %v", j.RunsOn)
		}
		for _, l := range j.RunsOn {
			if l == "Array" || strings.Contains(l, "${{") {
				t.Fatalf("bad label %q in %v", l, j.RunsOn)
			}
		}
	}
}

// A .* filter result is a list too: it must expand into labels, not the
// literal string "Array" that Stringify would render.
func TestCompileRunsOnStarFilterExpands(t *testing.T) {
	src := `on: push
jobs:
  j:
    runs-on: ${{ matrix.ro.* }}
    strategy:
      matrix:
        ro:
          - [self-hosted, linux]
          - [self-hosted, macos]
    steps:
      - run: echo hi
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	if len(res.Plan.Jobs) != 2 {
		t.Fatalf("want 2 matrix jobs, got %d", len(res.Plan.Jobs))
	}
	for _, j := range res.Plan.Jobs {
		if len(j.RunsOn) != 2 {
			t.Fatalf("star filter should expand to 2 labels, got %v", j.RunsOn)
		}
		for _, l := range j.RunsOn {
			if l == "Array" || strings.Contains(l, "${{") {
				t.Fatalf("bad label %q in %v", l, j.RunsOn)
			}
		}
	}
}
