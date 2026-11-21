//go:build windows

package runner

import (
	"os/exec"
	"time"
)

func configureHostProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
