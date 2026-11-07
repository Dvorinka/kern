package api

import (
	"encoding/json"
	"testing"

	"github.com/dvorinka/kern/internal/store"
)

func stepWithAnnotations(t *testing.T, raw string) store.Step {
	t.Helper()
	return store.Step{Number: 1, Name: "build", Annotations: json.RawMessage(raw)}
}

func TestCheckAnnotationsMapsFields(t *testing.T) {
	jobs := []store.Job{{
		JobKey: "build",
		Steps: []store.Step{stepWithAnnotations(t, `[
			{"level":"error","message":"boom","title":"T","file":"src/a.go","line":10,"col":3,"end_line":12,"end_col":7},
			{"level":"warning","message":"careful","file":"src/a.go","line":5},
			{"level":"notice","message":"fyi","file":"src/a.go","line":5,"end_line":5,"end_col":9},
			{"level":"debug","message":"verbose","file":"src/a.go","line":1},
			{"level":"error","message":"no file"},
			{"level":"warning","message":"no line","file":"b.go"}
		]`)},
	}}
	anns, skipped := checkAnnotations(jobs)
	if len(anns) != 4 {
		t.Fatalf("got %d annotations, want 4: %+v", len(anns), anns)
	}
	if skipped != 2 {
		t.Fatalf("skipped %d, want 2 (debug + file-less)", skipped)
	}
	a := anns[0]
	if a.Path != "src/a.go" || a.StartLine != 10 || a.EndLine != 12 ||
		a.StartColumn != 0 || a.EndColumn != 0 || // multi-line span drops columns
		a.AnnotationLevel != "failure" || a.Message != "boom" || a.Title != "T" {
		t.Fatalf("first annotation mis-mapped: %+v", a)
	}
	if anns[1].StartLine != 5 || anns[1].EndLine != 5 || anns[1].StartColumn != 0 {
		t.Fatalf("second annotation: %+v", anns[1])
	}
	// Columns without a start column are dropped — GitHub requires the pair.
	if anns[2].StartColumn != 0 || anns[2].EndColumn != 0 {
		t.Fatalf("end_column without start_column must be dropped: %+v", anns[2])
	}
	// File with no line defaults to line 1.
	if anns[3].StartLine != 1 || anns[3].EndLine != 1 {
		t.Fatalf("missing line should default to 1: %+v", anns[3])
	}
}

func TestCheckAnnotationsEmpty(t *testing.T) {
	anns, skipped := checkAnnotations([]store.Job{{JobKey: "x", Steps: []store.Step{{Number: 1}}}})
	if len(anns) != 0 || skipped != 0 {
		t.Fatalf("empty run: anns=%v skipped=%d", anns, skipped)
	}
	// Malformed JSON must not panic or count as postable.
	jobs := []store.Job{{JobKey: "x", Steps: []store.Step{
		{Number: 1, Annotations: json.RawMessage(`{not json`)},
		{Number: 2, Annotations: json.RawMessage(`["oops"]`)},
	}}}
	anns, skipped = checkAnnotations(jobs)
	if len(anns) != 0 || skipped != 0 {
		t.Fatalf("malformed: anns=%v skipped=%d", anns, skipped)
	}
}
