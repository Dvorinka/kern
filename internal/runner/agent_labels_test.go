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
		archLabel := arch
		if arch == "amd64" {
			archLabel = "x64"
		}
		for _, want := range []string{"self-hosted", "kern", "macos", "darwin-" + arch, archLabel} {
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

// The runner's name rides as a label so jobs can pin a host — prime
// fan-outs and operator drills. Empty names add nothing; a name that is
// already a label dedupes.
func TestLabelsName(t *testing.T) {
	a := &Agent{cfg: Config{Executor: "docker", Name: "ci-runner-901"}}
	if !hasLabel(a.labelsFor("linux", "amd64"), "ci-runner-901") {
		t.Error("runner name missing from labels")
	}
	a = &Agent{cfg: Config{Executor: "docker", Name: "  "}}
	for _, l := range a.labelsFor("linux", "amd64") {
		if l == "" || l == "  " {
			t.Errorf("blank name leaked into labels: %v", a.labelsFor("linux", "amd64"))
		}
	}
	a = &Agent{cfg: Config{Executor: "docker", Name: "ubuntu-latest"}}
	n := 0
	for _, l := range a.labelsFor("linux", "amd64") {
		if l == "ubuntu-latest" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("name colliding with a label should dedupe, got %d", n)
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

// kern-docker is the host-executor capability label for docker-in-job work:
// advertised only when the executor is host AND the daemon answers.
func TestLabelsKernDocker(t *testing.T) {
	up := func() bool { return true }
	down := func() bool { return false }

	host := &Agent{cfg: Config{Executor: "host"}, dockerFn: up}
	if !hasLabel(host.labelsFor("linux", "amd64"), "kern-docker") {
		t.Error("host executor with daemon missing kern-docker")
	}
	if hasLabel(host.labelsFor("linux", "amd64"), "docker") {
		t.Error("host executor leaked the container-execution docker label")
	}

	hostNoDaemon := &Agent{cfg: Config{Executor: "host"}, dockerFn: down}
	if hasLabel(hostNoDaemon.labelsFor("linux", "amd64"), "kern-docker") {
		t.Error("host executor without daemon advertised kern-docker")
	}

	dkr := &Agent{cfg: Config{Executor: "docker"}, dockerFn: up}
	ls := dkr.labelsFor("linux", "amd64")
	if hasLabel(ls, "kern-docker") {
		t.Error("docker executor advertised kern-docker")
	}
	if !hasLabel(ls, "docker") {
		t.Error("docker executor with daemon missing docker label")
	}
}
