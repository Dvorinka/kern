package workflows

import (
	"testing"
	"time"
)

func TestNextCron(t *testing.T) {
	base := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC) // Tuesday
	cases := []struct {
		expr string
		want time.Time
	}{
		{"* * * * *", base.Add(time.Minute)},
		{"30 5 * * *", time.Date(2026, 11, 4, 5, 30, 0, 0, time.UTC)},
		{"0 0 * * 1", time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}, // next Monday
		{"0 0 * * 7", time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}, // Sunday via 7
		{"*/15 * * * *", base.Add(15 * time.Minute)},
		{"0 9-17 * * 1-5", time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)},
		{"0,30 8 * * *", time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)},
		{"0 0 1 * *", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"0 0 29 2 *", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, err := NextCron(tc.expr, base)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("%q = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestNextCronErrors(t *testing.T) {
	for _, expr := range []string{"", "* * *", "61 * * * *", "x y z w v", "0 0 31 2 *"} {
		if _, err := NextCron(expr, time.Now()); err == nil {
			t.Errorf("%q: expected error", expr)
		}
	}
}
