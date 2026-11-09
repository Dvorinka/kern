package runner

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// swapBinary renames the running exe aside (windows forbids overwriting it
// but permits renaming) and moves the new binary into place.
func swapBinary(exe, newBin string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(newBin, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

// relaunchSelf starts the new binary detached — the scheduled task does
// not restart exited tasks — then exits the old process.
func relaunchSelf(exe string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd := exec.Command(exe)
	cmd.Args = os.Args
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
