package runner

// GITHUB_EVENT_PATH payload. GitHub writes the webhook body to a file and
// exports its path; actions like dorny/paths-filter and github-script read
// github.event.* from it. The payload shape lives in workflows.EventPayload,
// shared with the orchestrator's github.event expression context.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/dvorinka/kern/internal/workflows"
)

// containerEventPath is where the payload is mounted inside the job
// container; the host file lives in the per-job work root.
const containerEventPath = "/kern/event.json"

// eventPayloadMap builds the payload once per job; the same map feeds the
// file and the github.event step-expression context.
func (e *executor) eventPayloadMap() map[string]any {
	return workflows.EventPayload(
		e.claim.Run.Event, e.claim.Run.Ref, e.claim.Run.SHA, e.claim.Run.Actor,
		e.claim.Run.CommitMessage, e.claim.Run.RepoFullName, e.claim.Run.RepoURL,
		e.claim.Run.DefaultBranch, e.claim.Run.Source,
	)
}

func (e *executor) writeEventPayload() (string, error) {
	data, err := json.Marshal(e.eventPayloadMap())
	if err != nil {
		return "", err
	}
	path := filepath.Join(e.workRoot(), "event.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
