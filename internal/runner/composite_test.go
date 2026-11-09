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

func TestMissingActionEntrypoints(t *testing.T) {
	write := func(dir, name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// node action missing its main entrypoint — the paths-filter failure mode.
	dir := t.TempDir()
	write(dir, "action.yml", "runs:\n  using: node20\n  main: dist/index.js\n  post: dist/post.js\n")
	write(dir, "dist/post.js", "// post\n")
	missing := missingActionEntrypoints(dir)
	if len(missing) != 1 || missing[0] != "dist/index.js" {
		t.Fatalf("missing = %v, want [dist/index.js]", missing)
	}
	write(dir, "dist/index.js", "// main\n")
	if missing := missingActionEntrypoints(dir); len(missing) != 0 {
		t.Fatalf("complete tree reported missing: %v", missing)
	}

	// composite actions declare no entry files — never reported missing.
	comp := t.TempDir()
	write(comp, "action.yml", "runs:\n  using: composite\n  steps:\n    - run: echo hi\n      shell: bash\n")
	if missing := missingActionEntrypoints(comp); len(missing) != 0 {
		t.Fatalf("composite reported missing: %v", missing)
	}

	// docker action building from Dockerfile requires the file; named images do not.
	dkr := t.TempDir()
	write(dkr, "action.yml", "runs:\n  using: docker\n  image: Dockerfile\n")
	if missing := missingActionEntrypoints(dkr); len(missing) != 1 || missing[0] != "Dockerfile" {
		t.Fatalf("docker missing = %v, want [Dockerfile]", missing)
	}
	write(dkr, "Dockerfile", "FROM alpine\n")
	if missing := missingActionEntrypoints(dkr); len(missing) != 0 {
		t.Fatalf("docker tree reported missing: %v", missing)
	}
	img := t.TempDir()
	write(img, "action.yml", "runs:\n  using: docker\n  image: alpine:3.20\n")
	if missing := missingActionEntrypoints(img); len(missing) != 0 {
		t.Fatalf("image-based docker action reported missing: %v", missing)
	}
}

func TestMetaStepsParse(t *testing.T) {
	m := metaWith("    - id: a\n      run: echo hi\n      shell: bash\n      env:\n        X: \"1\"\n")
	cs := m.Runs.Steps[0]
	if cs.ID != "a" || cs.Run != "echo hi" || cs.Shell != "bash" || cs.Env["X"] != "1" {
		t.Errorf("step parse: %+v", cs)
	}
}

func TestParseLSRemote(t *testing.T) {
	sha1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sha3 := "cccccccccccccccccccccccccccccccccccccccc"
	out := sha1 + "\trefs/heads/main\n" +
		sha2 + "\trefs/tags/v1\n" +
		sha3 + "\trefs/tags/v1^{}\n"
	cases := []struct{ ref, want string }{
		{"main", sha1},            // branch
		{"v1", sha3},              // annotated tag prefers peeled commit
		{"refs/heads/main", sha1}, // full refname
		{"nope", ""},              // no match
	}
	for _, c := range cases {
		if got := parseLSRemote(out, c.ref); got != c.want {
			t.Errorf("parseLSRemote(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestIsActionSHA(t *testing.T) {
	if !isActionSHA("0123456789abcdef0123456789abcdef01234567") {
		t.Error("40-hex sha rejected")
	}
	if isActionSHA("main") || isActionSHA("0123") || isActionSHA("0123456789abcdef0123456789abcdef0123456g") {
		t.Error("non-sha accepted")
	}
}
