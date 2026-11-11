//go:build darwin

package runner

import (
	"context"
	"encoding/binary"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// cpuCounters reads kern.cp_time — struct xcpuinfo { uint64_t cp_time[4] }
// ordered USER, SYSTEM, IDLE, NICE. busy = total - idle, matching the
// /proc/stat contract (deltas between samples only).
func cpuCounters() (busy, total uint64, ok bool) {
	data, err := unix.SysctlRaw("kern.cp_time")
	if err != nil || len(data) < 32 {
		return 0, 0, false
	}
	var idle uint64
	for i := 0; i < 4; i++ {
		v := binary.LittleEndian.Uint64(data[i*8:])
		total += v
		if i == 2 {
			idle = v
		}
	}
	return total - idle, total, true
}

// loadAvg1 reads vm.loadavg — struct loadavg { u_int32_t ldavg[3];
// u_int32_t fscale }; each sample is ldavg[i]/fscale.
func loadAvg1() (float64, bool) {
	data, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(data) < 16 {
		return 0, false
	}
	fscale := binary.LittleEndian.Uint32(data[12:])
	if fscale == 0 {
		return 0, false
	}
	return float64(binary.LittleEndian.Uint32(data[0:])) / float64(fscale), true
}

func cpusVisible() int {
	if n, err := unix.SysctlUint32("hw.ncpu"); err == nil && n > 0 {
		return int(n)
	}
	return runtime.NumCPU()
}

// memInfo reports physical memory. Total comes from hw.memsize; "available"
// approximates Activity Monitor's reclaimable estimate as
// (free + inactive + speculative + purgeable) vm_stat pages — Darwin has no
// MemAvailable equivalent.
func memInfo() (total, available int64, ok bool) {
	totalU, err := unix.SysctlUint64("hw.memsize")
	if err != nil || totalU == 0 {
		return 0, 0, false
	}
	pageSize, err := unix.SysctlUint64("hw.pagesize")
	if err != nil || pageSize == 0 {
		return 0, 0, false
	}
	pages, ok := vmStatPages()
	if !ok {
		return 0, 0, false
	}
	availPages := pages["Pages free:"] + pages["Pages inactive:"] +
		pages["Pages speculative:"] + pages["Pages purgeable:"]
	return int64(totalU), int64(availPages) * int64(pageSize), true
}

// vmStatPages parses /usr/bin/vm_stat into a page-count map keyed by the
// stat's column label (e.g. "Pages free:").
func vmStatPages() (map[string]uint64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return nil, false
	}
	pages := map[string]uint64{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Label is everything before the trailing count; the count ends in "."
		// and the label's last word already carries the colon ("free:").
		last := strings.TrimSuffix(fields[len(fields)-1], ".")
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil {
			continue
		}
		pages[strings.Join(fields[:len(fields)-1], " ")] = n
	}
	return pages, len(pages) > 0
}
