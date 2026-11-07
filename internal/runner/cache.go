package runner

// actions/cache, actions/cache/restore and actions/cache/save, executed natively
// like the artifact actions. Archives are tar.gz of workspace-relative paths;
// the control plane owns lookup and storage. A combined actions/cache step
// restores immediately and defers its save to the end of the job — GitHub's
// post-job semantics — so later steps can change what gets cached.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pendingCacheSave is a combined actions/cache step that restored something
// other than its primary key (or missed entirely) and must save at job end.
type pendingCacheSave struct {
	step  int
	key   string
	paths []string
}

func (e *executor) cacheRestoreStep(step StepSpec, combined bool) (string, int) {
	key := withString(step, "key", "")
	paths := withLines(step, "path")
	if key == "" || len(paths) == 0 {
		e.logStep(step, "KERN: actions/cache requires with.key and with.path")
		return "failure", 1
	}
	tmp := filepath.Join(e.workRoot(), "cache-restore.tar.gz")
	defer os.Remove(tmp)
	matched, found, err := e.c.restoreCache(context.Background(), e.jobID, key,
		withLines(step, "restore-keys"), tmp)
	if err != nil {
		e.logStep(step, "KERN: cache restore: "+err.Error())
		return "failure", 1
	}
	// A combined actions/cache saves at job end unless lookup-only, or the exact
	// key was already hit — saves are immutable so a hit means nothing to write.
	deferSave := combined && withString(step, "lookup-only", "") != "true"
	if !found {
		e.setStepOutput("cache-hit", "false")
		e.logStep(step, "Cache not found for key: "+key)
		if withString(step, "fail-on-cache-miss", "") == "true" {
			return "failure", 1
		}
		if deferSave {
			e.pendingCache = append(e.pendingCache, pendingCacheSave{step: step.Number, key: key, paths: paths})
		}
		return "success", 0
	}
	ws := filepath.Join(e.workRoot(), "workspace")
	if err := untarGz(tmp, ws); err != nil {
		e.logStep(step, "KERN: extract cache: "+err.Error())
		return "failure", 1
	}
	e.setStepOutput("cache-hit", strconv.FormatBool(matched == key))
	if matched == key {
		e.logStep(step, "Cache restored from key: "+matched)
	} else {
		e.logStep(step, fmt.Sprintf("Cache restored from key: %s (matched restore-keys, requested %s)", matched, key))
	}
	if deferSave && matched != key {
		e.pendingCache = append(e.pendingCache, pendingCacheSave{step: step.Number, key: key, paths: paths})
	}
	return "success", 0
}

func (e *executor) cacheSaveStep(step StepSpec) (string, int) {
	key := withString(step, "key", "")
	paths := withLines(step, "path")
	if key == "" || len(paths) == 0 {
		e.logStep(step, "KERN: actions/cache/save requires with.key and with.path")
		return "failure", 1
	}
	if err := e.saveCacheArchive(key, paths); err != nil {
		if errors.Is(err, errCacheExists) || errors.Is(err, errCacheEmpty) {
			e.logStep(step, "KERN: "+err.Error())
			return "success", 0
		}
		e.logStep(step, "KERN: "+err.Error())
		return "failure", 1
	}
	e.logStep(step, "Cache saved with key: "+key)
	return "success", 0
}

// saveCaches flushes deferred combined-action saves at job end. Failures are
// warnings, never job failures — same contract as GitHub's post action.
func (e *executor) saveCaches() {
	for _, p := range e.pendingCache {
		if err := e.saveCacheArchive(p.key, p.paths); err != nil {
			e.logStep(StepSpec{Number: p.step}, "KERN: cache save: "+err.Error())
			continue
		}
		e.logStep(StepSpec{Number: p.step}, "Cache saved with key: "+p.key)
	}
}

var (
	errCacheExists = errors.New("cache key already taken")
	// errCacheEmpty means no cache path resolved to files. GitHub treats this
	// as a warning, not a failure — composite actions (trivy, setup-trivy)
	// cache paths outside the workspace and must not fail the step for it.
	errCacheEmpty = errors.New("no files found for cache path(s)")
	// errCacheTooLarge is the server's KERN_CACHE_MAX_BYTES refusal; the
	// archive was not stored, so the step must say so rather than imply a save.
	errCacheTooLarge = errors.New("cache archive exceeds the server size limit")
)

func (e *executor) saveCacheArchive(key string, paths []string) error {
	ws := filepath.Join(e.workRoot(), "workspace")
	tmp, err := os.CreateTemp(e.workRoot(), "cache-save-*.tar.gz")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	n, err := tarPaths(ws, paths, tmp.Name())
	if err != nil {
		return fmt.Errorf("pack cache: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", errCacheEmpty, strings.Join(paths, ", "))
	}
	err = e.c.saveCache(context.Background(), e.jobID, key, tmp.Name())
	if apiErr, ok := err.(*APIError); ok {
		switch apiErr.Status {
		case 409:
			return fmt.Errorf("%w: unable to reserve cache with key %q, another job may be creating this cache",
				errCacheExists, key)
		case 413:
			return fmt.Errorf("%w: %s", errCacheTooLarge, apiErr.Body)
		}
	}
	return err
}

// setStepOutput appends a key=value pair to the GITHUB_OUTPUT file the host
// merges after the step, so uses-steps expose outputs like run-steps do.
func (e *executor) setStepOutput(k, v string) {
	f, err := os.OpenFile(filepath.Join(e.cmdDir(), "GITHUB_OUTPUT"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s=%s\n", k, v)
}

// tarPaths packs each workspace-relative path (files or directories, walked
// recursively) into out. Paths resolving outside the workspace are skipped —
// host-level caching (e.g. ~/.cache) is not supported yet.
func tarPaths(ws string, relPaths []string, out string) (int, error) {
	ws = filepath.Clean(ws)
	var entries []artifactEntry
	for _, rp := range relPaths {
		full := filepath.Join(ws, filepath.Clean(rp))
		if full != ws && !strings.HasPrefix(full, ws+string(os.PathSeparator)) {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			entries = append(entries, artifactEntry{file: full, root: ws})
			continue
		}
		_ = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				entries = append(entries, artifactEntry{file: path, root: ws})
			}
			return nil
		})
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return len(entries), tarGz(entries, out)
}
