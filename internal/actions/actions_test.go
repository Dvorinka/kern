package actions

import "testing"

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"actions/checkout@v4":       KindNative,
		"actions/cache/restore@v3":  KindNative,
		"./.github/actions/build":   KindLocal,
		"./":                        KindLocal,
		"docker://alpine:3.19":      KindDocker,
		"owner/repo@v1":             KindRemote,
		"owner/repo/sub/dir@abc123": KindRemote,
		"actions/setup-node@v4":     KindRemote, // JS action: remote-shaped, fails at run
		"":                          KindInvalid,
	}
	for uses, want := range cases {
		if got := Classify(uses); got != want {
			t.Errorf("Classify(%q) = %d, want %d", uses, got, want)
		}
	}
}

func TestValidLocal(t *testing.T) {
	for _, u := range []string{"./x", "./a/b", "./"} {
		if err := ValidLocal(u); err != nil {
			t.Errorf("ValidLocal(%q): %v", u, err)
		}
	}
	for _, u := range []string{"./x@v1", "./../etc", "./a/../../b"} {
		if err := ValidLocal(u); err == nil {
			t.Errorf("ValidLocal(%q) should fail", u)
		}
	}
}

func TestParseRemote(t *testing.T) {
	r, err := ParseRemote("owner/repo/sub/path@v2")
	if err != nil {
		t.Fatal(err)
	}
	if r.Owner != "owner" || r.Repo != "repo" || r.Path != "sub/path" || r.Ref != "v2" {
		t.Errorf("unexpected ref: %+v", r)
	}
	r, err = ParseRemote("owner/repo@main")
	if err != nil || r.Path != "" {
		t.Errorf("root action: %+v err=%v", r, err)
	}
	for _, bad := range []string{"owner/repo", "owner/repo@", "noslash@v1", "a b/c@v1"} {
		if _, err := ParseRemote(bad); err == nil {
			t.Errorf("ParseRemote(%q) should fail", bad)
		}
	}
}

func TestParseMeta(t *testing.T) {
	m, err := ParseMeta([]byte(`
name: greet
inputs:
  who:
    required: true
  punctual:
    default: "yes"
outputs:
  message:
    value: ${{ steps.say.outputs.msg }}
runs:
  using: composite
  steps:
    - id: say
      run: echo "msg=hi ${{ inputs.who }}" >> $GITHUB_OUTPUT
      shell: bash
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Runs.Using != "composite" || len(m.Runs.Steps) != 1 {
		t.Fatalf("bad meta: %+v", m.Runs)
	}
	if !m.Inputs["who"].Required || m.Inputs["punctual"].Default != "yes" {
		t.Errorf("inputs: %+v", m.Inputs)
	}
	if m.Outputs["message"].Value != "${{ steps.say.outputs.msg }}" {
		t.Errorf("outputs: %+v", m.Outputs)
	}
}
