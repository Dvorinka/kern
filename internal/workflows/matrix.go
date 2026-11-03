package workflows

import (
	"fmt"
	"sort"
)

// expandMatrix expands strategy.matrix into concrete combinations. It supports
// include/exclude with GitHub's merge semantics and returns [nil] for non-matrix jobs.
func expandMatrix(job *Job) ([]map[string]any, error) {
	if job.Strategy == nil || len(job.Strategy.Matrix) == 0 {
		return []map[string]any{nil}, nil
	}

	dims := map[string][]any{}
	var includes, excludes []map[string]any
	for key, values := range job.Strategy.Matrix {
		switch key {
		case "include":
			for _, raw := range values {
				m, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("matrix.include must be a list of mappings")
				}
				includes = append(includes, m)
			}
		case "exclude":
			for _, raw := range values {
				m, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("matrix.exclude must be a list of mappings")
				}
				excludes = append(excludes, m)
			}
		default:
			// Sort scalar axis values for deterministic job ordering.
			vals := append([]any{}, values...)
			sort.SliceStable(vals, func(i, j int) bool { return asString(vals[i]) < asString(vals[j]) })
			dims[key] = vals
		}
	}

	dimKeys := sortedKeys(dims)
	combos := []map[string]any{{}}
	for _, key := range dimKeys {
		next := make([]map[string]any, 0, len(combos)*len(dims[key]))
		for _, combo := range combos {
			for _, val := range dims[key] {
				clone := make(map[string]any, len(combo)+1)
				for k, v := range combo {
					clone[k] = v
				}
				clone[key] = val
				next = append(next, clone)
			}
		}
		combos = next
	}

	// Apply excludes: a combination is dropped when every key in the exclude matches.
	if len(excludes) > 0 {
		filtered := combos[:0]
		for _, combo := range combos {
			excluded := false
			for _, ex := range excludes {
				if matchesSubset(combo, ex) {
					excluded = true
					break
				}
			}
			if !excluded {
				filtered = append(filtered, combo)
			}
		}
		combos = filtered
	}

	// Apply includes like GitHub: an entry merges into every combination whose
	// dimension values match, adding any extra keys; if nothing matches it becomes
	// an additional combination.
	dimSet := map[string]bool{}
	for _, k := range dimKeys {
		dimSet[k] = true
	}
	for _, inc := range includes {
		matched := false
		for _, combo := range combos {
			if matchesDimensions(combo, inc, dimSet) {
				for k, v := range inc {
					combo[k] = v
				}
				matched = true
			}
		}
		if !matched {
			extra := make(map[string]any, len(inc))
			for k, v := range inc {
				extra[k] = v
			}
			combos = append(combos, extra)
		}
	}

	if len(combos) == 0 {
		return nil, fmt.Errorf("matrix expands to zero jobs")
	}
	return combos, nil
}

// matchesSubset reports whether every key/value in subset exists in combo.
func matchesSubset(combo, subset map[string]any) bool {
	if len(subset) == 0 {
		return false
	}
	for k, v := range subset {
		cv, ok := combo[k]
		if !ok || asString(cv) != asString(v) {
			return false
		}
	}
	return true
}

// matchesDimensions reports whether the include entry agrees with combo on every
// key that is a real matrix dimension.
func matchesDimensions(combo, inc map[string]any, dims map[string]bool) bool {
	for k, v := range inc {
		if !dims[k] {
			continue
		}
		cv, ok := combo[k]
		if !ok || asString(cv) != asString(v) {
			return false
		}
	}
	return true
}
