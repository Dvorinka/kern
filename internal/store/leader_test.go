package store

import (
	"context"
	"testing"
	"time"
)

// testLeaderKey is distinct from orchestrator.LeaderLockKey so the test lock
// can never collide with a real deployment sharing the database.
const testLeaderKey int64 = 0x4B45524E7E

func TestLeaderSessionLifecycle(t *testing.T) {
	s, ctx := testStore(t)

	l1, ok, err := s.TryLeader(ctx, testLeaderKey)
	if err != nil {
		t.Fatalf("try leader: %v", err)
	}
	if !ok {
		t.Fatal("first TryLeader should acquire")
	}
	defer l1.Release()

	// A second contender on the same key must lose — this is the property
	// that keeps two api replicas from both ticking.
	if _, ok, err := s.TryLeader(ctx, testLeaderKey); err != nil {
		t.Fatalf("contend: %v", err)
	} else if ok {
		t.Fatal("second TryLeader should not acquire while first is held")
	}

	if !l1.Alive(ctx) {
		t.Fatal("held leader should report alive")
	}

	// Kill the backend holding the lock: the session-level advisory lock
	// dies with the connection, which is the failover guarantee.
	terminateLeaderBackend(t, ctx, s, testLeaderKey)
	deadline := time.Now().Add(5 * time.Second)
	for l1.Alive(ctx) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if l1.Alive(ctx) {
		t.Fatal("leader on a terminated backend still reports alive")
	}

	// Once the dead session is released, another replica can lead.
	l1.Release()
	l2, ok, err := s.TryLeader(ctx, testLeaderKey)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if !ok {
		t.Fatal("TryLeader should acquire after previous leader died")
	}
	l2.Release()
	l2.Release() // idempotent

	// And after a clean release the lock is free again.
	l3, ok, err := s.TryLeader(ctx, testLeaderKey)
	if err != nil {
		t.Fatalf("post-release try: %v", err)
	}
	if !ok {
		t.Fatal("TryLeader should acquire after Release")
	}
	l3.Release()
}

// terminateLeaderBackend finds the backend holding the advisory lock for key
// and terminates it, simulating a crashed or partitioned leader replica.
func terminateLeaderBackend(t *testing.T, ctx context.Context, s *Store, key int64) {
	t.Helper()
	// pg_locks splits a bigint advisory key into classid (high 32 bits) and
	// objid (low 32 bits).
	var pid int32
	err := s.pool.QueryRow(ctx, `
		SELECT pid FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND granted`,
		int32(key>>32), int32(key&0xFFFFFFFF)).Scan(&pid)
	if err != nil {
		t.Fatalf("find leader backend: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatalf("terminate leader backend %d: %v", pid, err)
	}
}
