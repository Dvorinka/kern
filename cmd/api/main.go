// kern-api boots the control plane: config, database, migrations, bootstrap
// org/user, blobs, secrets, orchestrator and the scheduler loop, then serves HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/api"
	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/orchestrator"
	"github.com/dvorinka/kern/internal/secrets"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/telemetry"
	"github.com/dvorinka/kern/internal/workflows"
)

type exitCode struct{ n int }

func main() {
	if code := run(); code != 0 {
		os.Exit(code)
	}
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-api: config:", err)
		return 2
	}
	log, err := telemetry.NewLogger(cfg.Env, "api")
	if err != nil {
		fmt.Fprintln(os.Stderr, "kern-api: logger:", err)
		return 2
	}
	defer log.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database unavailable", zap.String("dsn", redact(cfg.DatabaseURL)), zap.Error(err))
		return 1
	}
	defer st.Close()

	if err := st.Migrate(ctx, cfg.DatabaseURL); err != nil {
		log.Error("migrations failed", zap.Error(err))
		return 1
	}

	hash, err := auth.HashPassword(cfg.BootstrapPass)
	if err != nil {
		log.Error("bootstrap password", zap.Error(err))
		return 1
	}
	if _, err := st.Bootstrap(ctx, cfg.BootstrapOrg, cfg.BootstrapOrg,
		cfg.BootstrapEmail, "Admin", hash); err != nil {
		log.Error("bootstrap org", zap.String("org", cfg.BootstrapOrg), zap.Error(err))
		return 1
	}

	blobStore, err := blobs.NewFilesystem(cfg.BlobDir)
	if err != nil {
		log.Error("blob store", zap.String("dir", cfg.BlobDir), zap.Error(err))
		return 1
	}
	cipher, err := secrets.New(cfg.SecretKey)
	if err != nil {
		log.Error("secret cipher", zap.Error(err))
		return 1
	}

	compiler := workflows.Compiler{
		DefaultImage: cfg.DefaultImage,
		LabelImages:  map[string]string{"ubuntu-latest": cfg.DefaultImage, "ubuntu-24.04": cfg.DefaultImage},
	}
	orch := orchestrator.New(st, log, compiler)
	orch.MaxAttempts = cfg.MaxJobAttempts
	srv := api.New(cfg, st, orch, blobStore, cipher, log)

	// The scheduler advances gated runs between job completions.
	go schedulerLoop(ctx, cfg, orch, log)

	httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Engine, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("kern api listening", zap.String("addr", cfg.HTTPAddr), zap.String("env", cfg.Env))
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		log.Error("http server failed", zap.Error(err))
		return 1
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return 0
}

func schedulerLoop(ctx context.Context, cfg *config.Config, orch *orchestrator.Orchestrator, log *zap.Logger) {
	ticker := time.NewTicker(cfg.SchedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			orch.Tick(tctx)
			cancel()
		}
	}
}

// redact strips credentials from a DSN before it reaches the log.
func redact(dsn string) string {
	pre, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return dsn
	}
	body, host, cut := strings.Cut(rest, "@")
	if !cut {
		return dsn
	}
	user, _, _ := strings.Cut(body, ":")
	return pre + "://" + user + ":*@" + host
}
