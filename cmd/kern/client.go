package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client speaks the same /api/v1 contract as the web app, with Bearer auth.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token string) *client {
	return &client{base: strings.TrimRight(base, "/") + "/api/v1",
		token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func (c *client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Code != "" {
			return &apiError{resp.StatusCode, e.Error.Code, e.Error.Message}
		}
		return &apiError{resp.StatusCode, "http_error", strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *client) post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

func (c *client) put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, nil, body, out)
}

func (c *client) patch(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, body, out)
}

func (c *client) del(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

// putFile streams r as an octet-stream body and decodes the JSON reply. It
// uses an unbounded client — bundle uploads outsize the 30s API timeout.
func (c *client) putFile(ctx context.Context, path string, r io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.base+path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Code != "" {
			return &apiError{resp.StatusCode, e.Error.Code, e.Error.Message}
		}
		return &apiError{resp.StatusCode, "http_error", strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// download streams an artifact to w.
func (c *client) download(ctx context.Context, path string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &apiError{resp.StatusCode, "http_error", strings.TrimSpace(string(data))}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
