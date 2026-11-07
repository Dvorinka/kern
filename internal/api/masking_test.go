package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dvorinka/kern/internal/secrets"
)

// TestMaskDiagnosisRecursive verifies every string in a diagnosis blob —
// including nested detail/remediation fields that carry raw log tails — is
// masked before persistence.
func TestMaskDiagnosisRecursive(t *testing.T) {
	m := secrets.NewMasker([]string{"s3cr3t-value", "ghp_token123"})
	raw := json.RawMessage(`{
		"kind": "test_failure",
		"summary": "tests failed",
		"detail": "output contained s3cr3t-value and ghp_token123",
		"agent_prompt": {"nested": ["s3cr3t-value", {"deep": "ghp_token123"}]},
		"remediation": {"applied": true, "note": "s3cr3t-value"},
		"count": 3
	}`)
	out := maskDiagnosis(raw, m)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("masked diagnosis not JSON: %v", err)
	}
	text := string(out)
	for _, secret := range []string{"s3cr3t-value", "ghp_token123"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q survived masking in %s", secret, text)
		}
	}
	if v["kind"] != "test_failure" || v["count"] != float64(3) {
		t.Fatalf("non-string fields mangled: %v", v)
	}
}

func TestMaskDiagnosisPassthrough(t *testing.T) {
	m := secrets.NewMasker([]string{"x"})
	for _, in := range []string{"", "null", "not-json", `"plain"`} {
		if got := maskDiagnosis(json.RawMessage(in), m); string(got) != in {
			t.Fatalf("maskDiagnosis(%q) = %q, want passthrough", in, got)
		}
	}
}
