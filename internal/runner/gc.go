package runner

// Runner-side garbage collection of pulled job images. Images the agent pulled
// are tracked in a small JSON ledger under WorkRoot; a sweep removes entries
// unused for longer than Config.ImageMaxAge. Only images KERN itself recorded
// are touched — never the operator's own images — and a sweep runs only while
// no job is in flight, so an active job's image can never disappear mid-run.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// --- disk hygiene ---------------------------------------------------------

const (
	diskSweepInterval = 15 * time.Minute
	staleWorkspaceAge = 24 * time.Hour
)

// diskGCLoop removes leftover per-job work dirs and, when free space on the
// work-root filesystem drops below Config.DiskMinFreePct, prunes Docker
// reclaimable space. The stale-dir sweep always runs; the Docker prune only
// fires under pressure and only while no job is in flight, so a running job's
// image layers and in-flight build cache are never pulled out from under it.
func (a *Agent) diskGCLoop(ctx context.Context) {
	a.sweepWorkspaces()
	a.sweepBoundedCache()
	a.sweepOrphanServices(ctx)
	a.sweepWorkspaceLinks()
	a.pruneDangling(ctx)
	a.maybePruneDisk(ctx)
	t := time.NewTicker(diskSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepWorkspaces()
			a.sweepBoundedCache()
			a.sweepOrphanServices(ctx)
			a.sweepWorkspaceLinks()
			a.pruneDangling(ctx)
			a.maybePruneDisk(ctx)
		}
	}
}

// sweepOrphanServices removes kern-svc-*/kern-job-* containers and
// kern-svc-* networks left behind when a runner died mid-job: cleanup()
// never ran, the containers are --rm but still running, and they squat
// host ports (postgres:5432 conflicts) and serve stale state to later
// jobs. A resource is live only while its job ID sits in activeJobs —
// one runner per docker host is the documented model (same assumption
// hostPortLocks already makes).
func (a *Agent) sweepOrphanServices(ctx context.Context) {
	if a.executor() != "docker" {
		return
	}
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "name=kern-", "--format", "{{.ID}}|{{.Names}}").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			id, name, ok := strings.Cut(line, "|")
			name = strings.TrimPrefix(strings.TrimSpace(name), "/")
			if !ok || id == "" || a.jobActive(name) ||
				!(strings.HasPrefix(name, "kern-svc-") || strings.HasPrefix(name, "kern-job-")) {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := exec.CommandContext(rctx, "docker", "rm", "-f", id).Run(); err == nil {
				a.log.Info("orphan sweep: removed container", zap.String("name", name))
			} else {
				a.log.Debug("orphan sweep: rm failed", zap.String("id", id), zap.Error(err))
			}
			cancel()
		}
	}
	out, err = exec.CommandContext(ctx, "docker", "network", "ls", "--filter", "name=kern-", "--format", "{{.Name}}").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		if a.jobActive(name) ||
			!(strings.HasPrefix(name, "kern-svc-") || strings.HasPrefix(name, "kern-ego-") || strings.HasPrefix(name, "kern-egro-")) {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := exec.CommandContext(rctx, "docker", "network", "rm", name).Run(); err == nil {
			a.log.Info("orphan sweep: removed network", zap.String("name", name))
		}
		cancel()
	}
}

// jobActive reports whether name belongs to a job currently executing on
// this runner. Container names embed the job ID (kern-svc-<id>-<svc>,
// kern-job-<id>-<attempt>); network names end with it (kern-svc-<id>).
func (a *Agent) jobActive(name string) bool {
	active := false
	a.activeJobs.Range(func(k, _ any) bool {
		if id, ok := k.(string); ok && strings.Contains(name, id) {
			active = true
			return false
		}
		return true
	})
	return active
}

// sweepWorkspaceLinks removes /github/workspaces/<job> symlinks whose target
// work dir is gone — residue from jobs whose cleanup() never ran. Live links
// are kept so a running job's docker CLI calls keep resolving.
func (a *Agent) sweepWorkspaceLinks() {
	entries, err := os.ReadDir("/github/workspaces")
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join("/github/workspaces", e.Name())
		target, err := os.Readlink(path)
		if err != nil {
			continue
		}
		// Stat the work root, not workspace itself: checkout wipes and
		// recreates workspace mid-job, and a sweep landing in that window
		// must not pull the link from under a live job.
		if _, err := os.Stat(filepath.Dir(target)); err != nil {
			_ = os.Remove(path)
			a.log.Info("workspace link sweep: removed stale link", zap.String("path", path))
		}
	}
}

// pruneDangling drops untagged image layers every sweep while idle —
// cheap, always safe, and keeps disk creep from partial pulls and
// retagged builds from accumulating between pressure events.
func (a *Agent) pruneDangling(ctx context.Context) {
	if a.executor() != "docker" || a.active.Load() > 0 {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	out, err := exec.CommandContext(pctx, "docker", "image", "prune", "-f").CombinedOutput()
	cancel()
	if err != nil {
		a.log.Debug("dangling prune failed", zap.String("docker", tailStr(string(out), 200)))
		return
	}
	if s := strings.TrimSpace(string(out)); s != "" && !strings.Contains(s, "Total reclaimed space: 0B") {
		a.log.Info("dangling prune", zap.String("out", tailStr(s, 200)))
	}
}

// sweepWorkspaces deletes non-underscore directories under WorkRoot older
// than staleWorkspaceAge — residue from jobs whose cleanup() never ran.
// _cache/_node/_cli/_tool/_actions and the image ledger are managed state and
// are never touched.
func (a *Agent) sweepWorkspaces() {
	entries, err := os.ReadDir(a.cfg.WorkRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleWorkspaceAge)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(a.cfg.WorkRoot, e.Name())
		if err := os.RemoveAll(path); err != nil {
			a.log.Warn("workspace sweep: remove failed", zap.String("path", path), zap.Error(err))
			continue
		}
		a.log.Info("workspace sweep: removed stale dir", zap.String("path", path))
	}
}

// sweepBoundedCache enforces Config.CacheMaxBytes on the _cache tree. Files
// are evicted oldest-mtime-first; every resident is a rebuildable cache entry
// (language caches, _toolcache downloads, _actions checkouts), so removal is
// always safe — a miss just costs a re-download or recompile. Skipped while a
// job is active so an in-flight build never loses its warm cache mid-step.
func (a *Agent) sweepBoundedCache() {
	limit := a.cfg.CacheMaxBytes
	if limit <= 0 || a.active.Load() > 0 {
		return
	}
	root := filepath.Join(a.cfg.WorkRoot, "_cache")
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var files []entry
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, entry{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil || total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	var freed int64
	for _, f := range files {
		if total <= limit {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
		freed += f.size
	}
	if freed > 0 {
		a.log.Info("cache bound: evicted oldest entries",
			zap.Int64("freed_bytes", freed), zap.Int64("remaining_bytes", total),
			zap.Int64("cap_bytes", limit))
	}
}

func (a *Agent) maybePruneDisk(ctx context.Context) {
	if a.executor() != "docker" {
		return // host mode has no daemon to prune
	}
	pct := a.cfg.DiskMinFreePct
	if pct <= 0 {
		return
	}
	free, total := diskSpace(a.cfg.WorkRoot)
	if !diskUnderPressure(free, total, pct) {
		return
	}
	if a.active.Load() > 0 {
		a.log.Warn("disk pressure: jobs active, docker prune deferred",
			zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
			zap.Float64("min_free_pct", pct))
		return
	}
	a.log.Warn("disk pressure: pruning docker",
		zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
		zap.Float64("min_free_pct", pct))
	prune := [][]string{
		{"builder", "prune", "-af"},  // all reclaimable build cache
		{"container", "prune", "-f"}, // stopped containers only
		{"image", "prune", "-f"},     // dangling images only; tagged job images stay
		{"network", "prune", "-f"},   // unused networks
	}
	for _, args := range prune {
		ctxCmd, cancel := context.WithTimeout(ctx, 2*time.Minute)
		out, err := exec.CommandContext(ctxCmd, "docker", args...).CombinedOutput()
		cancel()
		if err != nil {
			a.log.Warn("disk prune step failed", zap.Strings("args", args), zap.String("docker", tailStr(string(out), 200)))
			continue
		}
		a.log.Info("disk prune", zap.Strings("args", args), zap.String("out", tailStr(strings.TrimSpace(string(out)), 200)))
	}
	if free2, _ := diskSpace(a.cfg.WorkRoot); free2 > free {
		a.log.Info("disk prune complete",
			zap.Int64("reclaimed_bytes", free2-free),
			zap.Int64("free_before", free), zap.Int64("free_after", free2))
	}
}

// diskUnderPressure reports whether free space is below pct percent of total.
// pct <= 0 disables the check; total == 0 means statfs failed.
func diskUnderPressure(free, total int64, pct float64) bool {
	return pct > 0 && total > 0 && float64(free)*100 < float64(total)*pct
}

// prepullLoop pulls Config.Prepull images at startup and hourly so job image
// pulls hit the local daemon instead of the registry. Best effort: failures
// are retried next tick and never block claiming.
func (a *Agent) prepullLoop(ctx context.Context) {
	a.prepull(ctx)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.prepull(ctx)
		}
	}
}

func (a *Agent) prepull(ctx context.Context) {
	for _, image := range a.cfg.Prepull {
		ctxCmd, cancel := context.WithTimeout(ctx, 10*time.Minute)
		out, err := exec.CommandContext(ctxCmd, "docker", "pull", image).CombinedOutput()
		cancel()
		if err != nil {
			a.log.Warn("prepull failed", zap.String("image", image), zap.String("docker", tailStr(string(out), 200)))
			continue
		}
		recordImageUse(a.cfg.WorkRoot, image)
		a.log.Info("prepull: image ready", zap.String("image", image))
	}
}
