package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSystemBinDir(t *testing.T) {
	cases := map[string]bool{
		`C:\Windows\System32`:                      true,
		`C:\Program Files\Git\cmd`:                 true,
		`C:\Program Files (x86)\Common Files\x`:    true,
		`C:\ProgramData\chocolatey\bin`:            true,
		`D:\Windows`:                               true,
		`c:\users\pc\.cargo\bin`:                   false,
		`C:\Users\PC\AppData\Local\Programs\Devin`: false,
		`C:\tools`:       false,
		`/usr/local/bin`: false,
		`bin`:            false,
	}
	for in, want := range cases {
		if got := isSystemBinDir(in); got != want {
			t.Errorf("isSystemBinDir(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsWritableDir(t *testing.T) {
	dir := t.TempDir()
	if !isWritableDir(dir) {
		t.Fatal("temp dir should be writable")
	}
	if isWritableDir(filepath.Join(dir, "missing")) {
		t.Fatal("missing dir reported writable")
	}
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isWritableDir(f) {
		t.Fatal("regular file reported as writable dir")
	}
}
