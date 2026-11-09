//go:build !windows

package runner

import (
	"os"
	"syscall"
)

// swapBinary atomically replaces exe with newBin. Renaming over a running
// unix binary is safe: the process keeps the old inode.
func swapBinary(exe, newBin string) error {
	return os.Rename(newBin, exe)
}

// relaunchSelf re-execs the new binary in place — the supervisor sees no
// exit, so on-failure restarts and the windows-task gap never apply.
func relaunchSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
