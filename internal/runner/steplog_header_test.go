package runner

import "testing"

func TestRunStepHeader(t *testing.T) {
	if got := runStepHeader(StepSpec{}); got != "" {
		t.Fatalf("empty run script must not emit a header, got %q", got)
	}
	got := runStepHeader(StepSpec{Run: "go build ./...\ngo vet ./..."})
	want := "##[group]Run go build ./...\ngo build ./...\ngo vet ./...\n##[endgroup]\n"
	if got != want {
		t.Fatalf("header mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestUsesStepHeader(t *testing.T) {
	got := usesStepHeader(StepSpec{Uses: "actions/cache@v4", With: map[string]any{
		"path": "~/go/pkg/mod", "key": "gomod-{{ hashFiles('go.sum') }}",
	}})
	want := "##[group]Run actions/cache@v4\nwith:\n  key: gomod-{{ hashFiles('go.sum') }}\n  path: ~/go/pkg/mod\n##[endgroup]\n"
	if got != want {
		t.Fatalf("header mismatch:\n got %q\nwant %q", got, want)
	}
}
