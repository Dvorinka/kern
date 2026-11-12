package api

// check_run and check_suite rerequest deliveries: GitHub's "Re-run" button
// (and the /check-runs/{id}/rerequest API) arrive here instead of at a
// REST endpoint. A check run's external_id is the KERN run id, or
// "<run id>/<job key>" for per-job checks, so a rerequest maps onto
// RerunJobs — a single-job rerequest resets that job plus its dependents,
// a run- or suite-level one resets everything that did not succeed (#323).

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// checkEventPayload covers both check_run and check_suite deliveries; only
// the fields a rerequest needs are modelled.
type checkEventPayload struct {
	Action   string `json:"action"`
	CheckRun struct {
		ExternalID string `json:"external_id"`
		Name       string `json:"name"`
	} `json:"check_run"`
	CheckSuite struct {
		HeadSHA string `json:"head_sha"`
	} `json:"check_suite"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// actOnCheckRun handles check_run deliveries; only "rerequested" acts.
func (s *Server) actOnCheckRun(c *gin.Context, body []byte) (status, detail string) {
	var p checkEventPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "failed", "payload is not a check_run event: " + err.Error()
	}
	if p.Action != "rerequested" {
		return "ignored", "check_run action " + p.Action + " is not a rerequest"
	}
	runPart, jobKey, _ := strings.Cut(p.CheckRun.ExternalID, "/")
	runID, err := uuid.Parse(runPart)
	if err != nil {
		return "ignored", "check run external_id is not a kern run: " + p.CheckRun.ExternalID
	}
	run, err := s.Store.Run(c.Request.Context(), runID)
	if err != nil {
		return "ignored", "check run references unknown run " + runPart
	}
	var keys []string
	if jobKey != "" {
		keys = []string{jobKey}
	}
	n, err := s.Store.RerunJobs(c.Request.Context(), runID, keys)
	if err != nil {
		return "ignored", fmt.Sprintf("rerequest %s #%d: %s", run.WorkflowName, run.RunNumber, err)
	}
	return "processed", fmt.Sprintf("rerequested %s #%d (%d jobs reset)", run.WorkflowName, run.RunNumber, n)
}

// actOnCheckSuite handles check_suite deliveries; only "rerequested" acts.
// The payload carries no check runs, so the suite resolves to the newest
// run per workflow at its head sha.
func (s *Server) actOnCheckSuite(c *gin.Context, body []byte) (status, detail string) {
	var p checkEventPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "failed", "payload is not a check_suite event: " + err.Error()
	}
	if p.Action != "rerequested" {
		return "ignored", "check_suite action " + p.Action + " is not a rerequest"
	}
	runs, err := s.Store.RunsForSHA(c.Request.Context(), p.Repository.FullName, p.CheckSuite.HeadSHA)
	if err != nil {
		return "failed", err.Error()
	}
	if len(runs) == 0 {
		return "ignored", "no runs for " + p.Repository.FullName + "@" + p.CheckSuite.HeadSHA
	}
	var parts []string
	for _, run := range runs {
		n, err := s.Store.RerunJobs(c.Request.Context(), run.ID, nil)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s #%d (%s)", run.WorkflowName, run.RunNumber, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s #%d (%d jobs reset)", run.WorkflowName, run.RunNumber, n))
	}
	return "processed", "rerequested: " + strings.Join(parts, "; ")
}
