package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

func TestPublicViewOmitsPrivateFields(t *testing.T) {
	run := store.Run{
		ID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		WorkflowName: "CI", RunNumber: 3, Event: "push",
		Ref: "refs/heads/master", SHA: "abcdef1234567890",
		CommitMessage: "fix the gate\nTOKEN=supersecret",
		Actor:         "admin@kern.local", Status: "completed", Conclusion: "success",
		Source:      json.RawMessage(`{"token":"supersecret"}`),
		ProjectSlug: "kern",
	}
	jobs := []store.Job{{
		Name: "go", Status: "completed", Conclusion: "success",
		Env:   json.RawMessage(`{"TOKEN":"supersecret"}`),
		Error: "boom TOKEN=supersecret",
		Steps: []store.Step{{
			Number: 1, Name: "test", Status: "completed", Conclusion: "success",
			Spec: json.RawMessage(`{"run":"echo supersecret"}`),
		}},
	}}
	view, pjobs := publicView(run, jobs, nil)
	b, err := json.Marshal(map[string]any{"run": view, "jobs": pjobs})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"supersecret", "TOKEN", "echo", "the gate\n"} {
		if strings.Contains(s, bad) {
			t.Fatalf("leaked %q in %s", bad, s)
		}
	}
	if view.Commit != "fix the gate" {
		t.Fatalf("commit %q", view.Commit)
	}
	if view.Actor != "" {
		t.Fatalf("email actor leaked: %q", view.Actor)
	}
	if pjobs[0].Steps[0].Name != "test" || len(pjobs[0].Steps) != 1 {
		t.Fatalf("steps %+v", pjobs[0].Steps)
	}

	login, _ := publicView(store.Run{ID: run.ID, Actor: "octocat"}, nil, nil)
	if login.Actor != "octocat" {
		t.Fatalf("login actor %q", login.Actor)
	}
}

func TestGitHubCheckAndLink(t *testing.T) {
	pending, c := githubCheck("pending", store.Run{})
	if pending != "in_progress" || c != "" {
		t.Fatalf("pending %s %s", pending, c)
	}
	st, c := githubCheck("error", store.Run{Conclusion: "cancelled"})
	if st != "completed" || c != "cancelled" {
		t.Fatalf("cancelled %s %s", st, c)
	}
	st, c = githubCheck("success", store.Run{Conclusion: "skipped"})
	if st != "completed" || c != "skipped" {
		t.Fatalf("skipped %s %s", st, c)
	}
	if runPagePath(false, "abc") != "/runs/abc" || runPagePath(true, "abc") != "/ci/abc" {
		t.Fatal(runPagePath(false, "abc"), runPagePath(true, "abc"))
	}
	desc := statusDescription(store.Run{WorkflowName: "CI"}, "failure", []store.Job{
		{Conclusion: "success"}, {Conclusion: "failure"},
	})
	if desc != "KERN CI: failure (1/2 jobs failed)" {
		t.Fatal(desc)
	}
	sum := checkSummary(store.Run{WorkflowName: "CI", RunNumber: 1, Event: "push", SHA: "abcdef1234"}, []store.Job{
		{Name: "go|test", Conclusion: "success", Steps: []store.Step{{Name: "vet", Conclusion: "success"}}},
	})
	if strings.Contains(sum, "go|test") || !strings.Contains(sum, "go/test") || !strings.Contains(sum, "abcdef1") {
		t.Fatal(sum)
	}
}

func TestJobCheckMapping(t *testing.T) {
	cases := []struct {
		state, conc, wantSt, wantConc string
	}{
		{"queued", "", "queued", ""},
		{"waiting_approval", "", "queued", ""},
		{"in_progress", "", "in_progress", ""},
		{"skipped", "", "completed", "skipped"},
		{"cancelled", "", "completed", "cancelled"},
		{"completed", "success", "completed", "success"},
		{"completed", "timed_out", "completed", "timed_out"},
		{"completed", "", "completed", "neutral"},
	}
	for _, tc := range cases {
		st, c := jobCheck(tc.state, store.Job{Conclusion: tc.conc})
		if st != tc.wantSt || c != tc.wantConc {
			t.Fatalf("%s/%s -> %s/%s, want %s/%s", tc.state, tc.conc, st, c, tc.wantSt, tc.wantConc)
		}
	}
}

// The first failing step's masked diagnosis rides the check-run summary;
// the private detail field never does (#136).
func TestCheckSummaryFailureDiagnosis(t *testing.T) {
	run := store.Run{WorkflowName: "CI", RunNumber: 2, Event: "push", SHA: "abc1234"}
	jobs := []store.Job{{
		JobKey: "build", Name: "build", Conclusion: "failure",
		Steps: []store.Step{
			{Number: 1, Name: "ok", Conclusion: "success"},
			{Number: 2, Name: "test", Conclusion: "failure", Diagnosis: json.RawMessage(
				`{"kind":"test_failure","summary":"2 tests failed","suggestion":"run go test ./...","detail":"log line"}`)},
		},
	}}
	sum := checkSummary(run, jobs)
	if !strings.Contains(sum, "test_failure") || !strings.Contains(sum, "2 tests failed") ||
		!strings.Contains(sum, "run go test") || strings.Contains(sum, "log line") {
		t.Fatal(sum)
	}
	// Failure without a diagnosis adds no section.
	sum = checkSummary(run, []store.Job{{
		JobKey: "b", Name: "b", Conclusion: "failure",
		Steps: []store.Step{{Name: "x", Conclusion: "failure"}},
	}})
	if strings.Contains(sum, "Failure —") {
		t.Fatal(sum)
	}
}

func TestOrgSettingWrites(t *testing.T) {
	off := "off"
	bad := "sometimes"
	on := "on"
	writes, msg := orgSettingWrites(&off, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if msg != "" || writes["self_heal"] != "off" || len(writes) != 1 {
		t.Fatalf("self_heal only: %v %q", writes, msg)
	}
	writes, msg = orgSettingWrites(nil, &on, nil, nil, nil, nil, nil, nil, nil, nil)
	if msg != "" || writes["public_ci"] != "on" {
		t.Fatalf("public only: %v %q", writes, msg)
	}
	writes, msg = orgSettingWrites(nil, nil, &on, nil, nil, nil, nil, nil, nil, nil)
	if msg != "" || writes["auto_update"] != "on" {
		t.Fatalf("auto_update only: %v %q", writes, msg)
	}
	if _, msg = orgSettingWrites(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil); msg == "" {
		t.Fatal("empty body must fail")
	}
	if _, msg = orgSettingWrites(&bad, nil, nil, nil, nil, nil, nil, nil, nil, nil); msg == "" {
		t.Fatal("bad self_heal must fail")
	}
	if _, msg = orgSettingWrites(nil, &bad, nil, nil, nil, nil, nil, nil, nil, nil); msg == "" {
		t.Fatal("bad public_ci must fail")
	}
	if _, msg = orgSettingWrites(nil, nil, &bad, nil, nil, nil, nil, nil, nil, nil); msg == "" {
		t.Fatal("bad auto_update must fail")
	}
	url := "https://hooks.example.com/kern"
	writes, msg = orgSettingWrites(nil, nil, nil, nil, &url, nil, nil, nil, nil, nil)
	if msg != "" || writes["notify_url"] != url {
		t.Fatalf("notify_url: %v %q", writes, msg)
	}
	badURL := "ftp://nope"
	if _, msg = orgSettingWrites(nil, nil, nil, nil, &badURL, nil, nil, nil, nil, nil); msg == "" {
		t.Fatal("non-http notify_url must fail")
	}
}

func TestDiskAlertEval(t *testing.T) {
	cases := []struct {
		free, total int64
		pct         float64
		alerted     bool
		wantAlert   bool
		wantClear   bool
	}{
		{5 << 30, 80 << 30, 10, false, true, false},  // 6.4% free: fires
		{5 << 30, 80 << 30, 10, true, false, false},  // already latched: quiet
		{9 << 30, 80 << 30, 10, true, false, false},  // 11% — below clear headroom
		{13 << 30, 80 << 30, 10, true, false, true},  // 16% >= 10+5: clears
		{9 << 30, 80 << 30, 10, false, false, false}, // above threshold, no latch
		{0, 0, 10, false, false, false},              // statfs failure
	}
	for i, c := range cases {
		alert, clear, _ := diskAlertEval(c.free, c.total, c.pct, c.alerted)
		if alert != c.wantAlert || clear != c.wantClear {
			t.Errorf("case %d: diskAlertEval = %v,%v want %v,%v", i, alert, clear, c.wantAlert, c.wantClear)
		}
	}
}

func TestLooksTransientError(t *testing.T) {
	for _, msg := range []string{
		"pull image: Get https://registry-1.docker.io/v2/: i/o timeout",
		"received status code 429 from server: Too Many Requests",
		"docker create: no space left on device",
		"read tcp: connection reset by peer",
		"create container: Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
		"docker run: error during connect: dial unix /var/run/docker.sock: connect: no such file",
	} {
		if !looksTransientError(msg) {
			t.Errorf("expected transient: %s", msg)
		}
	}
	for _, msg := range []string{
		"step failed with exit code 1",
		"image does not exist: foo",
		"",
	} {
		if looksTransientError(msg) {
			t.Errorf("expected non-transient: %s", msg)
		}
	}
	if !transientRetryKinds["network"] || !transientRetryKinds["oom"] ||
		!transientRetryKinds["daemon_gone"] || !transientRetryKinds["disk_full"] ||
		!transientRetryKinds["disk_quota"] || transientRetryKinds["script_failure"] {
		t.Error("transientRetryKinds must cover network+oom+daemon_gone+disk_full+disk_quota and exclude script_failure")
	}
	// Prepare-phase disk deaths carry no step diagnosis: they must classify
	// (and therefore retry) as disk_full, not fall into the network branch.
	for _, msg := range []string{
		"runner disk full: 42 MiB free",
		"job workspace exceeded disk_quota",
	} {
		if got := classifyJobError(msg); got != "disk_full" || !transientRetryKinds[got] {
			t.Errorf("classifyJobError(%q) = %q, want retryable disk_full", msg, got)
		}
	}
	if got := classifyJobError("create container: Cannot connect to the Docker daemon"); got != "daemon_gone" {
		t.Errorf("classifyJobError daemon = %q", got)
	}
}

// #331/#335: jobProgress narrates a live lane — current step, queue wait,
// and a median-history ETA with a job-local fallback for unrecorded steps.
func TestJobProgress(t *testing.T) {
	now := time.Now()
	started := now.Add(-90 * time.Second)
	queued := now.Add(-10 * time.Minute)
	job := store.Job{
		JobKey: "build", Status: "in_progress",
		QueuedAt: queued, StartedAt: &started,
		Steps: []store.Step{
			{Number: 1, Name: "checkout", Status: "completed", Conclusion: "success"},
			{Number: 2, Name: "build", Status: "running", StartedAt: &started},
			{Number: 3, Name: "test", Status: "queued"},
			{Number: 4, Name: "upload", Status: "queued"},
		},
	}
	med := map[string]float64{"checkout": 20, "build": 300, "test": 120}
	p := jobProgress(job, med, now)
	if p.current != "build" || p.num != 2 || p.done != 1 {
		t.Fatalf("progress = %+v, want build 2/? done=1", p)
	}
	if p.queueWait != 510 {
		t.Fatalf("queueWait = %d, want 510", p.queueWait)
	}
	// ETA = (300-90) build remainder + 120 test + fallback(120) upload.
	if p.eta != 450 {
		t.Fatalf("eta = %d, want 450", p.eta)
	}
	// No history → no ETA, still reports position.
	p = jobProgress(job, nil, now)
	if p.eta != -1 || p.current != "build" {
		t.Fatalf("no-history progress = %+v", p)
	}
	// Queued job reports live queue wait.
	qj := store.Job{JobKey: "b", Status: "queued", QueuedAt: queued}
	if p := jobProgress(qj, nil, now); p.queueWait != 600 {
		t.Fatalf("queued wait = %d, want 600", p.queueWait)
	}
}

func TestFmtSeconds(t *testing.T) {
	for in, want := range map[int64]string{45: "45s", 0: "0s", 60: "1m0s", 3725: "1h2m"} {
		if got := fmtSeconds(in); got != want {
			t.Errorf("fmtSeconds(%d) = %q, want %q", in, got, want)
		}
	}
}
