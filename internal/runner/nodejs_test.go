package runner

import (
	"testing"

	"github.com/dvorinka/kern/internal/actions"
)

func TestInputKey(t *testing.T) {
	for in, want := range map[string]string{
		"who":          "INPUT_WHO",
		"node-version": "INPUT_NODE-VERSION",
		"some input":   "INPUT_SOME_INPUT",
	} {
		if got := inputKey(in); got != want {
			t.Errorf("inputKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInputEnv(t *testing.T) {
	m, err := actions.ParseMeta([]byte(`
inputs:
  who:
    default: world
  token:
    required: true
runs:
  using: node20
`))
	if err != nil {
		t.Fatal(err)
	}
	env := inputEnv(m, map[string]any{"WHO": "kern", "extra": true})
	if env["INPUT_WHO"] != "kern" {
		t.Errorf("declared input: %v", env)
	}
	if env["INPUT_TOKEN"] != "" {
		t.Errorf("missing declared input should default to empty: %v", env)
	}
	if env["INPUT_EXTRA"] != "true" {
		t.Errorf("undeclared with key should map to INPUT_*: %v", env)
	}
}

func TestNodeArchive(t *testing.T) {
	archive, err := nodeArchive()
	if err != nil {
		t.Skipf("unsupported host: %v", err)
	}
	want := "node-v20.18.1-linux-x64.tar.gz"
	if archive != want {
		t.Errorf("archive = %q, want %q", archive, want)
	}
}
