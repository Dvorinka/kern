package expressions

import "testing"

func ctx() map[string]any {
	return map[string]any{
		"github": map[string]any{
			"ref":      "refs/heads/main",
			"repository": map[string]any{"name": "kern", "full_name": "dvorinka/kern"},
		},
		"matrix":  map[string]any{"go": "1.24", "os": "ubuntu-latest"},
		"needs":   map[string]any{"build": map[string]any{"result": "success", "outputs": map[string]any{"digest": "sha256:abc"}}},
		"secrets": map[string]any{"TOKEN": "s3cr3t"},
		"env":     map[string]any{"DEBUG": "true"},
		"steps":   map[string]any{"one": map[string]any{"outputs": map[string]any{"value": "42"}}},
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
		{"", true},
		{"always()", true},
		{"success()", false},
		{"failure()", true},
		{"${{ github.ref == 'refs/heads/main' }}", true},
		{"env.DEBUG == 'true'", true},
		{"env.DEBUG", true},
	}
	for _, tc := range cases {
		got, err := EvaluateCondition(tc.cond, ctx())
		if err != nil {
			t.Fatalf("%q: %v", tc.cond, err)
		}
		if got != tc.want {
			t.Errorf("%q = %v, want %v", tc.cond, got, tc.want)
		}
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
