package runner

import "testing"

func TestSizeWorkers(t *testing.T) {
	cases := []struct {
		host, conc int
		job        string
		workers    int
		cpus       string
	}{
		{4, 0, "", 4, "2"}, // share floor: --cpus is a burst limit, jobs get >=2 cores (#372)
		{4, 1, "", 1, "4"},
		{8, 2, "", 2, "4"},
		{8, 0, "2", 4, "2"},
		{4, 0, "0.5", 8, "0.5"},
		{4, 1, "1", 1, "1"},
		{4, 100, "", 100, "2"},
		{0, 0, "", 1, "1"}, // share floors at 2 but caps at hostCPUs=1
		{4, 0, "nope", 4, "2"},
	}
	for _, c := range cases {
		workers, cpus := SizeWorkers(c.host, c.conc, c.job)
		if workers != c.workers || cpus != c.cpus {
			t.Errorf("SizeWorkers(%d, %d, %q) = %d, %q; want %d, %q", c.host, c.conc, c.job, workers, cpus, c.workers, c.cpus)
		}
	}
}
