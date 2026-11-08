package store

import "testing"

func TestProjectVars(t *testing.T) {
	s, ctx := testStore(t)
	projectID, _, _ := fixture(t, ctx, s)

	vars, err := s.VarsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected no vars, got %d", len(vars))
	}

	for _, kv := range [][2]string{{"B_VAR", "two"}, {"A_VAR", "one"}} {
		if err := s.UpsertVar(ctx, projectID, kv[0], kv[1]); err != nil {
			t.Fatalf("upsert %s: %v", kv[0], err)
		}
	}

	vars, err = s.VarsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vars) != 2 || vars[0].Name != "A_VAR" || vars[0].Value != "one" ||
		vars[1].Name != "B_VAR" || vars[1].Value != "two" {
		t.Fatalf("unexpected vars: %+v", vars)
	}

	// Upsert overwrites the value in place.
	if err := s.UpsertVar(ctx, projectID, "A_VAR", "uno"); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	vars, err = s.VarsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if len(vars) != 2 || vars[0].Value != "uno" {
		t.Fatalf("upsert did not overwrite: %+v", vars)
	}

	// Vars are project-scoped: a second project sees none of them.
	other, _, _ := fixture(t, ctx, s)
	vars, err = s.VarsForProject(ctx, other)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("vars leaked across projects: %+v", vars)
	}

	if err := s.DeleteVar(ctx, projectID, "A_VAR"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	vars, err = s.VarsForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "B_VAR" {
		t.Fatalf("unexpected vars after delete: %+v", vars)
	}

	if err := s.DeleteVar(ctx, projectID, "A_VAR"); err != ErrNotFound {
		t.Fatalf("delete missing: got %v, want ErrNotFound", err)
	}
}
