package runner

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"testing"
)

func TestLeaseRenewAbort(t *testing.T) {
	if abort, n := leaseRenewAbort(nil, false, 4); abort || n != 0 {
		t.Fatalf("success should clear the streak, got abort=%v n=%d", abort, n)
	}
	if abort, n := leaseRenewAbort(nil, true, 0); !abort || n != 0 {
		t.Fatalf("server cancel should abort, got abort=%v n=%d", abort, n)
	}
	lost := &APIError{Status: http.StatusNotFound, Body: "job is not leased to this runner"}
	if abort, _ := leaseRenewAbort(lost, false, 0); !abort {
		t.Fatal("404 means the lease was reaped; the local job must stop")
	}
	gone := &APIError{Status: http.StatusConflict, Body: "conflict"}
	if abort, _ := leaseRenewAbort(gone, false, 0); !abort {
		t.Fatal("409 should abort")
	}
	blip := &APIError{Status: http.StatusInternalServerError, Body: "db"}
	abort, n := leaseRenewAbort(blip, false, 0)
	if abort || n != 1 {
		t.Fatalf("one 500 is a blip, got abort=%v n=%d", abort, n)
	}
	if abort, n = leaseRenewAbort(blip, false, leaseRenewGiveUp-1); !abort || n != leaseRenewGiveUp {
		t.Fatalf("streak past the lease window should abort, got abort=%v n=%d", abort, n)
	}
	if abort, n = leaseRenewAbort(errors.New("dial timeout"), false, leaseRenewGiveUp-2); abort || n != leaseRenewGiveUp-1 {
		t.Fatalf("transport error should only count, got abort=%v n=%d", abort, n)
	}
}

func TestDuFallbackWalk(t *testing.T) {
	if duFallbackWalk(nil) || duFallbackWalk(context.DeadlineExceeded) || duFallbackWalk(context.Canceled) {
		t.Fatal("timeout must not fall through to a full tree walk")
	}
	if !duFallbackWalk(exec.ErrNotFound) {
		t.Fatal("missing du should walk")
	}
	cmd := exec.Command("false")
	err := cmd.Run()
	if !duFallbackWalk(err) {
		t.Fatal("du rejecting -sb should walk")
	}
}
