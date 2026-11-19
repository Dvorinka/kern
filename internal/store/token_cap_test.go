package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// A dispatch token carrying max_concurrent_jobs holds at most that many
// in-progress jobs at once; further queued work from the same token waits
// while other callers' jobs still claim normally (#356).
func TestTokenConcurrencyCap(t *testing.T) {
	s, ctx := testStore(t)
	r := labeledRunner(t, ctx, s, "cap-runner", []string{"ubuntu-latest"}, "docker")
	owner, err := s.UserByEmail(ctx, "it@kern.local")
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	tok, err := s.CreateAPIToken(ctx, owner.ID, "cap", "p", "hash-"+uuid.NewString()[:8],
		[]string{"read", "write"}, nil, nil, nil, 2)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.MaxConcurrentJobs != 2 {
		t.Fatalf("cap = %d", tok.MaxConcurrentJobs)
	}

	pid, wid, vid := fixture(t, ctx, s)
	params := func(tokenID uuid.UUID) CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
			DispatchTokenID: tokenID,
		}
	}

	// Two runs from the capped token claim their jobs.
	for i := 0; i < 2; i++ {
		seedDockerRun(t, ctx, s, params(tok.ID), false, []string{"ubuntu-latest"})
		if c, err := s.ClaimJob(ctx, r.ID, 30*time.Second); err != nil || c == nil {
			t.Fatalf("claim %d under cap: %v %v", i+1, c, err)
		}
	}
	// The third stays queued — the token is at its cap.
	seedDockerRun(t, ctx, s, params(tok.ID), false, []string{"ubuntu-latest"})
	if c, err := s.ClaimJob(ctx, r.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("claim over cap: %v %v", c, err)
	}
	// A session-dispatched run (no token) is unaffected.
	seedDockerRun(t, ctx, s, params(uuid.Nil), false, []string{"ubuntu-latest"})
	if c, err := s.ClaimJob(ctx, r.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("uncapped caller starved by token cap: %v %v", c, err)
	}
}
