package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// fh2bytes adapts a *os.File body for do(), which only understands []byte and
// JSON-marshallable values. It reads eagerly so do() can set Content-Length-free
// octet-stream bodies; artifact sizes are bounded by the server-side limit.
func fh2bytes(f *os.File) []byte {
	data, _ := io.ReadAll(f)
	return data
}

// ClaimService is one service container for a job.
type ClaimService struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Options []string          `json:"options,omitempty"`
	// Ports marks the service as localhost-mode: it shares the job
	// container's network namespace so localhost:<port> resolves.
	Ports []string `json:"ports,omitempty"`
}

// Claim mirrors the control plane's claim payload. It is the runner's only view of a job.
type Claim struct {
	Job struct {
		ID              string            `json:"id"`
		RunID           string            `json:"run_id"`
		Key             string            `json:"key"`
		Name            string            `json:"name"`
		Attempt         int               `json:"attempt"`
		Image           string            `json:"image"`
		Env             map[string]string `json:"env"`
		Matrix          map[string]any    `json:"matrix"`
		RunsOn          []string          `json:"runs_on"`
		TimeoutSeconds  int               `json:"timeout_seconds"`
		ContinueOnError bool              `json:"continue_on_error"`
		OutputsSpec     map[string]string `json:"outputs_spec"`
		Inputs          map[string]string `json:"inputs"`
		Services        []ClaimService    `json:"services"`
		Needs           map[string]any    `json:"needs"`
	} `json:"job"`
	Run          ClaimRun            `json:"run"`
	Steps        []StepSpec          `json:"steps"`
	Secrets      []string            `json:"secrets"`
	LeaseEnds    time.Time           `json:"lease_expires_at"`
	Remediations map[string][]string `json:"remediations"`
	SelfHeal     string              `json:"self_heal"`
}

// ClaimRun carries run identity and checkout source. CloneToken is minted by
// the control plane per claim and never persisted.
type ClaimRun struct {
	ID            string `json:"id"`
	Number        int    `json:"number"`
	Event         string `json:"event"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	Actor         string `json:"actor"`
	Workflow      string `json:"workflow"`
	Project       string `json:"project"`
	ProjectSlug   string `json:"project_slug"`
	TrustLevel    string `json:"trust_level"`
	RepoURL       string `json:"repo_url"`
	RepoFullName  string `json:"repo_full_name"`
	CloneToken    string `json:"clone_token,omitempty"`
	DefaultBranch string `json:"default_branch"`
	LocalPath     string `json:"local_path"`
	BundleRef     string `json:"bundle_ref"`
	HeadRef       string `json:"head_ref"`
	HeadSHA       string `json:"head_sha"`
}

type StepSpec struct {
	Number           int               `json:"number"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Run              string            `json:"run"`
	Uses             string            `json:"uses"`
	Shell            string            `json:"shell"`
	WorkingDirectory string            `json:"working_directory"`
	If               string            `json:"if"`
	Env              map[string]string `json:"env"`
	With             map[string]any    `json:"with"`
	ContinueOnError  bool              `json:"continue_on_error"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
}

func (s StepSpec) contextID() string {
	if s.ID != "" {
		return s.ID
	}
	return "step" + strconv.Itoa(s.Number)
}

type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token string) *client {
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		if raw, ok := body.([]byte); ok {
			reader = bytes.NewReader(raw)
		} else {
			data, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		if _, ok := body.([]byte); ok {
			req.Header.Set("Content-Type", "application/octet-stream")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("control plane returned %d: %s", e.Status, e.Body)
}

func (c *client) register(ctx context.Context, req map[string]any) (runnerID, token string, err error) {
	var out struct {
		RunnerID string `json:"runner_id"`
		Token    string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/runners/register", req, &out); err != nil {
		return "", "", err
	}
	return out.RunnerID, out.Token, nil
}

type heartbeatResponse struct {
	Update *struct {
		Version string `json:"version"`
		Image   string `json:"image"`
	} `json:"update"`
}

func (c *client) heartbeat(ctx context.Context, m heartbeatMetrics) (heartbeatResponse, error) {
	var out heartbeatResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/runners/heartbeat", m, &out)
	return out, err
}

func (c *client) claim(ctx context.Context, wait int) (*Claim, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v1/runners/claim?wait="+strconv.Itoa(wait), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var claim Claim
	if err := json.Unmarshal(data, &claim); err != nil {
		return nil, err
	}
	return &claim, nil
}

func (c *client) renew(ctx context.Context, jobID string) (cancelled bool, err error) {
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	path := "/api/v1/runners/jobs/" + url.PathEscape(jobID) + "/heartbeat"
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &out); err != nil {
		return false, err
	}
	return out.Cancelled, nil
}

func (c *client) startStep(ctx context.Context, jobID string, n int) error {
	path := fmt.Sprintf("/api/v1/runners/jobs/%s/steps/%d/start", url.PathEscape(jobID), n)
	return c.do(ctx, http.MethodPost, path, map[string]any{}, nil)
}

func (c *client) appendLog(ctx context.Context, jobID string, n int, data []byte) error {
	path := fmt.Sprintf("/api/v1/runners/jobs/%s/steps/%d/logs", url.PathEscape(jobID), n)
	return c.do(ctx, http.MethodPost, path, data, nil)
}

func (c *client) finishStep(ctx context.Context, jobID string, n int, conclusion string, exitCode int, d *diagnosis) error {
	body := map[string]any{"conclusion": conclusion, "exit_code": exitCode}
	if d != nil {
		body["diagnosis"] = d
	}
	path := fmt.Sprintf("/api/v1/runners/jobs/%s/steps/%d/finish", url.PathEscape(jobID), n)
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// uploadSummary ships the step's GITHUB_STEP_SUMMARY markdown to the control
// plane, which stores it as a blob and marks the step.
func (c *client) uploadSummary(ctx context.Context, jobID string, n int, data []byte) error {
	path := fmt.Sprintf("/api/v1/runners/jobs/%s/steps/%d/summary", url.PathEscape(jobID), n)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return nil
}

func (c *client) finishJob(ctx context.Context, jobID, conclusion string, outputs map[string]string, errMsg string) error {
	path := "/api/v1/runners/jobs/" + url.PathEscape(jobID) + "/finish"
	return c.do(ctx, http.MethodPost, path, map[string]any{
		"conclusion": conclusion, "outputs": outputs, "error": errMsg,
	}, nil)
}

func (c *client) cancelCheck(ctx context.Context, jobID string) (bool, error) {
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	path := "/api/v1/runners/jobs/" + url.PathEscape(jobID) + "/cancel-check"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return false, err
	}
	return out.Cancelled, nil
}

func (c *client) uploadArtifact(ctx context.Context, jobID, name, path string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return c.do(ctx, http.MethodPut,
		"/api/v1/runners/jobs/"+url.PathEscape(jobID)+"/artifacts/"+url.PathEscape(name), fh2bytes(fh), nil)
}

func (c *client) downloadArtifact(ctx context.Context, jobID, name, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/runners/jobs/"+url.PathEscape(jobID)+"/artifacts/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = io.Copy(fh, resp.Body)
	return err
}

// downloadSource fetches the run's uploaded workspace bundle (kern exec) to path.
func (c *client) downloadSource(ctx context.Context, jobID, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/runners/jobs/"+url.PathEscape(jobID)+"/source", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = io.Copy(fh, resp.Body)
	return err
}

func (c *client) listArtifacts(ctx context.Context, jobID string) ([]string, error) {
	var out struct {
		Artifacts []string `json:"artifacts"`
	}
	path := "/api/v1/runners/jobs/" + url.PathEscape(jobID) + "/artifacts"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Artifacts, nil
}

// restoreCache downloads the best-matching archive to path and reports the
// matched key. found is false on a clean 404 cache miss.
func (c *client) restoreCache(ctx context.Context, jobID, key string, prefixes []string, path string) (string, bool, error) {
	q := url.Values{"key": {key}}
	if len(prefixes) > 0 {
		q.Set("restore_keys", strings.Join(prefixes, ","))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/runners/jobs/"+url.PathEscape(jobID)+"/cache/restore?"+q.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", false, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	fh, err := os.Create(path)
	if err != nil {
		return "", false, err
	}
	defer fh.Close()
	if _, err := io.Copy(fh, resp.Body); err != nil {
		return "", false, err
	}
	return resp.Header.Get("X-Kern-Cache-Key"), true, nil
}

// saveCache uploads an archive under key. A 409 means the key is taken;
// callers map that to a warning.
func (c *client) saveCache(ctx context.Context, jobID, key, path string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return c.do(ctx, http.MethodPut,
		"/api/v1/runners/jobs/"+url.PathEscape(jobID)+"/cache/"+url.PathEscape(key), fh2bytes(fh), nil)
}

func (c *client) jobSecrets(ctx context.Context, jobID string) (map[string]string, bool, error) {
	var out struct {
		Secrets map[string]string `json:"secrets"`
		Blocked bool              `json:"blocked"`
	}
	path := "/api/v1/runners/jobs/" + url.PathEscape(jobID) + "/secrets"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, false, err
	}
	return out.Secrets, out.Blocked, nil
}
