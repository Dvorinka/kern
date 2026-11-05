package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dvorinka/kern/internal/actions"
)

func metaWith(steps string) *actions.Meta {
	m, err := actions.ParseMeta([]byte("runs:\n  using: composite\n  steps:\n" + steps))
	if err != nil {
		panic(err)
	}
	return m
}

func TestResolveInputs(t *testing.T) {
	m, err := actions.ParseMeta([]byte(`
inputs:
  who:
    required: true
  greeting:
    default: hello
runs:
  using: composite
`))
	if err != nil {
		t.Fatal(err)
	}
	with := map[string]any{"WHO": "world", "extra": "x"}
	inputs, undeclared, err := resolveInputs(m, with)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["who"] != "world" {
		t.Errorf("case-insensitive with lookup failed: %v", inputs)
	}
	if inputs["greeting"] != "hello" {
		t.Errorf("default not applied: %v", inputs)
	}
	if inputs["extra"] != "x" {
		t.Errorf("undeclared input should still reach the context: %v", inputs)
	}
	if len(undeclared) != 1 || undeclared[0] != "extra" {
		t.Errorf("undeclared = %v", undeclared)
	}
}

func TestResolveInputsRequiredMissing(t *testing.T) {
	m, err := actions.ParseMeta([]byte("inputs:\n  who:\n    required: true\nruns:\n  using: composite\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveInputs(m, nil); err == nil {
		t.Fatal("missing required input should error")
	}
}

func TestCompositeShell(t *testing.T) {
	for name, want := range map[string]string{
		"sh": "/bin/sh", "bash": "/bin/bash", "/bin/zsh": "/bin/zsh",
	} {
		got, err := compositeShell(name, "/bin/bash")
		if err != nil || got != want {
			t.Errorf("compositeShell(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if got, err := compositeShell("bash", "/bin/sh"); err != nil || got != "/bin/sh" {
		t.Errorf("bash should fall back to the job shell: %q %v", got, err)
	}
	for _, name := range []string{"pwsh", "python", "cmd", "zsh"} {
		if _, err := compositeShell(name, "/bin/bash"); err == nil {
			t.Errorf("compositeShell(%q) should fail", name)
		}
	}
}

func TestCompositeCtxMergesSteps(t *testing.T) {
	evalCtx := map[string]any{
		"github": map[string]any{"sha": "abc"},
		"steps": map[string]any{
			"outer": map[string]any{"conclusion": "success", "outputs": map[string]any{}},
		},
	}
	internal := map[string]*stepOutcome{
		"inner": {Conclusion: "success", Outcome: "success", Outputs: map[string]string{"v": "1"}},
	}
	ctx := compositeCtx(evalCtx, map[string]any{"who": "x"}, internal, "")
	steps := ctx["steps"].(map[string]any)
	if steps["outer"] == nil || steps["inner"] == nil {
		t.Fatalf("steps not merged: %v", steps)
	}
	outs := steps["inner"].(map[string]any)["outputs"].(map[string]any)
	if outs["v"] != "1" {
		t.Errorf("inner outputs missing: %v", outs)
	}
	if ctx["inputs"].(map[string]any)["who"] != "x" {
		t.Error("inputs context missing")
	}
	if _, ok := evalCtx["inputs"]; ok {
		t.Error("caller context was mutated")
	}
}

func TestReadActionMeta(t *testing.T) {
	dir := t.TempDir()
	if _, err := readActionMeta(dir); err == nil {
		t.Fatal("missing action.yml should error")
	}
	if err := os.WriteFile(filepath.Join(dir, "action.yaml"), []byte("runs:\n  using: node20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readActionMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Runs.Using != "node20" {
		t.Errorf("using = %q", m.Runs.Using)
	}
}

func TestMetaStepsParse(t *testing.T) {
	m := metaWith("    - id: a\n      run: echo hi\n      shell: bash\n      env:\n        X: \"1\"\n")
	cs := m.Runs.Steps[0]
	if cs.ID != "a" || cs.Run != "echo hi" || cs.Shell != "bash" || cs.Env["X"] != "1" {
		t.Errorf("step parse: %+v", cs)
	}
}
