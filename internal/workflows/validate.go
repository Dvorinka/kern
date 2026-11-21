package workflows

import (
	"strconv"
	"strings"

	"github.com/dvorinka/kern/internal/expressions"
)

// validateExpressionsIn checks the syntax of every ${{ }} expression in the workflow.
// Context values are unknown at compile time, so only parsing is performed.
func validateExpressionsIn(d *diagnostics, wf *Workflow) {
	for id, job := range wf.Jobs {
		if job == nil {
			continue
		}
		base := "jobs." + id
		checkString(d, base+".name", job.Name)
		checkString(d, base+".if", job.If)
		checkString(d, base+".concurrency.group", job.Concurrency.Group)
		checkString(d, base+".concurrency.cancel-in-progress", job.Concurrency.CancelExpr)
		checkString(d, base+".continue-on-error", job.ContinueOnErrorExpr)
		checkString(d, base+".timeout-minutes", job.TimeoutExpr)
		for _, r := range job.RunsOn {
			checkString(d, base+".runs-on", r)
		}
		checkMap(d, base+".env", job.Env)
		checkMap(d, base+".with", job.With)
		for k, v := range job.Outputs {
			checkString(d, base+".outputs."+k, v)
		}
		if job.Strategy != nil {
			checkString(d, base+".strategy.fail-fast", job.Strategy.FailFastExpr)
		}
		for i, s := range job.Steps {
			stepBase := base + ".steps[" + strconv.Itoa(i) + "]"
			checkString(d, stepBase+".if", s.If)
			checkString(d, stepBase+".run", s.Run)
			checkString(d, stepBase+".name", s.Name)
			checkString(d, stepBase+".shell", s.Shell)
			checkString(d, stepBase+".working-directory", s.WorkingDirectory)
			checkString(d, stepBase+".continue-on-error", s.ContinueOnErrorExpr)
			checkString(d, stepBase+".timeout-minutes", s.TimeoutExpr)
			checkMap(d, stepBase+".env", s.Env)
			checkMap(d, stepBase+".with", s.With)
		}
	}
	checkMap(d, "env", wf.Env)
	checkString(d, "run-name", wf.RunName)
	checkString(d, "concurrency.group", wf.Concurrency.Group)
	checkString(d, "concurrency.cancel-in-progress", wf.Concurrency.CancelExpr)
}

func checkMap(d *diagnostics, path string, m map[string]any) {
	for k, v := range m {
		if s, ok := v.(string); ok {
			checkString(d, path+"."+k, s)
		}
	}
}

func checkString(d *diagnostics, path, s string) {
	if s == "" || !strings.Contains(s, "${{") {
		return
	}
	bodies, unterminated := expressionBodies(s)
	if unterminated {
		d.errorf("expression", path, "unterminated ${{ expression")
		return
	}
	for _, body := range bodies {
		if err := expressions.Validate(body); err != nil {
			d.errorf("expression", path, err.Error())
		}
	}
}

// expressionBodies extracts the contents of every ${{ ... }} segment and reports
// whether the last segment was left open.
func expressionBodies(s string) ([]string, bool) {
	var out []string
	rest := s
	for {
		start := strings.Index(rest, "${{")
		if start < 0 {
			return out, false
		}
		rest = rest[start+3:]
		end := findExpressionEnd(rest)
		if end < 0 {
			return out, true
		}
		out = append(out, rest[:end])
		rest = rest[end+2:]
	}
}

func findExpressionEnd(s string) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s)-1; i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '}':
			if !inSingle && !inDouble && s[i+1] == '}' {
				return i
			}
		}
	}
	return -1
}
