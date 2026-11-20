//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPsiSomeAvg10(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpu.pressure")
	if err := os.WriteFile(p, []byte(
		"some avg10=1.50 avg60=0.75 avg300=0.25 total=12345\n"+
			"full avg10=0.00 avg60=0.00 avg300=0.00 total=678\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, ok := psiSomeAvg10(p); !ok || v != 1.5 {
		t.Fatalf("psiSomeAvg10 = %v,%v want 1.5,true", v, ok)
	}
	if _, ok := psiSomeAvg10(filepath.Join(dir, "missing")); ok {
		t.Fatal("missing file reported readable")
	}
	if err := os.WriteFile(p, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := psiSomeAvg10(p); ok {
		t.Fatal("unparseable file reported readable")
	}
}
