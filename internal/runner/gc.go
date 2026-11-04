package runner

// Runner-side garbage collection of pulled job images. Images the agent pulled
// are tracked in a small JSON ledger under WorkRoot; a sweep removes entries
// unused for longer than Config.ImageMaxAge. Only images KERN itself recorded
// are touched — never the operator's own images — and a sweep runs only while
// no job is in flight, so an active job's image can never disappear mid-run.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

const imageLedgerName = "image-usage.json"

func imageLedgerPath(workRoot string) string {
	return filepath.Join(workRoot, imageLedgerName)
}

// recordImageUse notes that image was just pulled for a job. Best effort: a
// lost ledger entry only means the image is never GC'd.
func recordImageUse(workRoot, image string) {
	ledger := loadImageLedger(workRoot)
	ledger[image] = time.Now().Unix()
	data, err := json.Marshal(ledger)
	if err != nil {
		return
	}
	_ = os.WriteFile(imageLedgerPath(workRoot), data, 0o640)
}

func loadImageLedger(workRoot string) map[string]int64 {
	out := map[string]int64{}
	data, err := os.ReadFile(imageLedgerPath(workRoot))
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// imageGCLoop sweeps on an interval and once at startup, skipping periods when
// any job is active. ImageMaxAge <= 0 disables collection entirely.
func (a *Agent) imageGCLoop(ctx context.Context) {
	if a.cfg.ImageMaxAge <= 0 {
		return
	}
	a.sweepImages(ctx)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepImages(ctx)
		}
	}
}

func (a *Agent) sweepImages(ctx context.Context) {
	if a.active.Load() > 0 {
		return // never pull an image out from under a running job
	}
	cutoff := time.Now().Add(-a.cfg.ImageMaxAge).Unix()
	ledger := loadImageLedger(a.cfg.WorkRoot)
	dirty := false
	for image, usedAt := range ledger {
		if usedAt >= cutoff {
			continue
		}
		dirty = true
		ctxCmd, cancel := context.WithTimeout(ctx, 60*time.Second)
		out, err := exec.CommandContext(ctxCmd, "docker", "rmi", image).CombinedOutput()
		cancel()
		if err != nil {
			// Still referenced by a container, or already gone — drop the
			// ledger line for gone images, keep retrying referenced ones.
			ictx, icancel := context.WithTimeout(ctx, 15*time.Second)
			gone := exec.CommandContext(ictx, "docker", "image", "inspect", image).Run() != nil
			icancel()
			if gone {
				delete(ledger, image)
				continue
			}
			a.log.Debug("image gc skipped", zap.String("image", image), zap.String("docker", tailStr(string(out), 200)))
			continue
		}
		a.log.Info("image gc: removed", zap.String("image", image))
		delete(ledger, image)
	}
	if dirty {
		if data, err := json.Marshal(ledger); err == nil {
			_ = os.WriteFile(imageLedgerPath(a.cfg.WorkRoot), data, 0o640)
		}
	}
}
