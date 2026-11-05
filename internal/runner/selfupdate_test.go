package runner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dvorinka/kern/internal/update"
)

func TestPlanReplace(t *testing.T) {
	image := update.RunnerImage + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	raw := []byte(`{
		"Name": "/kern-runner",
		"Config": {
			"User": "",
			"Env": ["KERN_RUNNER_UPDATE=1", "KERN_RUNNER_JOIN_TOKEN=secret", ""],
			"Cmd": ["--control", "https://kern.example", "--name", "ci-runner-901"],
			"Entrypoint": ["kern-runner"]
		},
		"HostConfig": {
			"Binds": ["/var/run/docker.sock:/var/run/docker.sock", "/opt/kern/work:/opt/kern/work"],
			"NetworkMode": "bridge",
			"Privileged": false,
			"RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0}
		}
	}`)
	args, name, prev, err := planReplace(raw, image)
	if err != nil {
		t.Fatal(err)
	}
	if name != "kern-runner" || prev != "kern-runner-prev" {
		t.Fatalf("names %s %s", name, prev)
	}
	got := strings.Join(args, "\n")
	for _, want := range []string{
		"run\n-d\n--name\nkern-runner",
		"--restart\nunless-stopped",
		"--network\nbridge",
		"-v\n/var/run/docker.sock:/var/run/docker.sock",
		"-e\nKERN_RUNNER_JOIN_TOKEN=secret",
		"--entrypoint\nkern-runner",
		image + "\n--control\nhttps://kern.example\n--name\nci-runner-901",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "-e\n\n") {
		t.Fatal("empty env slipped through")
	}

	noSock := bytes.ReplaceAll(raw, []byte("/var/run/docker.sock"), []byte("/tmp/not-the-socket"))
	if _, _, _, err := planReplace(noSock, image); err == nil {
		t.Fatal("missing socket must be refused")
	}
	if _, _, _, err := planReplace(raw, "alpine:3"); err == nil {
		t.Fatal("foreign image must be refused")
	}
}
