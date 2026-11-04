package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertCheckRunCreatesThenUpdates(t *testing.T) {
	var created, patched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodGet:
			if !strings.Contains(r.URL.RawQuery, "check_name=kern%2FCI") {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			runs := []any{}
			if created > 0 {
				runs = []any{map[string]any{"id": 42, "external_id": "run-1"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": runs})
		case http.MethodPost:
			created++
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"external_id":"run-1"`) {
				t.Errorf("create body %s", body)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content-type %q", r.Header.Get("Content-Type"))
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodPatch:
			patched++
			if !strings.HasSuffix(r.URL.Path, "/check-runs/42") {
				t.Errorf("patch path %s", r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"conclusion":"success"`) {
				t.Errorf("patch body %s", body)
			}
			if strings.Contains(string(body), `"head_sha"`) {
				t.Errorf("patch must not resend head_sha: %s", body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	in := CheckRun{
		Name: "kern/CI", HeadSHA: "abc", Status: "in_progress",
		ExternalID: "run-1", Title: "CI", Summary: "queued", DetailsURL: "https://kern.example/ci/1",
	}
	if err := c.UpsertCheckRun(context.Background(), "acme/kern", in); err != nil {
		t.Fatal(err)
	}
	in.Status = "completed"
	in.Conclusion = "success"
	if err := c.UpsertCheckRun(context.Background(), "acme/kern", in); err != nil {
		t.Fatal(err)
	}
	if created != 1 || patched != 1 {
		t.Fatalf("created %d patched %d", created, patched)
	}
}
