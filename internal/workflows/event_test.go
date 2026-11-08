package workflows

import "testing"

func TestEventPayloadWorkflowRun(t *testing.T) {
	source := map[string]any{
		"action":         "completed",
		"wr_id":          float64(42),
		"wr_name":        "CI",
		"wr_path":        ".github/workflows/ci.yml",
		"wr_event":       "push",
		"wr_status":      "completed",
		"wr_conclusion":  "success",
		"wr_head_branch": "main",
		"wr_head_sha":    "abc123",
		"wr_number":      float64(7),
		"wr_attempt":     float64(1),
		"wr_url":         "https://github.com/o/r/actions/runs/42",
	}
	p := EventPayload("workflow_run", "refs/heads/main", "deadbeef", "octo", "", "o/r", "", "main", source)
	if p["action"] != "completed" {
		t.Fatalf("action = %v", p["action"])
	}
	wr, ok := p["workflow_run"].(map[string]any)
	if !ok {
		t.Fatalf("workflow_run missing: %#v", p)
	}
	if wr["conclusion"] != "success" || wr["name"] != "CI" || wr["head_sha"] != "abc123" {
		t.Fatalf("workflow_run = %#v", wr)
	}
}
