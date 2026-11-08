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
		// Filter results (x.*) are list values too; anything else falls
		// through to plain interpolation.
		elems, ok := expressions.ListElements(v)
		if !ok {
			return nil, false
		}
		arr = elems
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		out = append(out, asString(el))
	}
	return out, true
}

// ResolveRunsOn expands ${{ }} labels in a runs-on list against ctx. A
// whole-value expression evaluating to a list expands into labels (GitHub
// treats `runs-on: ${{ matrix.ro }}` with ro: [a, b] as labels a+b;
// stringifying it would produce the literal "Array" and match nothing).
// Anything still unresolvable stays literal and must exist as a runner
// label. Called at compile time with {matrix} and again at run seed time
// with {matrix, inputs} so dispatch inputs like ${{ inputs.runner }} resolve.
func ResolveRunsOn(runsOn []string, ctx map[string]any) []string {
	out := make([]string, 0, len(runsOn))
	for _, label := range runsOn {
		if strings.Contains(label, "${{") {
			if elems, ok := evalWholeExpr(label, ctx); ok {
				for _, el := range elems {
					if el = strings.TrimSpace(el); el != "" {
						out = append(out, el)
					}
				}
				continue
			}
			if resolved, err := expressions.Interpolate(label, ctx); err == nil && strings.TrimSpace(resolved) != "" {
				label = resolved
			}
		}
		out = append(out, label)
	}
	return out
}
