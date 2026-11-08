package main

import (
	"encoding/json"
	"testing"
)

func TestNeedsLogTail(t *testing.T) {
	diag, _ := json.Marshal(map[string]string{"kind": "test", "summary": "s"})
	cases := []struct {
		name string
		step step
		want bool
	}{
		{"failure", step{Conclusion: "failure"}, true},
		{"cancelled", step{Conclusion: "cancelled"}, true},
		{"timed_out", step{Conclusion: "timed_out"}, true},
		{"success", step{Conclusion: "success"}, false},
		{"skipped", step{Conclusion: "skipped"}, false},
		{"running no diag", step{Status: "running"}, false},
		{"success with diagnosis", step{Conclusion: "success", Diagnosis: diag}, true},
		{"failure with null diagnosis", step{Conclusion: "failure", Diagnosis: json.RawMessage("null")}, true},
	}
	for _, tc := range cases {
		if got := needsLogTail(tc.step); got != tc.want {
			t.Errorf("%s: needsLogTail = %v, want %v", tc.name, got, tc.want)
		}
	}
}
