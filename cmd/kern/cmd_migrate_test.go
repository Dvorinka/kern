package main

import "testing"

func TestParseGitHubRepo(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{"Dvorinka/kern", "Dvorinka", "kern", false},
		{"dvorinka/kern.git", "dvorinka", "kern", false},
		{"https://github.com/Dvorinka/kern", "Dvorinka", "kern", false},
		{"https://github.com/Dvorinka/kern.git", "Dvorinka", "kern", false},
		{"https://github.com/Dvorinka/kern/", "Dvorinka", "kern", false},
		{"git@github.com:Dvorinka/kern.git", "Dvorinka", "kern", false},
		{"ssh://git@github.com/Dvorinka/kern.git", "Dvorinka", "kern", false},
		{"https://gitlab.com/Dvorinka/kern", "", "", true},
		{"", "", "", true},
		{"just-a-name", "", "", true},
	}
	for _, tc := range cases {
		owner, name, err := parseGitHubRepo(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseGitHubRepo(%q) = %q/%q, want error", tc.in, owner, name)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGitHubRepo(%q) error: %v", tc.in, err)
			continue
		}
		if owner != tc.wantOwner || name != tc.wantName {
			t.Errorf("parseGitHubRepo(%q) = %q/%q, want %q/%q", tc.in, owner, name, tc.wantOwner, tc.wantName)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"kern":          "kern",
		"My Cool Repo!": "my-cool-repo",
		"--already--":   "already",
		"a__b--c":       "a-b-c",
		"Repo_2024.x":   "repo-2024-x",
		"!!!":           "",
		"under_score":   "under-score",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
