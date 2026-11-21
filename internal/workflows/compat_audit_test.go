package workflows

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIncludeOnlyMatrixExpandsAllEntries(t *testing.T) {
	src := `
name: include-only
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - target: linux
            arch: amd64
          - target: darwin
            arch: arm64
          - target: windows
            arch: amd64
    steps:
      - run: echo ${{ matrix.target }}
`
	res, err := Compile(src, Compiler{DefaultImage: "ubuntu:24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("expected valid compile, diags=%+v", res.Diagnostics)
	}
	if len(res.Plan.Jobs) != 3 {
		t.Fatalf("include-only matrix produced %d jobs, want 3", len(res.Plan.Jobs))
	}
}

func TestMatrixCartesianOOMGuard(t *testing.T) {
	// 20^4 = 160,000 combinations — must abort before allocating.
	axis := make([]string, 20)
	for i := range axis {
		axis[i] = fmt.Sprintf("v%d", i)
	}
	list := "[" + strings.Join(axis, ", ") + "]"
	src := fmt.Sprintf(`
name: huge
on: push
jobs:
  boom:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        a: %s
        b: %s
        c: %s
        d: %s
    steps:
      - run: echo hi
`, list, list, list, list)
	res, err := Compile(src, Compiler{DefaultImage: "ubuntu:24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("expected huge Cartesian matrix to fail validation")
	}
}

func TestNestedReusableSecretsDoNotLeakAndInputsStayIntact(t *testing.T) {
	wfC := `
name: C
on:
  workflow_call:
    inputs:
      tag:
        required: true
jobs:
  leaf:
    runs-on: ubuntu-latest
    steps:
      - run: echo ${{ inputs.tag }}
`
	wfB := `
name: B
on:
  workflow_call:
    inputs:
      env:
        required: true
jobs:
  call_c:
    uses: ./.github/workflows/c.yml
    with:
      tag: from-b
`
	wfA := `
name: A
on: push
jobs:
  call_b:
    uses: ./.github/workflows/b.yml
    with:
      env: prod
    secrets: inherit
`
	res, err := Compile(wfA, Compiler{
		DefaultImage: "ubuntu:24.04",
		Resolve: func(ref string) (string, error) {
			switch ref {
			case "./.github/workflows/b.yml":
				return wfB, nil
			case "./.github/workflows/c.yml":
				return wfC, nil
			default:
				return "", fmt.Errorf("unexpected ref %s", ref)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("compile failed: %+v", res.Diagnostics)
	}
	var leaf *JobPlan
	for i := range res.Plan.Jobs {
		if strings.HasSuffix(res.Plan.Jobs[i].JobID, "leaf") {
			leaf = &res.Plan.Jobs[i]
		}
	}
	if leaf == nil {
		t.Fatalf("leaf job missing from plan: %+v", res.Plan.Jobs)
	}
	// A --inherit--> B --(no secrets)--> C: C must receive empty map (no secrets), NOT nil (inherit).
	if leaf.SecretsMap == nil || len(leaf.SecretsMap) != 0 {
		t.Fatalf("leaf SecretsMap = %#v, want non-nil empty map (no secret leak)", leaf.SecretsMap)
	}
	if leaf.Inputs["tag"] != "from-b" {
		t.Fatalf("leaf Inputs = %#v, want tag=from-b (must not be clobbered by A's inputs)", leaf.Inputs)
	}
}

func TestCronDOW7AndNamesAndFastJump(t *testing.T) {
	// Saturday 2026-01-03 23:59 UTC -> Sunday 2026-01-04 00:00 UTC
	sat := time.Date(2026, 1, 3, 23, 59, 0, 0, time.UTC)
	for _, expr := range []string{"0 0 * * 5-7", "0 0 * * 1-7", "0 0 * * SUN", "0 0 * JAN 7"} {
		next, err := NextCron(expr, sat)
		if err != nil {
			t.Fatalf("NextCron(%q): %v", expr, err)
		}
		if next.Weekday() != time.Sunday || next.Day() != 4 {
			t.Fatalf("NextCron(%q) = %v (%s), want Sun Jan 4", expr, next, next.Weekday())
		}
	}
	// Leap day Feb 29 (2028-02-29) must resolve fast via field jumping.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	leap, err := NextCron("0 12 29 FEB *", start)
	if err != nil {
		t.Fatalf("leap day cron failed: %v", err)
	}
	if leap.Year() != 2028 || leap.Month() != time.February || leap.Day() != 29 {
		t.Fatalf("leap day = %v, want 2028-02-29", leap)
	}
}

func TestGHAYAMLModelCompat(t *testing.T) {
	src := `
name: gha-compat
on: push
permissions: read-all
concurrency: ci-main
jobs:
  build:
    runs-on:
      group: fleet
      labels: [ubuntu-latest]
    concurrency:
      group: build-${{ github.ref }}
      cancel-in-progress: ${{ !github.ref_protected }}
    continue-on-error: ${{ matrix.experimental }}
    timeout-minutes: ${{ inputs.timeout }}
    strategy:
      fail-fast: ${{ inputs.fast }}
      max-parallel: 2
    defaults:
      run:
        shell: pwsh
    steps:
      - run: echo ok
        continue-on-error: ${{ inputs.allow_fail }}
        timeout-minutes: ${{ inputs.step_timeout }}
`
	res, err := Compile(src, Compiler{DefaultImage: "ubuntu:24.04"})
	if err != nil {
		t.Fatalf("YAML decode failed: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid compile, diags=%+v", res.Diagnostics)
	}
	if res.Plan.Concurrency != "ci-main" {
		t.Fatalf("scalar concurrency = %q, want ci-main", res.Plan.Concurrency)
	}
	j := res.Plan.Jobs[0]
	if j.Permissions["contents"] != "read" {
		t.Fatalf("permissions: read-all not expanded: %+v", j.Permissions)
	}
	if len(j.RunsOn) != 2 || j.RunsOn[0] != "fleet" || j.RunsOn[1] != "ubuntu-latest" {
		t.Fatalf("runs-on mapping = %v, want [fleet ubuntu-latest]", j.RunsOn)
	}
	// Verify job-level concurrency and strategy.max-parallel produce explicit warnings (never silent).
	var sawJobConc, sawMaxPar bool
	for _, d := range res.Diagnostics {
		if d.Code == "concurrency" && strings.Contains(d.Path, "jobs.build.concurrency") {
			sawJobConc = true
		}
		if d.Code == "strategy" && strings.Contains(d.Path, "max-parallel") {
			sawMaxPar = true
		}
	}
	if !sawJobConc || !sawMaxPar {
		t.Fatalf("expected warnings for job concurrency (%v) and max-parallel (%v), diags=%+v", sawJobConc, sawMaxPar, res.Diagnostics)
	}
}
