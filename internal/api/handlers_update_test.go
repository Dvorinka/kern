package api

import "testing"

func TestApplyBlock(t *testing.T) {
	if got := applyBlock(updateInfo{AutoUpdate: "off", OK: true, ManifestOK: true, Current: "v0.3.0"}); got != "auto_update is off" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "dev", Available: true}); got == "" {
		t.Fatal("dev must not apply")
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "v0.3.0", Available: true}); got != "" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: false, ManifestError: "release has no signed manifest", Current: "v0.3.0"}); got != "release has no signed manifest" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "v0.4.0", Available: false}); got != "" {
		t.Fatal(got)
	}
}
