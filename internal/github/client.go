// Package github is a thin api.github.com client: commit statuses, check
// runs, and workflow file discovery. The GitHub App flow (installation
// tokens, private keys) is a separate path.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	if base == "" {
		base = "https://api.github.com"
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token,
		http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path, accept string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// PostCommitStatus creates a commit status (the coloured dot on a commit).
// state is one of error, failure, pending, success.
func (c *Client) PostCommitStatus(ctx context.Context, repo, sha, state, contextName, targetURL, description string) error {
	if len(description) > 140 {
		description = description[:140]
	}
	return c.do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, "", map[string]any{
		"state": state, "context": contextName,
		"target_url": targetURL, "description": description,
	}, nil)
}

// HeadSHA resolves a branch (or ref) to its head commit sha.
func (c *Client) HeadSHA(ctx context.Context, repo, ref string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+ref, "", nil, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// WorkflowFile is a workflow discovered under .github/workflows.
type WorkflowFile struct {
	Path string
	Name string
}

// ListWorkflowFiles returns workflow file paths under .github/workflows at ref.
func (c *Client) ListWorkflowFiles(ctx context.Context, repo, ref string) ([]WorkflowFile, error) {
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/trees/"+ref+"?recursive=1", "", nil, &out); err != nil {
		return nil, err
	}
	var files []WorkflowFile
	for _, e := range out.Tree {
		if e.Type != "blob" || !strings.HasPrefix(e.Path, ".github/workflows/") {
			continue
		}
		name := strings.TrimPrefix(e.Path, ".github/workflows/")
		if strings.Contains(name, "/") ||
			!(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		files = append(files, WorkflowFile{Path: e.Path, Name: name})
	}
	return files, nil
}

// ManifestConversion is the credential bundle GitHub returns when a
// manifest-flow code is converted into a registered app.
type ManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	HTMLURL       string `json:"html_url"`
}

// ConvertManifestCode exchanges the code GitHub appends to the manifest
// redirect URL for the new app's credentials. Unauthenticated by design.
func (c *Client) ConvertManifestCode(ctx context.Context, code string) (ManifestConversion, error) {
	var out ManifestConversion
	err := c.do(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, &out)
	return out, err
}

// FileContent downloads a file's raw content at ref.
func (c *Client) FileContent(ctx context.Context, repo, path, ref string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/repos/"+repo+"/contents/"+path+"?ref="+ref, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github contents %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}
