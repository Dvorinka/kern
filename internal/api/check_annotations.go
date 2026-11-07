package api

// Mapping from stored step annotations (steps.annotations, written by the
// runner's ::error::/::warning::/::notice::/::debug:: parser) onto GitHub
// Checks API annotations. Annotations append across check-run updates and
// GitHub accepts at most 50 per request, so the caller chunks them.
//
// Two classes of stored annotation cannot be posted: debug level has no
// Checks API equivalent, and GitHub requires a path. Both remain visible in
// KERN's step detail; the skipped count is returned for logging.

import (
	"encoding/json"

	"github.com/dvorinka/kern/internal/github"
	"github.com/dvorinka/kern/internal/store"
)

// storedAnnotation mirrors the runner-side annotation JSON. Field names are
// the runner package's json tags, duplicated here to keep api free of a
// runner import cycle risk.
type storedAnnotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Title   string `json:"title,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	EndCol  int    `json:"end_col,omitempty"`
}

// maxCheckRunAnnotations bounds the total annotations one completed status
// post will push — 20 PATCHes worst case. A pathological run keeps its
// annotations in KERN; the overflow is logged, not silently dropped.
const maxCheckRunAnnotations = 1000

// checkAnnotations flattens every finished step's stored annotations into
// GitHub's shape, in job order then step order. It returns the postable list
// plus the count of annotations that cannot be represented (no file, debug
// level, or beyond the cap).
func checkAnnotations(jobs []store.Job) ([]github.CheckAnnotation, int) {
	var out []github.CheckAnnotation
	skipped := 0
	for _, job := range jobs {
		for _, step := range job.Steps {
			if len(step.Annotations) == 0 {
				continue
			}
			var items []storedAnnotation
			if err := json.Unmarshal(step.Annotations, &items); err != nil {
				continue
			}
			for _, a := range items {
				level := ""
				switch a.Level {
				case "error":
					level = "failure"
				case "warning":
					level = "warning"
				case "notice":
					level = "notice"
				default:
					skipped++ // debug has no Checks API annotation level
					continue
				}
				if a.File == "" {
					skipped++
					continue
				}
				if len(out) >= maxCheckRunAnnotations {
					skipped++
					continue
				}
				start := a.Line
				if start < 1 {
					start = 1
				}
				end := a.EndLine
				if end < start {
					end = start
				}
				ga := github.CheckAnnotation{
					Path:            a.File,
					StartLine:       start,
					EndLine:         end,
					AnnotationLevel: level,
					Message:         a.Message,
					Title:           a.Title,
					RawDetails:      "job " + job.JobKey + " / step " + step.Name,
				}
				// GitHub only honours columns on a single-line span, and
				// end_column requires start_column — drop both otherwise.
				if end == start && a.Col > 0 {
					ga.StartColumn = a.Col
					if a.EndCol > a.Col {
						ga.EndColumn = a.EndCol
					}
				}
				out = append(out, ga)
			}
		}
	}
	return out, skipped
}
