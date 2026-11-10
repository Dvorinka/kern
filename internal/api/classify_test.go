package api

import "testing"

func TestClassifyJobFailure(t *testing.T) {
	for _, tc := range []struct {
		kinds  []string
		jobErr string
		want   string
	}{
		{[]string{"script_failure", "network"}, "anything", "script_failure"},
		{[]string{"network"}, "", "network"},
		{nil, "mkdir /work: no space left on device", "disk_full"},
		{nil, "runner disk full: 42 MiB free", "disk_full"},
		{nil, "job workspace exceeded disk_quota", "disk_full"},
		{nil, "pull image: i/o timeout", "network"},
		{nil, "registry: status code 429", "network"},
		{nil, "job timed out", "timeout"},
		{nil, "runner panic: runtime error", "infra"},
		{nil, "checkout failed: repository not found", "prepare"},
		{nil, "", "prepare"},
	} {
		if got := classifyJobFailure(tc.kinds, tc.jobErr); got != tc.want {
			t.Errorf("classifyJobFailure(%v, %q) = %q, want %q", tc.kinds, tc.jobErr, got, tc.want)
		}
	}
}
