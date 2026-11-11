//go:build linux

package runner

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// cpuCounters reads the aggregate "cpu" line of /proc/stat. busy is everything
// except idle+iowait.
func cpuCounters() (busy, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		var user, nice, system, idle, iowait, irq, softirq, steal uint64
		fields := strings.Fields(line[4:])
		vals := []*uint64{&user, &nice, &system, &idle, &iowait, &irq, &softirq, &steal}
		for i, f := range fields {
			if i >= len(vals) {
				break
			}
			*vals[i], _ = strconv.ParseUint(f, 10, 64)
		}
		total = user + nice + system + idle + iowait + irq + softirq + steal
		busy = total - idle - iowait
		return busy, total, true
	}
	return 0, 0, false
}

// loadAvg1 reads the 1-minute load average, used by the optional claim gate.
func loadAvg1() (float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Fields(string(data))[0], 64)
	return v, err == nil
}

// cpusVisible counts the processors the kernel reports via /proc/cpuinfo.
// That is the machine /proc/loadavg describes — inside an LXC or a cpuset
// docker container, runtime.NumCPU reflects the container's quota while
// loadavg stays host-wide, so normalizing by NumCPU over-fires the gate.
func cpusVisible() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		n := strings.Count(string(data), "\nprocessor")
		if strings.HasPrefix(string(data), "processor") {
			n++
		}
		if n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}

// memInfo reports total and available bytes from /proc/meminfo — MemAvailable
// is the kernel's estimate of memory a new allocation can use without
// swapping.
func memInfo() (total, available int64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = kb
		case "MemAvailable:":
			available = kb
		}
	}
	return total * 1024, available * 1024, total > 0
}
