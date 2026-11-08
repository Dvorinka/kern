package workflows

import (
	"strings"

	"github.com/dvorinka/kern/internal/expressions"
)

// failFast resolves strategy.fail-fast, which defaults to true like GitHub Actions.
func failFast(job *Job) bool {
	if job.Strategy == nil || job.Strategy.FailFast == nil {
		return true
	}
	return *job.Strategy.FailFast
}

// evalWholeExpr reports whether label is a single ${{ }} expression spanning
// the whole string and, when it evaluates to a list, returns its elements as
// label strings. Scalars and non-list results return ok=false so the caller
// falls through to plain interpolation, preserving prior behavior.
func evalWholeExpr(label string, ctx map[string]any) ([]string, bool) {
	t := strings.TrimSpace(label)
	if !strings.HasPrefix(t, "${{") || !strings.HasSuffix(t, "}}") {
		return nil, false
	}
	v, err := expressions.Evaluate(strings.TrimSpace(t[3:len(t)-2]), ctx)
	if err != nil {
		return nil, false
	}
	var arr []any
	switch a := v.(type) {
	case []any:
		arr = a
	case []string:
		arr = make([]any, len(a))
		for i, s := range a {
			arr[i] = s
		}
	default:
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		out = append(out, asString(el))
	}
	return out, true
}
