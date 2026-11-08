package orchestrator

import (
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func TestJobNeedsDocker(t *testing.T) {
	cases := []struct {
		name  string
		steps []workflows.StepPlan
		want  bool
	}{
		{"no steps", nil, false},
		{"plain run", []workflows.StepPlan{{Run: "go test ./..."}}, false},
		{"docker build", []workflows.StepPlan{{Run: "docker build -t app ."}}, true},
		{"docker compose", []workflows.StepPlan{{Run: "docker compose up -d"}}, true},
		{"docker-compose binary", []workflows.StepPlan{{Run: "docker-compose up"}}, true},
		{"docker mid-script", []workflows.StepPlan{{Run: "set -e\napt-get install -y git\ndocker login ghcr.io\ndocker push img"}}, true},
		{"docker action", []workflows.StepPlan{{Uses: "docker/build-push-action@v6"}}, true},
		{"docker login action", []workflows.StepPlan{{Uses: "docker/login-action@v3"}}, true},
		{"setup buildx", []workflows.StepPlan{{Uses: "docker/setup-buildx-action@v3"}}, true},
		{"non-docker action", []workflows.StepPlan{{Uses: "actions/checkout@v4"}}, false},
		{"docker in prose only", []workflows.StepPlan{{Run: "echo 'we ship docker images'"}}, true}, // false positive is harmless
		{"second step hits", []workflows.StepPlan{{Run: "make"}, {Run: "docker build ."}}, true},
		{"uses docker uri not counted", []workflows.StepPlan{{Uses: "docker://alpine:3"}}, false},
	}
	for _, tc := range cases {
		if got := jobNeedsDocker(tc.steps); got != tc.want {
			t.Errorf("%s: jobNeedsDocker = %v, want %v", tc.name, got, tc.want)
		}
	}
}
