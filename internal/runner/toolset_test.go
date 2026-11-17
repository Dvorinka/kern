package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestPickToolBin(t *testing.T) {
	writable := t.TempDir()
	other := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope")

	cases := []struct {
		name string
		path string
		want string
	}{
		{"writable absolute picked", writable, writable},
		{"first writable in PATH order", writable + string(os.PathListSeparator) + other, writable},
		{"relative entries skipped", "bin" + string(os.PathListSeparator) + "." + string(os.PathListSeparator) + writable, writable},
		{"nonexistent absolute skipped", missing + string(os.PathListSeparator) + writable, writable},
		{"nothing usable", "bin" + string(os.PathListSeparator) + missing, ""},
		{"empty PATH", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickToolBin(tc.path); got != tc.want {
				t.Fatalf("pickToolBin(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestToolsetHealDue(t *testing.T) {
	a := &Agent{}
	missing := []string{"minisign"}

	if !a.toolsetHealDue(missing) {
		t.Fatal("first attempt for a set should be due")
	}
	if a.toolsetHealDue(missing) {
		t.Fatal("identical set inside cooldown should not be due")
	}
	if !a.toolsetHealDue([]string{"gh", "minisign"}) {
		t.Fatal("changed missing set should heal immediately")
	}
	a.toolsetHealAt = time.Now().Add(-toolsetHealCooldown - time.Second)
	if !a.toolsetHealDue([]string{"gh", "minisign"}) {
		t.Fatal("identical set past cooldown should be due again")
	}
}

// Windows plans must cover both arches — a regression that drops the
// arm64 branch leaves Windows-on-ARM runners unable to self-heal.
func TestWindowsPlansCoverARM(t *testing.T) {
	for name, plan := range map[string]string{"gh": ghInstall, "minisign": minisignInstall} {
		idx := strings.Index(plan, "MINGW*|MSYS*|CYGWIN*|Windows*)")
		if idx < 0 {
			t.Fatalf("%s: no Windows branch in install plan", name)
		}
		win := plan[idx:]
		if !strings.Contains(win, "arm64") && !strings.Contains(win, "aarch64") {
			t.Fatalf("%s: Windows branch has no ARM arch case", name)
		}
		if !strings.Contains(win, "KERN_TOOL_BIN") {
			t.Fatalf("%s: Windows branch does not install into KERN_TOOL_BIN", name)
		}
	}
}
