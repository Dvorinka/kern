package workflows

import (
	"os"
	"strings"
	"testing"
)

var testCompiler = Compiler{
	DefaultImage: "ubuntu:24.04",
	LabelImages: map[string]string{
		"ubuntu-24.04": "ubuntu:24.04",
		"linux-x64":    "ubuntu:24.04",
	},
}

func compileFile(t *testing.T, name string) Result {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Compile(string(content), testCompiler)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return res
}

func TestCompileSimple(t *testing.T) {
	res := compileFile(t, "simple.yml")
	if !res.Valid {
		t.Fatalf("expected valid workflow, diagnostics: %+v", res.Diagnostics)
	}
	if res.Plan.Name != "CI" {
		t.Errorf("name = %q", res.Plan.Name)
	}
	if len(res.Plan.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(res.Plan.Jobs))
	}
	build := res.Plan.Jobs[0]
	if build.Key != "build" || build.Image != "ubuntu:24.04" {
		t.Errorf("unexpected build job: %+v", build)
	}
	if build.Env["GOFLAGS"] != "-mod=readonly" {
		t.Errorf("workflow env not inherited: %+v", build.Env)
	}
	if build.Steps[0].Uses != "actions/checkout@v4" {
		t.Errorf("checkout step lost: %+v", build.Steps[0])
	}
	if build.Steps[1].Name != "Build" {
		t.Errorf("explicit step name = %q", build.Steps[1].Name)
	}
	test := res.Plan.Jobs[1]
	if len(test.Needs) != 1 || test.Needs[0] != "build" {
		t.Errorf("needs = %v", test.Needs)
	}
	if test.Env["CGO_ENABLED"] != "0" {
		t.Errorf("job env = %v", test.Env)
	}
	if test.Steps[1].Env["TEST_LEVEL"] != "2" {
		t.Errorf("step env = %v", test.Steps[1].Env)
	}
	if len(res.Plan.Edges) != 1 || res.Plan.Edges[0].From != "build" || res.Plan.Edges[0].To != "test" {
		t.Errorf("edges = %+v", res.Plan.Edges)
	}
	if res.Plan.Jobs[0].FailFast != true {
		t.Error("fail-fast should default to true")
	}
}

func TestMatrixExpansion(t *testing.T) {
	res := compileFile(t, "matrix.yml")
	if !res.Valid {
		t.Fatalf("expected valid, diagnostics: %+v", res.Diagnostics)
	}
	if len(res.Plan.Jobs) != 3 {
		keys := make([]string, len(res.Plan.Jobs))
		for i, j := range res.Plan.Jobs {
			keys[i] = j.Key
		}
		t.Fatalf("jobs = %v, want 3 combinations", keys)
	}
	if res.Plan.Jobs[0].FailFast {
		t.Error("fail-fast: false was not honoured")
	}
	foundExperimental := false
	for _, j := range res.Plan.Jobs {
		if j.Matrix["experimental"] == true {
			foundExperimental = true
		}
		if j.Matrix["go"] == "1.23" && j.Matrix["os"] == "ubuntu-latest" {
			t.Errorf("excluded combination was scheduled: %s", j.Key)
		}
	}
	if !foundExperimental {
		t.Error("include was not merged into the matching combination")
	}
}

func TestUnknownAndUnsupportedConstructs(t *testing.T) {
	res := compileFile(t, "hostile.yml")
	if res.Valid {
		t.Fatal("hostile workflow should not be valid")
	}
	wantCodes := []string{"unknown-key", "action-unsupported", "step-both", "no-steps", "services", "cycle"}
	for _, code := range wantCodes {
		found := false
		for _, d := range res.Diagnostics {
			if d.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing diagnostic %q; got %+v", code, res.Diagnostics)
		}
	}
}

func TestInvalidYAML(t *testing.T) {
	res, err := Compile("jobs: [", testCompiler)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Valid || len(res.Diagnostics) == 0 || res.Diagnostics[0].Code != "yaml" {
		t.Fatalf("expected YAML diagnostic, got %+v", res)
	}
}

func TestTriggerForms(t *testing.T) {
	cases := map[string]string{
		"`on: push`":                   "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo a\n",
		"`on: [push]`":                 "on: [push]\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo a\n",
		"`on: {push: {}}`":             "on:\n  push:\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo a\n",
		"workflow_dispatch with input": "on:\n  workflow_dispatch:\n    inputs:\n      env:\n        type: string\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo a\n",
	}
	for name, src := range cases {
		res, err := Compile(src, testCompiler)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !res.Valid || len(res.Events) == 0 {
			t.Errorf("%s: invalid, events=%v diagnostics=%+v", name, res.Events, res.Diagnostics)
		}
	}
}

func TestExpressionSyntaxIsValidated(t *testing.T) {
	src := "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ github.ref\n"
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("unterminated expression should invalidate the workflow")
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Code == "expression" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected expression diagnostic, got %+v", res.Diagnostics)
	}
}

func TestMissingRunsOn(t *testing.T) {
	res, _ := Compile("on: push\njobs:\n  a:\n    steps:\n      - run: echo a\n", testCompiler)
	if res.Valid {
		t.Fatal("job without runs-on should be invalid")
	}
	if !strings.Contains(diagnosticText(res), "runs-on") {
		t.Errorf("diagnostics: %s", diagnosticText(res))
	}
}

func diagnosticText(res Result) string {
	var b strings.Builder
	for _, d := range res.Diagnostics {
		b.WriteString(d.Code + ": " + d.Message + "\n")
	}
	return b.String()
}

func TestEnvironment(t *testing.T) {
	src := `on: push
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment:
      name: prod-${{ matrix.tier }}
      url: https://example.com/${{ matrix.tier }}
    strategy:
      matrix:
        tier: [web, api]
    steps:
      - run: echo ship
`
	res, err := Compile(src, testCompiler)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("expected valid, diagnostics: %+v", res.Diagnostics)
	}
	if len(res.Plan.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2 matrix members", len(res.Plan.Jobs))
	}
	for _, j := range res.Plan.Jobs {
		tier := j.Matrix["tier"]
		want := "prod-" + asString(tier)
		if j.Environment != want {
			t.Errorf("job %s environment = %q, want %q", j.Key, j.Environment, want)
		}
		if j.EnvironmentURL != "https://example.com/"+asString(tier) {
			t.Errorf("job %s environment url = %q", j.Key, j.EnvironmentURL)
		}
	}

	// Bare string form.
	res, err = Compile("on: push\njobs:\n  d:\n    runs-on: ubuntu-latest\n    environment: staging\n    steps:\n      - run: echo\n", testCompiler)
	if err != nil || !res.Valid {
		t.Fatalf("string environment should compile: %v %+v", err, res.Diagnostics)
	}
	if res.Plan.Jobs[0].Environment != "staging" {
		t.Errorf("environment = %q", res.Plan.Jobs[0].Environment)
	}

	// Empty name is a compile error.
	res, _ = Compile("on: push\njobs:\n  d:\n    runs-on: ubuntu-latest\n    environment: {}\n    steps:\n      - run: echo\n", testCompiler)
	if res.Valid {
		t.Fatal("environment without name should be invalid")
	}
	if !strings.Contains(diagnosticText(res), "environment") {
		t.Errorf("diagnostics: %s", diagnosticText(res))
	}
}
