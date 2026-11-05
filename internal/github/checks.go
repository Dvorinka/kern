package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// CheckRun is one KERN workflow run as GitHub renders it on a commit.
// Name is the check name (kern/<workflow>). ExternalID is the KERN run id,
// so a rerun on the same SHA updates its own check instead of a sibling's.
type CheckRun struct {
	Name       string
	HeadSHA    string
	Status     string // queued, in_progress, completed
	Conclusion string // set only when Status is completed
	DetailsURL string
	ExternalID string
	Title      string
	Summary    string
}

// UpsertCheckRun creates the check, or updates it when one already exists
// for this ExternalID. Listing then writing can duplicate the check if two
// updates for the same run race; status posts for one run are sequential.
func (c *Client) UpsertCheckRun(ctx context.Context, repo string, in CheckRun) error {
	if in.Name == "" || in.HeadSHA == "" || in.ExternalID == "" {
		return fmt.Errorf("check run requires name, head sha and external id")
	}
	if in.Summary == "" {
		in.Summary = "KERN run."
	}
	id, err := c.findCheckRun(ctx, repo, in.HeadSHA, in.Name, in.ExternalID)
	if err != nil {
		return err
	}
	if id == 0 {
		return c.do(ctx, http.MethodPost, "/repos/"+repo+"/check-runs", "", in.body(true), nil)
	}
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/check-runs/%d", repo, id), "", in.body(false), nil)
}

func (c *Client) findCheckRun(ctx context.Context, repo, sha, name, externalID string) (int64, error) {
	q := url.Values{}
	q.Set("check_name", name)
	q.Set("per_page", "100")
	var out struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			ExternalID string `json:"external_id"`
		} `json:"check_runs"`
	}
	path := "/repos/" + repo + "/commits/" + sha + "/check-runs?" + q.Encode()
	if err := c.do(ctx, http.MethodGet, path, "", nil, &out); err != nil {
		return 0, err
	}
	for _, cr := range out.CheckRuns {
		if cr.ExternalID == externalID {
			return cr.ID, nil
		}
	}
	return 0, nil
}

func (in CheckRun) body(create bool) map[string]any {
	out := map[string]any{
		"status":      in.Status,
		"details_url": in.DetailsURL,
		"output":      map[string]string{"title": in.Title, "summary": in.Summary},
	}
	if create {
		out["name"] = in.Name
		out["head_sha"] = in.HeadSHA
		out["external_id"] = in.ExternalID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	switch in.Status {
	case "in_progress":
		out["started_at"] = now
	case "completed":
		out["conclusion"] = in.Conclusion
		out["completed_at"] = now
	}
	return out
}
