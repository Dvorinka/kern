package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTimeoutOverdueJobs(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	_, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 3)
	r := runner(t, ctx, s, "watch")
	first, err := s.ClaimJob(ctx, r.ID, time.Minute)
	if err != nil || first == nil {
		t.Fatalf("claim overdue: %v", err)
	}
	second, err := s.ClaimJob(ctx, r.ID, time.Minute)
	if err != nil || second == nil {
		t.Fatalf("claim fresh: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE jobs SET started_at = now() - interval '2 minutes', timeout_seconds = 30
		WHERE id = $1`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	env, err := s.EnsureEnvironment(ctx, pid, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, pid, env.ID, first.Run.ID, first.Job.ID, "refs/heads/main", "abc", "", nil); err != nil {
		t.Fatal(err)
	}

	n, err := s.TimeoutOverdueJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("timed out %d jobs, want 1", n)
	}
	over, _ := s.Job(ctx, first.Job.ID)
	if over.Status != "completed" || over.Conclusion != "failure" || over.Error != "job exceeded timeout-minutes" {
		t.Fatalf("overdue = %s/%s %q", over.Status, over.Conclusion, over.Error)
	}
	if over.LeaseExpiresAt != nil {
		t.Fatal("timeout should drop the lease")
	}
	fresh, _ := s.Job(ctx, second.Job.ID)
	if fresh.Status != "in_progress" {
		t.Fatalf("in-window job = %s, want in_progress", fresh.Status)
	}
	var queued int
	for _, j := range jobs {
		if j.ID == first.Job.ID || j.ID == second.Job.ID {
			continue
		}
		got, _ := s.Job(ctx, j.ID)
		if got.Status == "queued" {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("queued jobs left = %d, want 1", queued)
	}
	dep, err := s.DeploymentForJob(ctx, first.Job.ID)
	if err != nil || dep.Status != "failure" {
		t.Fatalf("deployment = %+v err=%v", dep, err)
	}
}

func TestCacheLookupAndEviction(t *testing.T) {
	s, ctx := testStore(t)
	pid, _, _ := fixture(t, ctx, s)
	exact, err := s.SaveCacheEntry(ctx, pid, "sum-key", "v1", "trusted", "blob-exact", 10)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	other, err := s.SaveCacheEntry(ctx, pid, "sum-key-other", "v1", "trusted", "blob-other", 11)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CacheLookup(ctx, pid, "sum-key", []string{"", "sum-"}, []string{"trusted"})
	if err != nil || got.BlobRef != "blob-exact" {
		t.Fatalf("exact lookup = %q err=%v, want blob-exact", got.BlobRef, err)
	}
	got, err = s.CacheLookup(ctx, pid, "missing", []string{"", "sum-"}, []string{"trusted"})
	if err != nil || got.ID != other.ID {
		t.Fatalf("prefix lookup = %s err=%v, want newer prefix %s", got.ID, err, other.ID)
	}
	if _, err := s.CacheLookup(ctx, pid, "missing", []string{""}, []string{"trusted"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty prefix = %v, want not found", err)
	}
	if _, err := s.CacheLookup(ctx, pid, "sum-key", nil, []string{"fork"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong scope = %v, want not found", err)
	}
	if _, err := s.SaveCacheEntry(ctx, pid, "sum-key", "v1", "trusted", "blob-again", 1); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate save = %v, want ErrExists", err)
	}
	if _, err := s.SaveCacheEntry(ctx, pid, "sum-key", "v2", "trusted", "blob-v2", 1); err != nil {
		t.Fatalf("new version: %v", err)
	}

	kept, err := s.SaveCacheEntry(ctx, pid, "kept", "v1", "trusted", "blob-kept", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE cache_entries SET last_used_at = now() - interval '2 hours' WHERE id = $1`, exact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE cache_entries SET expires_at = now() - interval '1 minute' WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE cache_entries SET last_used_at = now() - interval '2 hours' WHERE id = $1`, kept.ID); err != nil {
		t.Fatal(err)
	}
	s.TouchCache(ctx, kept.ID)

	refs, err := s.DeleteStaleCacheEntries(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	gotRefs := map[string]bool{}
	for _, ref := range refs {
		gotRefs[ref] = true
	}
	if !gotRefs["blob-exact"] || !gotRefs["blob-other"] || gotRefs["blob-kept"] || gotRefs["blob-v2"] {
		t.Fatalf("evicted %v", refs)
	}
}

func TestWebhookDeliveryIsIdempotent(t *testing.T) {
	s, ctx := testStore(t)
	id := "delivery-" + uuid.NewString()
	fresh, err := s.SaveWebhookDelivery(ctx, "github", id, "push", []byte(`{"ok":true}`))
	if err != nil || !fresh {
		t.Fatalf("first save fresh=%v err=%v", fresh, err)
	}
	again, err := s.SaveWebhookDelivery(ctx, "github", id, "push", []byte(`{"ok":true}`))
	if err != nil || again {
		t.Fatalf("duplicate fresh=%v err=%v, want false", again, err)
	}
	if _, err := s.SaveWebhookDelivery(ctx, "github", id+"-bad", "push", []byte(`not-json`)); !errors.Is(err, errInvalidJSON) {
		t.Fatalf("invalid payload err = %v", err)
	}
}

// Token-mode runners never re-register, so version and disk capacity must
// ride the heartbeat or they report enroll-time values forever — a stale
// version made the auto-update offer loop on the same binary.
func TestHeartbeatRefreshesVersionAndDisk(t *testing.T) {
	s, ctx := testStore(t)
	r := runner(t, ctx, s, "hb-refresh")
	if _, err := s.pool.Exec(ctx, `UPDATE runners SET version = 'v0.0.1', disk_bytes = 1 WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	free := int64(5)
	total := int64(50)
	if err := s.Heartbeat(ctx, r.ID, HostMetrics{DiskFreeBytes: &free, DiskBytes: &total, Version: "v0.9.9"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Runner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v0.9.9" || got.DiskBytes != 50 {
		t.Fatalf("version=%q disk=%d, want v0.9.9/50", got.Version, got.DiskBytes)
	}
	// An empty version or nil total must not clobber the stored values.
	if err := s.Heartbeat(ctx, r.ID, HostMetrics{}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Runner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v0.9.9" || got.DiskBytes != 50 {
		t.Fatalf("after empty heartbeat version=%q disk=%d, want v0.9.9/50", got.Version, got.DiskBytes)
	}
}

// The label set rides the heartbeat too — a runner enrolled before the
// name-label release keeps its stale registration-time labels until it
// re-registers, which token-mode runners never do.
func TestHeartbeatRefreshesLabels(t *testing.T) {
	s, ctx := testStore(t)
	r := runner(t, ctx, s, "hb-labels")
	if _, err := s.pool.Exec(ctx, `UPDATE runners SET labels = '{self-hosted,kern}' WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx, r.ID, HostMetrics{Labels: []string{"self-hosted", "kern", "hb-labels", "linux-x64"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Runner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 4 || got.Labels[2] != "hb-labels" {
		t.Fatalf("labels=%v, want name label synced", got.Labels)
	}
	// nil (old runner, no labels field) must not clobber the stored set.
	if err := s.Heartbeat(ctx, r.ID, HostMetrics{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Runner(ctx, r.ID); len(got.Labels) != 4 {
		t.Fatalf("after empty heartbeat labels=%v, want preserved", got.Labels)
	}
}

func TestMarkStaleRunnersOffline(t *testing.T) {
	s, ctx := testStore(t)
	fresh := runner(t, ctx, s, "fresh-hb")
	stale := runner(t, ctx, s, "stale-hb")
	if _, err := s.pool.Exec(ctx, `
		UPDATE runners SET last_heartbeat_at = now() - interval '5 minutes' WHERE id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	offline, err := s.MarkStaleRunnersOffline(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(offline) != 1 || offline[0].ID != stale.ID {
		t.Fatalf("offline = %+v, want [%s]", offline, stale.ID)
	}
	org, _ := s.FirstOrg(ctx)
	list, err := s.Runners(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	status := map[uuid.UUID]string{}
	for _, r := range list {
		status[r.ID] = r.Status
	}
	if status[fresh.ID] != "online" || status[stale.ID] != "offline" {
		t.Fatalf("statuses = %v", status)
	}
}
