package runner

import (
	"slices"
	"testing"
)

func hasLabel(ls []string, want string) bool { return slices.Contains(ls, want) }

// The darwin label bug advertised linux-* selectors on macOS runners — a
// real scheduling hazard, since a `runs-on: [linux]` job could land on a Mac.
// labelsFor must keep the OS label families disjoint.
func TestLabelsDarwin(t *testing.T) {
	a := &Agent{cfg: Config{Executor: "host"}}
	for _, arch := range []string{"amd64", "arm64"} {
		ls := a.labelsFor("darwin", arch)
		for _, want := range []string{"self-hosted", "kern", "macos", "darwin-" + arch, arch} {
			if !hasLabel(ls, want) {
				t.Errorf("darwin/%s labels %v missing %q", arch, ls, want)
			}
		}
		for _, bad := range []string{"linux", "ubuntu-latest", "ubuntu-24.04", "docker", "linux-x64", "linux-arm64", "linux-amd64"} {
			if hasLabel(ls, bad) {
				t.Errorf("darwin/%s labels %v leaked %q", arch, ls, bad)
			}
		}
	}
}

func TestLabelsLinux(t *testing.T) {
	// docker executor + docker daemon reachable: the docker label is legal
	// on linux; macos never appears regardless.
	a := &Agent{cfg: Config{Executor: "docker"}}
	ls := a.labelsFor("linux", "amd64")
	for _, want := range []string{"self-hosted", "kern", "linux", "linux-amd64", "linux-x64", "x64", "ubuntu-latest", "ubuntu-24.04"} {
		if !hasLabel(ls, want) {
			t.Errorf("linux/amd64 labels %v missing %q", ls, want)
		}
	}
	if hasLabel(ls, "macos") {
		t.Errorf("linux labels leaked macos: %v", ls)
	}
}

// A host-executor runner must never advertise the docker label even when a
// daemon happens to be installed on the host.
func TestLabelsHostExecutorNoDocker(t *testing.T) {
	a := &Agent{cfg: Config{Executor: "host"}}
	if hasLabel(a.labelsFor("linux", "amd64"), "docker") {
		t.Error("host executor advertised the docker label")
	}
}
