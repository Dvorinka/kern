package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// RunLocalJob executes one job claim entirely in-process (#143, kern
// testbox): an httptest shim stands in for the runner-facing API so the
// real executor path runs unmodified — same container, same step wrapper,
// same log pipeline — with no control plane involved. Step logs stream to
// logOut; uploaded artifacts land in artifactsDir; cache restores always
// miss; ghcache calls 404 (actions degrade to warnings).
//
// Source comes from bundlePath, a tar.gz of the working tree — the same
// bundle kern exec uploads — fetched through the /source endpoint.
func RunLocalJob(ctx context.Context, cfg Config, claim *Claim, secrets, vars map[string]string, log *zap.Logger, logOut io.Writer, artifactsDir, bundlePath string) (string, error) {
	srv := httptest.NewServer(localShim(logOut, artifactsDir, bundlePath))
	defer srv.Close()

	cl := newClient(srv.URL, "")
	exec, err := newJobExecutor(cfg, cl, claim, secrets, vars, log)
	if err != nil {
		return "", err
	}
	defer exec.cleanup()

	if err := exec.prepare(ctx); err != nil {
		return "", err
	}
	exec.runPreHooks(ctx)

	jobStatus := "success"
	for _, step := range claim.Steps {
		if ctx.Err() != nil {
			return "cancelled", ctx.Err()
		}
		conclusion, stepErr := exec.runStep(ctx, step)
		if stepErr != nil {
			return "", stepErr
		}
		switch conclusion {
		case "failure":
			jobStatus = "failure"
		case "cancelled":
			jobStatus = "cancelled"
		}
		if conclusion == "cancelled" {
			break
		}
	}
	if jobStatus != "cancelled" {
		if !exec.runPostHooks(ctx) {
			jobStatus = "failure"
		}
		exec.saveCaches()
	}
	return jobStatus, nil
}

// localShim answers every runner-facing route the executor can call, keyed
// by path suffix so the job-id segment does not need parsing.
func localShim(logOut io.Writer, artifactsDir, bundlePath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/source"):
			http.ServeFile(w, r, bundlePath)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/logs"):
			_, _ = io.Copy(logOut, r.Body)
			writeLocalJSON(w, map[string]any{"ok": true})
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/heartbeat"):
			writeLocalJSON(w, map[string]any{"cancelled": false})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/cancel-check"):
			writeLocalJSON(w, map[string]any{"cancelled": false})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/secrets"):
			writeLocalJSON(w, map[string]any{"secrets": map[string]string{}, "vars": map[string]string{}, "blocked": false})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/artifacts"):
			names := []string{}
			entries, _ := os.ReadDir(artifactsDir)
			for _, e := range entries {
				names = append(names, e.Name())
			}
			writeLocalJSON(w, map[string]any{"artifacts": names})
		case r.Method == http.MethodPut && strings.Contains(p, "/artifacts/"):
			name := filepath.Base(p)
			if err := os.MkdirAll(artifactsDir, 0o755); err == nil {
				if data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20)); err == nil {
					_ = os.WriteFile(filepath.Join(artifactsDir, name), data, 0o644)
				}
			}
			writeLocalJSON(w, map[string]any{"ok": true})
		case r.Method == http.MethodGet && strings.Contains(p, "/artifacts/"):
			name := filepath.Base(p)
			http.ServeFile(w, r, filepath.Join(artifactsDir, name))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/cache/restore"):
			http.NotFound(w, r)
		case r.Method == http.MethodPut && strings.Contains(p, "/cache/"):
			writeLocalJSON(w, map[string]any{"ok": true})
		case strings.Contains(p, "/ghcache/"):
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/start") || strings.HasSuffix(p, "/finish") || strings.HasSuffix(p, "/summary"):
			writeLocalJSON(w, map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeLocalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
