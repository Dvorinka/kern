//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// configureHostProcessGroup isolates host-mode steps in their own process
// group so cancellation or timeout kills the entire child tree (go test,
// node, java) instead of leaving orphaned descendants behind.
func configureHostProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
