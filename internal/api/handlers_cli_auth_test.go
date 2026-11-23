package api

import (
	"testing"
	"time"

	"github.com/dvorinka/kern/internal/store"
)

func TestCLIAuthStoreLifecycle(t *testing.T) {
	cs := newCLIAuthStore()

	entry, err := cs.start("kern-cli (laptop)", "laptop", 90)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(entry.DeviceCode) != 64 {
		t.Fatalf("device_code len = %d, want 64", len(entry.DeviceCode))
	}
	if len(entry.UserCode) != 9 || entry.UserCode[4] != '-' {
		t.Fatalf("unexpected user_code %q", entry.UserCode)
	}

	// Poll while pending keeps the entry alive.
	polled, ok := cs.poll(entry.DeviceCode)
	if !ok || polled.Status != "pending" {
		t.Fatalf("poll pending: ok=%v status=%q", ok, polled.Status)
	}

	// Lookup normalizes case and missing hyphen.
	noHyphen := entry.UserCode[:4] + entry.UserCode[5:]
	looked, ok := cs.lookup("  " + noHyphen + " ")
	if !ok || looked.Name != "kern-cli (laptop)" || looked.DeviceCode != "" {
		t.Fatalf("lookup mismatch: ok=%v entry=%+v", ok, looked)
	}

	// Approve then poll once: returns token and consumes the session.
	if !cs.approve(noHyphen, "kern_secret_tok", store.APIToken{Prefix: "kern_sec"}, store.User{Email: "a@b.c"}) {
		t.Fatal("approve failed")
	}
	got, ok := cs.poll(entry.DeviceCode)
	if !ok || got.Status != "approved" || got.Token != "kern_secret_tok" || got.User.Email != "a@b.c" {
		t.Fatalf("poll approved: ok=%v got=%+v", ok, got)
	}
	if _, again := cs.poll(entry.DeviceCode); again {
		t.Fatal("second poll after approval must return gone")
	}
}

func TestCLIAuthStoreDenyAndExpiry(t *testing.T) {
	cs := newCLIAuthStore()

	e1, err := cs.start("kern-cli", "box", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.deny(e1.UserCode) {
		t.Fatal("deny failed")
	}
	got, ok := cs.poll(e1.DeviceCode)
	if !ok || got.Status != "denied" {
		t.Fatalf("poll denied: ok=%v got=%+v", ok, got)
	}
	if _, again := cs.poll(e1.DeviceCode); again {
		t.Fatal("second poll after deny must return gone")
	}

	e2, err := cs.start("kern-cli", "box", 30)
	if err != nil {
		t.Fatal(err)
	}
	cs.mu.Lock()
	cs.byDevice[e2.DeviceCode].ExpiresAt = time.Now().Add(-time.Second)
	cs.mu.Unlock()
	if _, ok := cs.poll(e2.DeviceCode); ok {
		t.Fatal("expired session should not be returned")
	}
}
