package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMembersLifecycle(t *testing.T) {
	s, ctx := testStore(t)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	owner, err := s.UserByEmail(ctx, "it@kern.local")
	if err != nil {
		t.Fatalf("owner: %v", err)
	}

	member, err := s.CreateMember(ctx, org.ID, "m-"+uuid.NewString()[:8]+"@it.local", "Member", "x", "member")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if member.Role != "member" {
		t.Fatalf("role %q", member.Role)
	}
	if _, err := s.CreateMember(ctx, org.ID, member.Email, "Dup", "x", "member"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate email: %v", err)
	}

	// The last owner cannot be demoted or removed.
	if err := s.SetMemberRole(ctx, org.ID, owner.ID, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("demote last owner: %v", err)
	}
	if err := s.RemoveMember(ctx, org.ID, owner.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("remove last owner: %v", err)
	}

	// A second owner unlocks demotion.
	owner2, err := s.CreateMember(ctx, org.ID, "o-"+uuid.NewString()[:8]+"@it.local", "Owner Two", "x", "owner")
	if err != nil {
		t.Fatalf("create owner2: %v", err)
	}
	if err := s.SetMemberRole(ctx, org.ID, member.ID, "admin"); err != nil {
		t.Fatalf("promote member: %v", err)
	}
	if err := s.SetMemberRole(ctx, org.ID, owner.ID, "admin"); err != nil {
		t.Fatalf("demote first owner with second present: %v", err)
	}

	// Removal kills sessions and API tokens, not just the membership.
	if err := s.CreateSession(ctx, member.ID, "deadhash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("session: %v", err)
	}
	if _, err := s.CreateAPIToken(ctx, member.ID, "t", "p", "deadtokenhash", []string{"read"}, nil); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := s.RemoveMember(ctx, org.ID, member.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if _, err := s.UserBySession(ctx, "deadhash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived removal: %v", err)
	}
	if _, _, err := s.UserByAPIToken(ctx, "deadtokenhash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token survived removal: %v", err)
	}

	members, err := s.Members(ctx, org.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	var seen bool
	for _, m := range members {
		if m.ID == owner2.ID {
			seen = true
		}
		if m.ID == member.ID {
			t.Fatalf("removed member still listed")
		}
	}
	if !seen {
		t.Fatalf("owner2 missing from members list")
	}
}
