package runner

import "testing"

func TestSizeWorkers(t *testing.T) {
	cases := []struct {
		host, conc int
		job        string
		workers    int
		cpus       string
	}{
		{4, 0, "", 4, "1"},
		{4, 1, "", 1, "4"},
		{8, 2, "", 2, "4"},
		{8, 0, "2", 4, "2"},
		{4, 0, "0.5", 8, "0.5"},
		{4, 1, "1", 1, "1"},
		{4, 100, "", 100, "1"},
		{0, 0, "", 1, "1"},
		{4, 0, "nope", 4, "1"},
	}
	for _, c := range cases {
		workers, cpus := SizeWorkers(c.host, c.conc, c.job)
		if workers != c.workers || cpus != c.cpus {
			t.Errorf("SizeWorkers(%d, %d, %q) = %d, %q; want %d, %q", c.host, c.conc, c.job, workers, cpus, c.workers, c.cpus)
		}
	}
}
