package runner

import (
	"runtime"
	"testing"

	"github.com/dvorinka/kern/internal/actions"
)

func TestContainerNodeDir(t *testing.T) {
	e := &executor{}
	if got := e.nodeBinDir("node20"); got != "/kern/node/v20.18.1/bin" {
		t.Fatal(got)
	}
	if got := e.nodeBinDir("node24"); got != "/kern/node/v24.22.2/bin" {
		t.Fatal(got)
	}
	// Deprecated runtimes retarget onto the node20 pin, matching GitHub.
	if got := e.nodeBinDir("node16"); got != "/kern/node/v20.18.1/bin" {
		t.Fatal(got)
	}
}

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
	env := inputEnv(m, map[string]any{"WHO": "kern", "extra": true}, nil)
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
	archive, err := nodeArchive("v20.18.1")
	if err != nil {
		t.Skipf("unsupported host: %v", err)
	}
	want := "node-v20.18.1-" + runtime.GOOS + "-x64.tar.gz"
	if runtime.GOARCH == "arm64" {
		want = "node-v20.18.1-" + runtime.GOOS + "-arm64.tar.gz"
	}
	if archive != want {
		t.Errorf("archive = %q, want %q", archive, want)
	}
}
