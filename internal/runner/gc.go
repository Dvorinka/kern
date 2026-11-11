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
	a.sweepImagesOlder(ctx, a.cfg.ImageMaxAge)
}

// sweepImagesOlder removes ledger-tracked images unused for longer than
// maxAge — the same rules as sweepImages but at the caller's horizon.
func (a *Agent) sweepImagesOlder(ctx context.Context, maxAge time.Duration) {
	if a.active.Load() > 0 {
		return // never pull an image out from under a running job
	}
	cutoff := time.Now().Add(-maxAge).Unix()
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
	a.sweepToolcacheIncomplete()
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
			a.sweepToolcacheIncomplete()
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
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
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
	a.sweepWorkspacesOlderThan(staleWorkspaceAge)
}

// sweepWorkspacesOlderThan is sweepWorkspaces at the caller's horizon —
// critical disk pressure tightens the stale age.
func (a *Agent) sweepWorkspacesOlderThan(age time.Duration) {
	entries, err := os.ReadDir(a.cfg.WorkRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-age)
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

// sweepBoundedCache enforces Config.CacheMaxBytes on the _cache tree.
// Eviction is by atomic unit, not loose files: removing part of an
// extracted module, installed tool, or action checkout corrupts it — go
// stamps module files with uniformly old mtimes, so file-level eviction
// shredded gomod trees first (missing .go/.s files under intact dirs).
// Every unit is a rebuildable cache entry; a miss just costs a
// re-download or recompile. Skipped while a job is active so an
// in-flight build never loses its warm cache mid-step.
func (a *Agent) sweepBoundedCache() {
	a.sweepBoundedCacheTo(a.cfg.CacheMaxBytes)
}

// sweepBoundedCacheTo is sweepBoundedCache against the caller's floor —
// critical disk pressure evicts to a tighter bound than the daily cap.
func (a *Agent) sweepBoundedCacheTo(limit int64) {
	if limit <= 0 || a.active.Load() > 0 {
		return
	}
	root := filepath.Join(a.cfg.WorkRoot, "_cache")
	type unit struct {
		path string
		dir  bool
		size int64
		mod  time.Time
	}
	units := map[string]*unit{}
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		upath, udir := cacheEvictionUnit(root, rel)
		u := units[upath]
		if u == nil {
			u = &unit{path: upath, dir: udir}
			units[upath] = u
		}
		u.size += info.Size()
		if info.ModTime().After(u.mod) {
			u.mod = info.ModTime()
		}
		total += info.Size()
		return nil
	})
	if err != nil || total <= limit {
		return
	}
	list := make([]*unit, 0, len(units))
	for _, u := range units {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod.Before(list[j].mod) })
	var freed int64
	for _, u := range list {
		if total <= limit {
			break
		}
		var err error
		if u.dir {
			err = os.RemoveAll(u.path)
			// toolcache done-markers sit beside the install dir.
			_ = os.Remove(u.path + ".complete")
		} else {
			err = os.Remove(u.path)
		}
		if err != nil {
			continue
		}
		total -= u.size
		freed += u.size
	}
	if freed > 0 {
		a.log.Info("cache bound: evicted oldest entries",
			zap.Int64("freed_bytes", freed), zap.Int64("remaining_bytes", total),
			zap.Int64("cap_bytes", limit))
	}
}

// sweepToolcacheIncomplete removes _cache/_toolcache/<tool>/<ver> dirs that
// carry no done-marker. A fetch or unpack interrupted mid-install (disk
// pressure, cache GC, container kill) leaves a skeleton that setup-* actions
// still report as cached — observed on ci-runner-906, whose empty
// Java_Temurin-Hotspot_jdk dir failed every setup-java job with
// missing_binary: java until fixed by hand. Idle-gated like the other
// mutating sweeps: an in-flight install is markerless by definition, so the
// sweep must never race a running job.
func (a *Agent) sweepToolcacheIncomplete() {
	if a.active.Load() > 0 {
		return
	}
	root := filepath.Join(a.cfg.WorkRoot, "_cache", "_toolcache")
	tools, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, tool := range tools {
		if !tool.IsDir() {
			continue
		}
		tdir := filepath.Join(root, tool.Name())
		vers, err := os.ReadDir(tdir)
		if err != nil {
			continue
		}
		for _, ver := range vers {
			if !ver.IsDir() {
				continue
			}
			vdir := filepath.Join(tdir, ver.Name())
			if toolcacheComplete(vdir) {
				continue
			}
			if err := os.RemoveAll(vdir); err != nil {
				a.log.Warn("toolcache sweep: remove failed", zap.String("path", vdir), zap.Error(err))
				continue
			}
			a.log.Info("toolcache sweep: removed incomplete install", zap.String("path", vdir))
		}
		// A <tool> dir left holding only markerless versions still reads as
		// installed to actions — drop it once nothing complete remains.
		if rest, err := os.ReadDir(tdir); err == nil && len(rest) == 0 {
			if err := os.Remove(tdir); err == nil {
				a.log.Info("toolcache sweep: removed empty tool dir", zap.String("path", tdir))
			}
		}
	}
}

// toolcacheComplete reports whether a <tool>/<ver> install dir carries a
// done-marker: either an arch marker file inside (<ver>/<arch>.complete) or
// the arch-less convention's sibling (<ver>.complete).
func toolcacheComplete(vdir string) bool {
	if _, err := os.Stat(vdir + ".complete"); err == nil {
		return true
	}
	kids, err := os.ReadDir(vdir)
	if err != nil {
		return false
	}
	for _, k := range kids {
		if !k.IsDir() && strings.HasSuffix(k.Name(), ".complete") {
			return true
		}
	}
	return false
}

// cacheEvictionUnit maps a file under the persistent _cache tree to the
// smallest subtree that may be removed atomically. Tree sections return
// their entry dir (dir=true); flat content-addressed sections and loose
// files return the file itself.
//
//   - gomod: <mod>@<ver> extracted trees and per-module @v download dirs
//     are atomic — go assumes an existing module dir is complete.
//   - _toolcache/<tool>/<ver>: install trees; sibling <ver>.complete
//     markers resolve to the same unit.
//   - _actions/<entry>, ms-playwright/<entry>: one checkout/install per
//     direct child.
//   - cargo/registry/src/<index>/<crate>-<ver>: extracted crate trees —
//     cargo re-extracts on a miss. cargo/git/{db,checkouts}/<repo>: one
//     repo per unit. cargo/registry/{cache,index} entries are
//     content-addressed — file-level, a miss is a re-download.
//   - gocache, pnpm-store, gotmp, gopath: content-addressed flat files —
//     a missing entry is a plain cache miss.
//   - unknown sections: direct children evict whole — coarse but always
//     safe; add a case if finer granularity is needed.
func cacheEvictionUnit(root, rel string) (string, bool) {
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 1 {
		return filepath.Join(root, rel), false
	}
	switch parts[0] {
	case "gomod":
		for i := 1; i < len(parts)-1; i++ {
			if strings.Contains(parts[i], "@") {
				return filepath.Join(append([]string{root}, parts[:i+1]...)...), true
			}
		}
	case "_toolcache":
		if len(parts) >= 3 {
			ver := strings.TrimSuffix(parts[2], ".complete")
			return filepath.Join(root, parts[0], parts[1], ver), true
		}
	case "_actions", "ms-playwright":
		return filepath.Join(root, parts[0], parts[1]), true
	case "cargo":
		// Without a case the whole registry evicts as one multi-GB unit.
		if len(parts) >= 5 && parts[1] == "registry" && parts[2] == "src" {
			return filepath.Join(append([]string{root}, parts[:5]...)...), true
		}
		if len(parts) >= 4 && parts[1] == "git" {
			return filepath.Join(append([]string{root}, parts[:4]...)...), true
		}
		// jarvis: credentials.toml can age out under cap pressure —
		// private-registry users should pass CARGO_REGISTRIES_*_TOKEN.
		return filepath.Join(root, rel), false
	case "gocache", "pnpm-store", "gotmp", "gopath":
	default:
		return filepath.Join(root, parts[0], parts[1]), true
	}
	return filepath.Join(root, rel), false
}

// Critical-tier horizons: under critical pressure the runner reclaims
// harder than the warn tier's docker prune — ledger images unused past a
// day, workspaces older than a couple of hours, and the cache floor
// halves. Everything stays gated on active == 0.
const (
	criticalImageAge     = 24 * time.Hour
	criticalWorkspaceAge = 2 * time.Hour
	criticalTmpAge       = 24 * time.Hour
)

// tmpStalePatterns are directory names under the job temp root that are
// safe to delete when stale: test-tool scratch dirs, never workspaces or
// operator paths. Anything not matching stays — especially gha-* and
// kern-job-* trees a live job might still touch.
var tmpStalePatterns = []string{"pytest-of-*", "trivy-*", "playwright-*"}

// sweepTmpStale removes aged scratch dirs under os.TempDir() at the
// critical horizon. Co-located runners (GHA fleets, test frameworks)
// leave multi-gigabyte residue in /tmp that docker prune never sees.
func (a *Agent) sweepTmpStale(age time.Duration) {
	root := os.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-age)
	for _, e := range entries {
		matched := false
		for _, pat := range tmpStalePatterns {
			if ok, _ := filepath.Match(pat, e.Name()); ok {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			a.log.Warn("tmp sweep: remove failed", zap.String("path", path), zap.Error(err))
			continue
		}
		a.log.Info("tmp sweep: removed stale scratch dir", zap.String("path", path))
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
	critical := diskCritical(free, total, a.cfg.DiskCriticalFreePct, a.cfg.DiskCriticalFloorBytes)
	if !diskUnderPressure(free, total, pct) && !critical {
		return
	}
	// Docker prunes only touch stopped/unused objects — a running job's
	// containers, images, volumes and networks are in use and skipped by the
	// daemon. Deferring all reclaim to idle was the defect that let jobs die
	// on ENOSPC mid-write: pressure is worst exactly while jobs hold space.
	a.log.Warn("disk pressure: pruning docker",
		zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
		zap.Float64("min_free_pct", pct), zap.Bool("critical", critical))
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
	// Deep reclaim can remove objects a live job is reading (ledger images,
	// job workspaces, _cache entries mid-build) — it stays idle-gated.
	if a.active.Load() > 0 {
		a.log.Warn("disk pressure: jobs active, deep reclaim deferred",
			zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
			zap.Float64("min_free_pct", pct), zap.Bool("critical", critical))
		return
	}
	if critical {
		a.log.Warn("disk critical: escalating reclaim",
			zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
			zap.Float64("critical_free_pct", a.cfg.DiskCriticalFreePct),
			zap.Int64("critical_floor_bytes", a.cfg.DiskCriticalFloorBytes))
		a.sweepImagesOlder(ctx, criticalImageAge)
		a.sweepWorkspacesOlderThan(criticalWorkspaceAge)
		a.sweepBoundedCacheTo(a.cfg.CacheMaxBytes / 2)
		a.sweepTmpStale(criticalTmpAge)
	}
}

// diskUnderPressure reports whether free space is below pct percent of total.
// pct <= 0 disables the check; total == 0 means statfs failed.
func diskUnderPressure(free, total int64, pct float64) bool {
	return pct > 0 && total > 0 && float64(free)*100 < float64(total)*pct
}

// diskCritical reports the critical tier: free space below pct percent of
// total OR below the absolute floor, whichever trips first. Either bound
// of zero disables that half.
func diskCritical(free, total int64, pct float64, floor int64) bool {
	if diskUnderPressure(free, total, pct) {
		return true
	}
	return floor > 0 && free < floor
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
