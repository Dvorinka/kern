package runner

import "strconv"

// SizeWorkers picks how many jobs this host runs at once and the --cpus value
// for each of them. concurrency > 0 is an operator pin. Zero means one worker
// per CPU, or hostCPUs/jobCPUs when jobCPUs is a positive number.
// An empty or unparsable jobCPUs becomes an equal share of the host, at least 1.
// An explicit jobCPUs string is kept as written.
func SizeWorkers(hostCPUs, concurrency int, jobCPUs string) (workers int, cpus string) {
	if hostCPUs < 1 {
		hostCPUs = 1
	}
	pinned, err := strconv.ParseFloat(jobCPUs, 64)
	pinnedOK := err == nil && pinned > 0
	workers = concurrency
	if workers <= 0 {
		workers = hostCPUs
		if pinnedOK {
			workers = int(float64(hostCPUs) / pinned)
		}
		if workers < 1 {
			workers = 1
		}
	}
	if pinnedOK {
		return workers, jobCPUs
	}
	share := hostCPUs / workers
	// --cpus is a burst limit, not a reservation — a strict equal share
	// (1 core per job on an 8-worker host) starves parallel compilers
	// while leaving the kernel idle (#372). Floor each job at 2 cores;
	// oversubscription is safe because the scheduler arbitrates bursts.
	if share < 2 {
		share = 2
	}
	if share > hostCPUs {
		share = hostCPUs
	}
	return workers, strconv.Itoa(share)
}
