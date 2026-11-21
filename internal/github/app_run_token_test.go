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

// TestRunTokenPermissionNormalization verifies Actions-style hyphenated
// permission names reach GitHub in REST underscore spelling, and that an
// explicit empty permissions object mints a zero-scope token rather than
// falling back to installation defaults (#368).
func TestRunTokenPermissionNormalization(t *testing.T) {
	_, pemStr := testKey(t)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/installation":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 9002,
				"permissions": map[string]string{
					"contents": "read", "pull_requests": "read",
				},
			})
		case "/app/installations/9002/access_tokens":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_tok",
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

	// Hyphenated workflow syntax must normalize before the ceiling lookup —
	// "pull-requests" would otherwise miss the installation's "pull_requests"
	// grant and silently drop the scope.
	if _, _, err := app.RunToken(ctx, "o/r", map[string]string{"pull-requests": "read"}); err != nil {
		t.Fatalf("RunToken hyphenated: %v", err)
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if perms["pull_requests"] != "read" {
		t.Fatalf("permissions = %v, want pull_requests=read", gotBody["permissions"])
	}
	if _, has := perms["pull-requests"]; has {
		t.Fatalf("hyphenated key leaked to GitHub: %v", perms)
	}

	// permissions: {} is an explicit zero-scope request — the empty object
	// must be sent, not treated as "no permissions block".
	gotBody = nil
	if _, scoped, err := app.RunToken(ctx, "o/r", map[string]string{}); err != nil || !scoped {
		t.Fatalf("RunToken({}): scoped=%v err=%v", scoped, err)
	}
	perms, hasPerms := gotBody["permissions"].(map[string]any)
	if !hasPerms || len(perms) != 0 {
		t.Fatalf("permissions = %v (present=%v), want empty object", gotBody["permissions"], hasPerms)
	}
}

// TestCloneTokenScope verifies the clone credential is pinned to one
// repository with contents:read — never the installation's full grant set.
func TestCloneTokenScope(t *testing.T) {
	_, pemStr := testKey(t)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/installation":
			json.NewEncoder(w).Encode(map[string]any{"id": 9003})
		case "/app/installations/9003/access_tokens":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_clone",
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
	tok, err := app.CloneToken(context.Background(), "o/r")
	if err != nil || tok != "ghs_clone" {
		t.Fatalf("CloneToken = %q, %v", tok, err)
	}
	if repos, _ := gotBody["repositories"].([]any); len(repos) != 1 || repos[0] != "r" {
		t.Fatalf("repositories = %v, want [r]", gotBody["repositories"])
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("permissions = %v, want {contents: read}", gotBody["permissions"])
	}
}
