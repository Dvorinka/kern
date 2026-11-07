package runner

import (
	"os"

	"golang.org/x/sys/unix"
)

func totalDisk(dir string) int64 {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}

// diskSpace reports free and total bytes on the filesystem holding dir.
func diskSpace(dir string) (free, total int64) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, 0
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize)
}
