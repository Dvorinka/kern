package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNormalizeServerURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"kern.tdvorak.dev", "https://kern.tdvorak.dev"},
		{"https://kern.tdvorak.dev/api/v1/", "https://kern.tdvorak.dev"},
		{"localhost:8080", "http://localhost:8080"},
		{"127.0.0.1:18080/", "http://127.0.0.1:18080"},
		{"kern.local:8080", "http://kern.local:8080"},
		{"http://example.com:8080/api/v1", "http://example.com:8080"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeServerURL(tc.in); got != tc.want {
			t.Errorf("normalizeServerURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConfigMultiContextAndLegacyMigration(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("KERN_SERVER", "")
	t.Setenv("KERN_URL", "")
	t.Setenv("KERN_TOKEN", "")
	t.Setenv("KERN_PROJECT", "")
	t.Setenv("KERN_CONTEXT", "")

	// Legacy flat config migrates cleanly into contexts on normalize.
	legacy := configFile{Server: "https://old.example.com", Token: "kern_oldtok", Project: "demo"}
	if err := saveConfig(legacy); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	loaded := loadConfig()
	if loaded.CurrentContext != "old.example.com" {
		t.Fatalf("current_context = %q, want old.example.com", loaded.CurrentContext)
	}
	if loaded.Contexts["old.example.com"].Server != "https://old.example.com" {
		t.Fatalf("migrated server = %q", loaded.Contexts["old.example.com"].Server)
	}

	// Add a second context and switch between them.
	loaded.upsertContext("local", contextConfig{
		Server: "localhost:8080", Token: "kern_localtok", Email: "dev@local",
	}, true)
	if err := saveConfig(loaded); err != nil {
		t.Fatalf("saveConfig 2: %v", err)
	}

	c := &cli{cfg: loadConfig()}
	if err := c.resolve(); err != nil {
		t.Fatalf("resolve local: %v", err)
	}
	if c.server != "http://localhost:8080" || c.token != "kern_localtok" {
		t.Fatalf("resolved local = (%q, %q)", c.server, c.token)
	}

	// Target the first context via --context without switching default.
	c2 := &cli{cfg: loadConfig(), context: "old.example.com"}
	if err := c2.resolve(); err != nil {
		t.Fatalf("resolve old: %v", err)
	}
	if c2.server != "https://old.example.com" || c2.project != "demo" {
		t.Fatalf("resolved old = (%q, %q)", c2.server, c2.project)
	}
}

func TestExtractGlobalsShortFlags(t *testing.T) {
	c := &cli{}
	rest := extractGlobals([]string{"-u", "kern.example.com", "-c", "prod", "-p", "kern", "-j", "runs", "list"}, c)
	if c.server != "kern.example.com" || c.context != "prod" || c.project != "kern" || !c.json {
		t.Fatalf("globals not extracted: server=%q ctx=%q proj=%q json=%v", c.server, c.context, c.project, c.json)
	}
	if len(rest) != 2 || rest[0] != "runs" || rest[1] != "list" {
		t.Fatalf("rest = %v", rest)
	}

	// -c after `ssh` must stay with ssh (command flag), not become --context.
	cSSH := &cli{}
	restSSH := extractGlobals([]string{"ssh", "job-1", "-c", "ls -la"}, cSSH)
	if cSSH.context != "" || len(restSSH) != 4 {
		t.Fatalf("ssh -c stolen: context=%q rest=%v", cSSH.context, restSSH)
	}
}

func TestSuggestCommand(t *testing.T) {
	if got := suggestCommand("rnus", topLevelCommands); got != "runs" {
		t.Fatalf("suggestCommand(rnus) = %q, want runs", got)
	}
	if got := suggestCommand("contxt", topLevelCommands); got != "context" {
		t.Fatalf("suggestCommand(contxt) = %q, want context", got)
	}
	if got := suggestCommand("zzzzzzzz", topLevelCommands); got != "" {
		t.Fatalf("suggestCommand(zzzzzzzz) = %q, want empty", got)
	}
}

func TestBrowserLoginFlowEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("KERN_SERVER", "")
	t.Setenv("KERN_URL", "")
	t.Setenv("KERN_TOKEN", "")
	t.Setenv("KERN_CONTEXT", "")

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/meta":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "KERN", "version": "v0.19.3"})
		case "/api/v1/auth/cli/start":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-secret-123",
				"user_code":        "ABCD-2345",
				"verification_url": "http://example.test/auth/cli?code=ABCD-2345",
				"expires_in":       60,
				"interval":         1,
			})
		case "/api/v1/auth/cli/poll":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "approved",
				"token":  "kern_browser_token_123456",
				"record": map[string]any{"id": "tok-1", "prefix": "kern_browser"},
				"user":   map[string]any{"email": "operator@kern.local", "role": "owner"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &cli{cfg: loadConfig(), json: true}
	err := c.cmdLogin(context.Background(), []string{"--url", srv.URL, "--web", "--no-browser", "--context", "test-srv"})
	if err != nil {
		t.Fatalf("cmdLogin browser flow: %v", err)
	}
	saved := loadConfig()
	if saved.CurrentContext != "test-srv" {
		t.Fatalf("current_context = %q, want test-srv", saved.CurrentContext)
	}
	cc := saved.Contexts["test-srv"]
	if cc.Token != "kern_browser_token_123456" || cc.Email != "operator@kern.local" || cc.Server != srv.URL {
		t.Fatalf("saved context mismatch: %+v", cc)
	}
}
