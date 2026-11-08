//go:build windows

package runner

import (
	"os"

	"golang.org/x/sys/windows"
)

func totalDisk(dir string) int64 {
	_, total := diskSpace(dir)
	return total
}

// diskSpace reports free and total bytes on the volume holding dir via
// GetDiskFreeSpaceEx. Windows does not distinguish unprivileged-available
// bytes, so free == totalFree.
func diskSpace(dir string) (free, total int64) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, 0
	}
	var freeAvail, totalBytes, totalFree uint64
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0
	}
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &totalBytes, &totalFree); err != nil {
		return 0, 0
	}
	return int64(totalFree), int64(totalBytes)
}
