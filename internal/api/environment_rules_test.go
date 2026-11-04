package api

import "testing"

func TestNormalizeEnvironment(t *testing.T) {
	ok, emails, msg := normalizeEnvironment(environmentRequest{
		Protected: true, RequiredApprovals: 1, WaitMinutes: 15,
		ReviewerEmails: []string{" A@Kern.Local ", "a@kern.local"},
		BranchPolicy:   "selected", BranchPatterns: []string{"main", " main", "release/*"},
	})
	if msg != "" {
		t.Fatal(msg)
	}
	if ok.WaitMinutes != 15 || ok.BranchPolicy != "selected" || len(ok.BranchPatterns) != 2 {
		t.Fatalf("rule = %+v", ok)
	}
	if len(emails) != 1 || emails[0] != "a@kern.local" {
		t.Fatalf("emails = %v", emails)
	}

	cases := []environmentRequest{
		{Protected: true},
		{Protected: true, BranchPolicy: "selected"},
		{Protected: true, RequiredApprovals: 2, ReviewerEmails: []string{"a@b.c"}},
		{Protected: true, WaitMinutes: 50000},
		{BranchPolicy: "protected"},
		{BranchPatterns: []string{"a[b"}},
	}
	for _, c := range cases {
		if _, _, msg := normalizeEnvironment(c); msg == "" {
			t.Errorf("expected rejection for %+v", c)
		}
	}
	if _, _, msg := normalizeEnvironment(environmentRequest{Protected: true, WaitMinutes: 5}); msg != "" {
		t.Errorf("wait-only protection rejected: %s", msg)
	}
}
