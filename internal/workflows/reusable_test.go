package workflows

import (
	"strings"
	"testing"
)

const calledWorkflowYAML = `on:
  workflow_call:
    inputs:
      who:
        required: true
    outputs:
      greeting:
        value: ${{ jobs.say.outputs.greet }}
jobs:
  say:
    runs-on: self-hosted
    outputs:
      greet: ${{ steps.s.outputs.greet }}
    steps:
      - id: s
        run: echo "greet=hello ${{ inputs.who }}" >> "$GITHUB_OUTPUT"
  late:
    runs-on: self-hosted
    needs: say
    steps:
      - run: echo "needs={{ needs.say.outputs.greet }}"
`

const callerWorkflowYAML = `on: push
jobs:
  prep:
    runs-on: self-hosted
    steps:
      - run: echo prep
  call:
    needs: prep
    uses: ./.github/workflows/called.yml
    with:
      who: kern
  after:
    runs-on: self-hosted
    needs: call
    steps:
      - run: echo "got ${{ needs.call.outputs.greeting }}"
`

func resolverFor(deps map[string]string) func(string) (string, error) {
	return func(uses string) (string, error) {
		if c, ok := deps[uses]; ok {
			return c, nil
		}
		return "", &missingDep{uses}
	}
}

type missingDep struct{ uses string }

func (e *missingDep) Error() string { return "unknown uses " + e.uses }

func compileWith(t *testing.T, src string, deps map[string]string) Result {
	t.Helper()
	c := testCompiler
	c.Resolve = resolverFor(deps)
	res, err := Compile(src, c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReusableWorkflowExpansion(t *testing.T) {
	res := compileWith(t, callerWorkflowYAML, map[string]string{
		"./.github/workflows/called.yml": calledWorkflowYAML,
	})
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	plan := res.Plan
	byJobID := map[string]*JobPlan{}
	for i := range plan.Jobs {
		byJobID[plan.Jobs[i].JobID] = &plan.Jobs[i]
	}
	say, ok := byJobID["call_say"]
	if !ok {
		t.Fatalf("expanded job call_say missing; jobs: %+v", jobIDsOf(plan))
	}
	if say.Inputs["who"] != "kern" {
		t.Fatalf("call_say inputs: %+v", say.Inputs)
	}
	if len(say.Needs) != 1 || say.Needs[0] != "prep" {
		t.Fatalf("call_say needs (caller needs must fan in): %v", say.Needs)
	}
	late, ok := byJobID["call_late"]
	if !ok || len(late.Needs) != 1 || late.Needs[0] != "call_say" {
		t.Fatalf("call_late needs: %+v", late)
	}
	boundary, ok := byJobID["call"]
	if !ok {
		t.Fatal("boundary job for caller id missing")
	}
	if len(boundary.Needs) != 2 {
		t.Fatalf("boundary must wait on all called jobs: %v", boundary.Needs)
	}
	got := boundary.Outputs["greeting"]
	want := "${{ needs.call_say.outputs.greet }}"
	if got != want {
		t.Fatalf("boundary output rewrite = %q, want %q", got, want)
	}
	after := byJobID["after"]
	if len(after.Needs) != 1 || after.Needs[0] != "call" {
		t.Fatalf("downstream needs must target the caller id: %v", after.Needs)
	}
	if got := res.Deps["./.github/workflows/called.yml"]; got != calledWorkflowYAML {
		t.Fatal("resolved dep content not recorded")
	}
}

func jobIDsOf(p *Plan) []string {
	var ids []string
	for _, j := range p.Jobs {
		ids = append(ids, j.JobID)
	}
	return ids
}

func TestReusableWorkflowCallerIfGatesSubgraph(t *testing.T) {
	src := strings.Replace(callerWorkflowYAML, "    uses:", "    if: github.ref == 'refs/heads/main'\n    uses:", 1)
	res := compileWith(t, src, map[string]string{
		"./.github/workflows/called.yml": calledWorkflowYAML,
	})
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	for _, j := range res.Plan.Jobs {
		if j.JobID == "call_say" && !strings.Contains(j.If, "github.ref") {
			t.Fatalf("caller if must reach called jobs: %q", j.If)
		}
	}
}

func TestReusableWorkflowDiagnostics(t *testing.T) {
	deps := map[string]string{"./.github/workflows/called.yml": calledWorkflowYAML}
	cases := []struct {
		name string
		src  string
		code string
	}{
		{"missing required input", `on: push
jobs:
  call:
    uses: ./.github/workflows/called.yml
`, "reusable-inputs"},
		{"caller has steps", `on: push
jobs:
  call:
    uses: ./.github/workflows/called.yml
    steps:
      - run: x
`, "reusable-caller"},
		{"secrets scalar", `on: push
jobs:
  call:
    uses: ./.github/workflows/called.yml
    with:
      who: bob
    secrets: 42
`, "reusable-secrets"},
		{"local ref with @", `on: push
jobs:
  call:
    uses: ./.github/workflows/called.yml@main
`, "reusable-workflow"},
		{"no workflow_call", `on: push
jobs:
  call:
    uses: ./.github/workflows/called.yml
`, "reusable-trigger"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := deps
			if tc.code == "reusable-trigger" {
				d = map[string]string{"./.github/workflows/called.yml": "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: y\n"}
			}
			res := compileWith(t, tc.src, d)
			if res.Valid {
				t.Fatalf("expected invalid; plan jobs: %v", jobIDsOf(res.Plan))
			}
			if !hasDiagnostic(res, tc.code) {
				t.Fatalf("want diagnostic %s, got %+v", tc.code, res.Diagnostics)
			}
		})
	}
}

func TestReusableWorkflowNestedAndCaps(t *testing.T) {
	// A calls B calls C — two levels resolve and expand.
	inner := `on:
  workflow_call:
jobs:
  deep:
    runs-on: self-hosted
    steps:
      - run: echo deep
`
	mid := `on:
  workflow_call:
jobs:
  m:
    uses: ./.github/workflows/inner.yml
`
	outer := `on: push
jobs:
  o:
    uses: ./.github/workflows/mid.yml
`
	res := compileWith(t, outer, map[string]string{
		"./.github/workflows/mid.yml":   mid,
		"./.github/workflows/inner.yml": inner,
	})
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	byJobID := map[string]bool{}
	for _, j := range res.Plan.Jobs {
		byJobID[j.JobID] = true
	}
	if !byJobID["o_m_deep"] {
		t.Fatalf("nested expansion missing o_m_deep: %v", jobIDsOf(res.Plan))
	}
	if !byJobID["o"] || !byJobID["o_m"] {
		t.Fatalf("boundary jobs missing: %v", jobIDsOf(res.Plan))
	}
}

func TestReusableWorkflowSecretsInherit(t *testing.T) {
	src := strings.Replace(callerWorkflowYAML, "    uses:", "    secrets: inherit\n    uses:", 1)
	res := compileWith(t, src, map[string]string{
		"./.github/worktrees/called.yml": calledWorkflowYAML,
		"./.github/workflows/called.yml": calledWorkflowYAML,
	})
	if !res.Valid {
		t.Fatalf("secrets: inherit must compile: %+v", res.Diagnostics)
	}
	for _, d := range res.Diagnostics {
		if d.Code == "reusable-caller" && strings.Contains(d.Path, "secrets") {
			t.Fatalf("inherit must not diagnose: %+v", d)
		}
	}
	// Called jobs run in the caller's project; 'inherit' stamps nil so the
	// per-job secrets endpoint delivers the full eligible set.
	for _, j := range res.Plan.Jobs {
		if !strings.HasPrefix(j.JobID, "call_") {
			continue
		}
		if j.JobID == "call_say" && len(j.Steps) == 0 {
			t.Fatal("expanded job lost its steps")
		}
		if j.SecretsMap != nil {
			t.Fatalf("inherit must leave SecretsMap nil, got %v", j.SecretsMap)
		}
	}
}

func TestReusableWorkflowSecretsMap(t *testing.T) {
	src := strings.Replace(callerWorkflowYAML, "    uses:", `    secrets:
      CALLED_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
      REGION: eu-west
    uses:`, 1)
	res := compileWith(t, src, map[string]string{
		"./.github/workflows/called.yml": calledWorkflowYAML,
	})
	if !res.Valid {
		t.Fatalf("explicit secrets map must compile: %+v", res.Diagnostics)
	}
	for _, j := range res.Plan.Jobs {
		switch j.JobID {
		case "call_say", "call_late":
			if j.SecretsMap["CALLED_TOKEN"] != "${{ secrets.DEPLOY_TOKEN }}" ||
				j.SecretsMap["REGION"] != "eu-west" || len(j.SecretsMap) != 2 {
				t.Fatalf("%s secrets map = %v", j.JobID, j.SecretsMap)
			}
		case "call":
			if j.SecretsMap == nil || len(j.SecretsMap) != 0 {
				t.Fatalf("boundary must get an empty secrets map, got %v", j.SecretsMap)
			}
		case "prep", "after":
			if j.SecretsMap != nil {
				t.Fatalf("normal job %s must keep nil SecretsMap, got %v", j.JobID, j.SecretsMap)
			}
		}
	}
}

func TestReusableWorkflowSecretsAbsentMeansNone(t *testing.T) {
	res := compileWith(t, callerWorkflowYAML, map[string]string{
		"./.github/workflows/called.yml": calledWorkflowYAML,
	})
	if !res.Valid {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	for _, j := range res.Plan.Jobs {
		if j.JobID == "call_say" || j.JobID == "call_late" {
			if j.SecretsMap == nil || len(j.SecretsMap) != 0 {
				t.Fatalf("omitted secrets must stamp an empty map on %s, got %v", j.JobID, j.SecretsMap)
			}
		}
	}
}

func TestReusableWorkflowSecretsInvalid(t *testing.T) {
	for _, bad := range []string{
		"    secrets: 42\n",
		"    secrets: [a, b]\n",
		"    secrets: notinherit\n",
	} {
		src := strings.Replace(callerWorkflowYAML, "    uses:", bad+"    uses:", 1)
		res := compileWith(t, src, map[string]string{
			"./.github/workflows/called.yml": calledWorkflowYAML,
		})
		found := false
		for _, d := range res.Diagnostics {
			if d.Code == "reusable-secrets" {
				found = true
			}
		}
		if res.Valid || !found {
			t.Fatalf("secrets %q must fail with reusable-secrets, valid=%v diags=%+v", bad, res.Valid, res.Diagnostics)
		}
	}
}
