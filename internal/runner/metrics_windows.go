//go:build windows

package runner

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX — dwLength must be set to the
// struct size or GlobalMemoryStatusEx fails.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// cpuCounters reads GetSystemTimes. Windows kernel time includes idle, so
// busy = kernel + user - idle. Units are 100ns FILETIME ticks; only deltas
// between samples are meaningful.
func cpuCounters() (busy, total uint64, ok bool) {
	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, 0, false
	}
	ft := func(f windows.Filetime) uint64 {
		return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
	}
	total = ft(kernel) + ft(user)
	return total - ft(idle), total, true
}

// loadAvg1 has no Windows equivalent; the overload claim gate stays off.
func loadAvg1() (float64, bool) { return 0, false }

func cpusVisible() int { return runtime.NumCPU() }

// memInfo reports physical total/available via GlobalMemoryStatusEx.
func memInfo() (total, available int64, ok bool) {
	var ms memoryStatusEx
	ms.dwLength = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return 0, 0, false
	}
	return int64(ms.ullTotalPhys), int64(ms.ullAvailPhys), true
}
