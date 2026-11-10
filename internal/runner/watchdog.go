package runner

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// startDiskSampler enforces the job's work-root budget (jobDiskBudget):
// KERN_JOB_DISK on the workflow (trusted runs) or the runner-wide
// Config.JobDiskBytes. Breaching the ceiling kills the step in flight —
// an early disk_quota diagnosis beats dying on ENOSPC mid-write, which
// both corrupts the workspace and starves sibling jobs.
func (e *executor) startDiskSampler(ctx context.Context) {
	budget := e.jobDiskBudget()
	if budget <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			used, err := dirSizeBytes(e.workRoot())
			if err != nil {
				continue
			}
			if e.metrics != nil {
				e.metrics.observeDisk(used)
			}
			if used <= budget {
				continue
			}
			if e.diskQuotaExceeded.CompareAndSwap(false, true) {
				e.log.Warn("job disk budget exceeded: aborting",
					zap.Int64("used", used), zap.Int64("budget", budget))
				e.mu.Lock()
				if e.killFn != nil {
					e.killFn()
				}
				e.mu.Unlock()
			}
			return
		}
	}()
}

// dirSizeBytes totals regular-file bytes under path. `du -sb` is used when
// available (linux runner hosts); the WalkDir fallback covers darwin du and
// Windows, where the flag does not exist.
func dirSizeBytes(path string) (int64, error) {
	if out, err := exec.Command("du", "-sb", path).Output(); err == nil {
		if f := strings.Fields(string(out)); len(f) == 2 {
			if n, perr := strconv.ParseInt(f[0], 10, 64); perr == nil {
				return n, nil
			}
		}
	}
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, e2 := d.Info(); e2 == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}

// watchSilence kills a step that produces no output for
// 2*Config.StepSilenceSeconds — a hang, not a wall-clock timeout. At the
// first interval it dumps process state (and jstack when a JVM is around)
// into the step log so the hang is diagnosable; at the second it cancels
// the step. Gradle daemon stalls and registry deadlocks look exactly like
// this: a live process, silent for twenty minutes.
func (e *executor) watchSilence(ctx context.Context, step StepSpec, pump *logPump) {
	if e.cfg.StepSilenceSeconds <= 0 || step.Uses != "" {
		return
	}
	limit := time.Duration(e.cfg.StepSilenceSeconds) * time.Second
	start := time.Now()
	dumped := false
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		last := pump.lastWrite.Load()
		silent := time.Since(start)
		if last > 0 {
			silent = time.Since(time.Unix(0, last))
		}
		if silent < limit {
			continue
		}
		if !dumped {
			dumped = true
			pump.write([]byte(fmt.Sprintf(
				"KERN: no output for %s — dumping process state (kill at %s silence)\n",
				silent.Round(time.Second), (2 * limit).Round(time.Second))))
			if ps := e.procDump(ctx); ps != "" {
				pump.write([]byte("KERN: process dump:\n" + ps))
			}
			continue
		}
		if silent >= 2*limit {
			pump.write([]byte(fmt.Sprintf(
				"KERN: no output for %s — killing step as hung\n", silent.Round(time.Second))))
			e.hangDetected.Store(true)
			e.mu.Lock()
			if e.killFn != nil {
				e.killFn()
			}
			e.mu.Unlock()
			return
		}
	}
}

// procDump snapshots the job's process table — `ps` inside the container
// for docker jobs, on the host otherwise — plus a jstack of any JVMs,
// which is the canonical Gradle-stall diagnostic.
func (e *executor) procDump(ctx context.Context) string {
	script := `(ps auxww 2>/dev/null || ps) | head -40
for p in $(pgrep -x java 2>/dev/null | head -3); do
  echo "--- jstack $p ---"
  jstack "$p" 2>&1 | head -80
done
true`
	var cmd *exec.Cmd
	if e.host {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	} else {
		cmd = exec.CommandContext(ctx, "docker", "exec", e.container, "sh", "-c", script)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return tailStr(string(out), 8<<10)
}
