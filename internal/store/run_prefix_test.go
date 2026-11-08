package store

import (
	"strings"
	"testing"
)

func TestRunPrefix(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	params := func() CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
		}
	}
	run, _ := seedRun(t, ctx, s, params(), 1)

	// Full id resolves through the uuid path; the 8-char prefix the CLI
	// prints resolves through RunPrefix.
	short := run.ID.String()[:8]
	cands, err := s.RunPrefix(ctx, short)
	if err != nil {
		t.Fatalf("prefix lookup: %v", err)
	}
	if len(cands) != 1 || cands[0].ID != run.ID {
		t.Fatalf("prefix %q resolved to %+v, want run %s", short, cands, run.ID)
	}

	// Longer prefixes still resolve; unknown prefixes return empty.
	cands, err = s.RunPrefix(ctx, run.ID.String()[:20])
	if err != nil || len(cands) != 1 {
		t.Fatalf("20-char prefix: %v %+v", err, cands)
	}
	// A prefix containing no dash can never match id::text past the first
	// segment — 32 hex chars overflows the first uuid group, so this is
	// guaranteed empty.
	cands, err = s.RunPrefix(ctx, strings.Repeat("f", 32))
	if err != nil {
		t.Fatalf("impossible prefix: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("impossible prefix matched %d runs", len(cands))
	}
}
