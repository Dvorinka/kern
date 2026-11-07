package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRunTokenCeiling verifies the minted token's permission set is the
// intersection of the workflow's request and the installation's grants,
// clamped downward — never widened.
func TestRunTokenCeiling(t *testing.T) {
	_, pemStr := testKey(t)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/installation":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 9001,
				"permissions": map[string]string{
					"contents":      "read",
					"pull_requests": "write",
					"issues":        "none",
				},
			})
		case "/app/installations/9001/access_tokens":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_run_token",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	app, err := NewApp("42", pemStr, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tok, scoped, err := app.RunToken(ctx, "o/r", map[string]string{
		"contents":      "write", // installation grants read only → clamp
		"pull_requests": "read",  // narrower than grant → keep
		"issues":        "write", // installation grants none → drop
		"admin":         "write", // not granted at all → drop
		"metadata":      "read",  // not in installation map → drop? see below
	})
	if err != nil {
		t.Fatalf("RunToken: %v", err)
	}
	if tok != "ghs_run_token" || !scoped {
		t.Fatalf("token=%q scoped=%v", tok, scoped)
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	want := map[string]string{"contents": "read", "pull_requests": "read"}
	if len(perms) != len(want) {
		t.Fatalf("permissions = %v, want %v", perms, want)
	}
	for k, v := range want {
		if perms[k] != v {
			t.Fatalf("permissions[%q] = %v, want %q (all: %v)", k, perms[k], v, perms)
		}
	}
	if repos, _ := gotBody["repositories"].([]any); len(repos) != 1 || repos[0] != "r" {
		t.Fatalf("repositories = %v, want [r]", gotBody["repositories"])
	}

	// A ceiling that grants nothing the installation holds is an error,
	// not a silent full-access token.
	if _, _, err := app.RunToken(ctx, "o/r", map[string]string{"secrets": "write"}); err == nil {
		t.Fatal("RunToken with ungrantable ceiling should fail")
	}

	// No permissions block → token carries installation defaults.
	gotBody = nil
	if _, scoped, err := app.RunToken(ctx, "o/r", nil); err != nil || scoped {
		t.Fatalf("RunToken(nil): scoped=%v err=%v", scoped, err)
	}
	if _, has := gotBody["permissions"]; has {
		t.Fatalf("empty request must not send permissions, got %v", gotBody["permissions"])
	}
}
