package expressions

import (
	"os"
	"path/filepath"
	"testing"
)

func ctx() map[string]any {
	return map[string]any{
		"github": map[string]any{
			"ref":        "refs/heads/main",
			"repository": map[string]any{"name": "kern", "full_name": "dvorinka/kern"},
		},
		"matrix":       map[string]any{"go": "1.24", "os": "ubuntu-latest"},
		"needs":        map[string]any{"build": map[string]any{"result": "success", "outputs": map[string]any{"digest": "sha256:abc"}}},
		"secrets":      map[string]any{"TOKEN": "s3cr3t"},
		"env":          map[string]any{"DEBUG": "true"},
		"steps":        map[string]any{"one": map[string]any{"outputs": map[string]any{"value": "42"}}},
		"__job_status": "failure",
	}
}

func TestInterpolate(t *testing.T) {
	cases := []struct{ tmpl, want string }{
		{"no expressions", "no expressions"},
		{"ref=${{ github.ref }}", "ref=refs/heads/main"},
		{"${{ matrix.go }}", "1.24"},
		{"a${{ matrix.go }}b${{ matrix.os }}", "a1.24bubuntu-latest"},
		{"${{ needs.build.outputs.digest }}", "sha256:abc"},
		{"${{ env.MISSING }}", ""},
		{"${{ 1 == 1 }}", "true"},
	}
	for _, tc := range cases {
		got, err := Interpolate(tc.tmpl, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.tmpl, err)
		}
		if got != tc.want {
			t.Errorf("%q = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestEvaluateOperators(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"1 == 1", true},
		{"'a' == 'A'", true},
		{"1 != 2", true},
		{"2 > 1 && 3 >= 3", true},
		{"false || true", true},
		{"!false", true},
		{"contains('refs/heads/main', 'main')", true},
		{"!contains(github.ref, 'tags')", true},
		{"startsWith(github.ref, 'refs/heads/')", true},
		{"endsWith(github.repository.name, 'rn')", true},
		{"format('{0}-{1}', matrix.os, matrix.go)", "ubuntu-latest-1.24"},
		{"join(fromJSON('[\"a\",\"b\"]'), '/')", "a/b"},
		{"toJSON(matrix.os)", `"ubuntu-latest"`},
		{"fromJSON('{\"ok\":true}').ok", true},
		{"needs.build.outputs.digest == 'sha256:abc'", true},
		{"steps.one.outputs.value", "42"},
	}
	for _, tc := range cases {
		got, err := Evaluate(tc.expr, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%q = %#v, want %#v", tc.expr, got, tc.want)
		}
	}
}

func TestConditions(t *testing.T) {
	cases := []struct {
		cond string
		want bool
	}{
		{"always()", true},
		{"success()", false},
		{"failure()", true},
		{"cancelled()", false},
		// Implicit success() applies when no status check function is present.
		{"", false},
		{"${{ github.ref == 'refs/heads/main' }}", false},
		{"env.DEBUG == 'true'", false},
		{"env.DEBUG", false},
		{"github.ref == 'refs/heads/main' || failure()", true},
	}
	for _, tc := range cases {
		got, err := EvaluateCondition(tc.cond, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.cond, err)
		}
		if got != tc.want {
			t.Errorf("%q = %v, want %v (job_status=failure)", tc.cond, got, tc.want)
		}
	}
}

func TestConditionsSuccessStatus(t *testing.T) {
	ok := ctx()
	ok["__job_status"] = "success"
	cases := []struct {
		cond string
		want bool
	}{
		{"", true},
		{"env.DEBUG == 'true'", true},
		{"env.DEBUG", true},
		{"failure()", false},
		{"always()", true},
		{"success() && github.ref == 'refs/heads/main'", true},
	}
	for _, tc := range cases {
		got, err := EvaluateCondition(tc.cond, ok)
		if err != nil {
			t.Fatalf("%q: %v", tc.cond, err)
		}
		if got != tc.want {
			t.Errorf("%q = %v, want %v (job_status=success)", tc.cond, got, tc.want)
		}
	}
}

func TestHashFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.lock"), []byte("aaa"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "b.lock"), []byte("bbb"), 0o644)
	c := ctx()
	c["__workspace"] = dir

	got, err := Evaluate(`hashFiles('**/*.lock')`, c)
	if err != nil {
		t.Fatalf("hashFiles: %v", err)
	}
	sum, ok := got.(string)
	if !ok || len(sum) != 64 {
		t.Fatalf("hashFiles = %#v, want 64-char hex", got)
	}

	// Deterministic, and changes when a matched file changes.
	got2, _ := Evaluate(`hashFiles('**/*.lock')`, c)
	if got2 != got {
		t.Error("hashFiles not deterministic")
	}
	_ = os.WriteFile(filepath.Join(dir, "a.lock"), []byte("changed"), 0o644)
	got3, _ := Evaluate(`hashFiles('**/*.lock')`, c)
	if got3 == got {
		t.Error("hashFiles unchanged after content change")
	}

	// No workspace context → diagnostic, the job-level rejection path.
	if _, err := Evaluate(`hashFiles('x')`, ctx()); err == nil {
		t.Error("expected error without __workspace")
	}
}

func TestErrors(t *testing.T) {
	if _, err := Interpolate("${{ github.ref", ctx()); err == nil {
		t.Error("expected unterminated expression error")
	}
	if _, err := Evaluate("nosuchfn(1)", ctx()); err == nil {
		t.Error("expected unknown function error")
	}
	if _, err := Evaluate("${{ }}", ctx()); err == nil {
		t.Error("expected empty expression error")
	}
	if _, err := Evaluate("1 +", ctx()); err == nil {
		t.Error("expected syntax error")
	}
}

func TestTruthy(t *testing.T) {
	for _, v := range []any{nil, "", "0", float64(0), false} {
		if Truthy(v) {
			t.Errorf("%#v should be falsey", v)
		}
	}
	for _, v := range []any{"false", "x", float64(1), true, []any{}, map[string]any{}} {
		if !Truthy(v) {
			t.Errorf("%#v should be truthy", v)
		}
	}
}

func TestHyphenatedIdentifiers(t *testing.T) {
	ctx := map[string]any{
		"inputs": map[string]any{"scan-type": "fs"},
		"steps":  map[string]any{"dl": map[string]any{"outputs": map[string]any{"cache-hit": "true"}}},
	}
	ok, err := EvaluateCondition("${{ inputs.scan-type == 'fs' }}", ctx)
	if err != nil || !ok {
		t.Errorf("hyphenated input: ok=%v err=%v", ok, err)
	}
	ok, err = EvaluateCondition("${{ steps.dl.outputs.cache-hit == 'true' }}", ctx)
	if err != nil || !ok {
		t.Errorf("hyphenated step output: ok=%v err=%v", ok, err)
	}
}

func TestNumberLiterals(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"-9.2", -9.2},
		{"+3", 3.0},
		{".5", 0.5},
		{"1.", 1.0},
		{"-2.99e-2", -0.0299},
		{"1e3", 1000.0},
		{"0xff", 255.0},
		{"-0x10", -16.0},
		{"0o777", 511.0},
		{"01", 1.0},
		{"NaN == NaN", false},
		{"Infinity > 1e10", true},
		{"-Infinity < 0", true},
	}
	for _, tc := range cases {
		got, err := Evaluate(tc.expr, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%q = %#v, want %#v", tc.expr, got, tc.want)
		}
	}
	// Signs outside literals are still errors — GitHub has no arithmetic.
	for _, bad := range []string{"1 + 2", "1 - 2", "2 * 3", "4 / 2"} {
		if _, err := Evaluate(bad, ctx()); err == nil {
			t.Errorf("%q should be a syntax error", bad)
		}
	}
}

func TestObjectFilters(t *testing.T) {
	c := ctx()
	c["needs"] = map[string]any{
		"build": map[string]any{"result": "success"},
		"test":  map[string]any{"result": "failure"},
		"lint":  map[string]any{"outputs": map[string]any{"x": "1"}}, // no result key
	}
	c["github"] = map[string]any{
		"event": map[string]any{
			"issue": map[string]any{
				"labels": []any{
					map[string]any{"name": "bug"},
					map[string]any{"name": "ui"},
				},
			},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		// needs.*.result skips the lint job (missing key), keeps order.
		{"join(needs.*.result, ',')", "success,failure"},
		{"contains(needs.*.result, 'failure')", true},
		{"contains(needs.*.result, 'skipped')", false},
		// Array-of-objects filter then deref.
		{"join(github.event.issue.labels.*.name, ',')", "bug,ui"},
		// Chained wildcard flattens one level.
		{"join(matrix.*.* , '-')", ""}, // matrix values are scalars → empty
		// Object filter over a plain object yields sorted values.
		{"join(github.event.issue.labels.*, '-')", "Object-Object"},
	}
	for _, tc := range cases {
		got, err := Evaluate(tc.expr, c)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%q = %#v, want %#v", tc.expr, got, tc.want)
		}
	}
	// needs.* itself is an array of the job objects.
	got, err := Evaluate("needs.*", c)
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := got.([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("needs.* = %#v, want 3-item array", got)
	}
	// Flattening: a.*.* over nested arrays.
	c["nested"] = map[string]any{"a": []any{1.0, 2.0}, "b": []any{3.0}}
	got, err = Evaluate("join(nested.*.*, '+')", c)
	if err != nil || got != "1+2+3" {
		t.Fatalf("nested.*.* = %#v err=%v, want \"1+2+3\"", got, err)
	}
}

func TestGitHubEqualitySemantics(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"'01' == '1'", false}, // string-kind compare, no numeric coercion
		{"'01' == 1", true},    // string→number coercion across kinds
		{"1 == '1.0'", true},
		{"'true' == true", false}, // 'true' parses to NaN, true → 1
		{"'1' == true", true},     // coerces: '1'→1, true→1
		{"'' == false", true},     // '' → 0, false → 0
		{"null == false", true},   // null → 0
		{"null == 0", true},
		{"null == null", true},
		{"'10' < '9'", true},                              // both strings → lexicographic, NOT numeric
		{"10 < '9'", false},                               // number vs string → numeric: 10 < 9 false
		{"'2' > '10'", true},                              // string compare '2' > '1'
		{"true > false", true},                            // false < true ordering
		{"fromJSON('[1,2]') == fromJSON('[1,2]')", false}, // collections: identity
		{"fromJSON('[1,2]') != fromJSON('[1,2]')", true},
		{"contains(fromJSON('[1,2]'), 2)", true},
		{"contains(fromJSON('[1,2]'), '2')", true}, // AbstractEqual coerces
		{"contains(fromJSON('{\"a\":1}'), 'a')", false},
	}
	for _, tc := range cases {
		got, err := Evaluate(tc.expr, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%q = %#v, want %#v", tc.expr, got, tc.want)
		}
	}
}

func TestFunctionSemantics(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"join(matrix)", ""},   // object → empty string
		{"join('abc')", "abc"}, // primitive passthrough
		{"join(fromJSON('[1,true,null]'))", "1,true,"},
		{"join(fromJSON('[\"a\",\"b\"]'), ' / ')", "a / b"},
		{"case(false, 'x', true, 'y', 'd')", "y"},
		{"case(false, 'x', false, 'y', 'd')", "d"},
		{"case(true, 1, 2)", 1.0},
		{"toJSON(fromJSON('{\"a\":1}'))", "{\n  \"a\": 1\n}"},
	}
	for _, tc := range cases {
		got, err := Evaluate(tc.expr, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%q = %#v, want %#v", tc.expr, got, tc.want)
		}
	}
	// case() rejects even arg counts and non-boolean predicates.
	for _, bad := range []string{"case(true, 'x')", "case('yes', 'x', 'd')"} {
		if _, err := Evaluate(bad, ctx()); err == nil {
			t.Errorf("%q should error", bad)
		}
	}
}
