package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/term"
)

// cmdSSH opens a live-exec session into a running job's container (#133):
// `kern ssh <job-id|run-id> [--job KEY] [-c CMD]`. With no -c the session is
// interactive — a line-mode shell (no PTY; full-screen programs won't
// render). The remote command runs verbatim, nothing is rewritten locally.
func (c *cli) cmdSSH(ctx context.Context, args []string) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args, "job", "c")
	if len(pos) == 0 {
		return fmt.Errorf("usage: kern ssh <job-id|run-id> [--job KEY] [-c CMD]")
	}
	target, jobKey := pos[0], opts["job"]
	cmd := opts["c"]
	if cmd == "" && len(pos) > 1 {
		cmd = strings.Join(pos[1:], " ")
	}
	id, err := uuid.Parse(target)
	if err != nil {
		return fmt.Errorf("job or run id must be a uuid — got %q", target)
	}
	var execID string
	if jobKey == "" {
		// Try the id as a job first; a 404 means it names a run instead.
		execID, err = c.openExec(ctx, id.String(), cmd)
		if err != nil {
			if ae, ok := err.(*apiError); !ok || ae.Status != http.StatusNotFound {
				return err
			}
		}
	}
	if execID == "" {
		jobID, err := c.liveJobForRun(ctx, id.String(), jobKey)
		if err != nil {
			return err
		}
		execID, err = c.openExec(ctx, jobID, cmd)
		if err != nil {
			return err
		}
	}
	return c.streamExec(ctx, execID, cmd == "")
}

// openExec requests a session on a job and returns its exec ID.
func (c *cli) openExec(ctx context.Context, jobID, cmd string) (string, error) {
	var out struct {
		Exec struct {
			ID string `json:"id"`
		} `json:"exec"`
	}
	body := map[string]any{"command": cmd, "interactive": cmd == ""}
	if err := c.api().post(ctx, "/jobs/"+jobID+"/exec", body, &out); err != nil {
		return "", err
	}
	return out.Exec.ID, nil
}

// liveJobForRun picks the single in_progress job on a run, or the one --job
// names when several are live.
func (c *cli) liveJobForRun(ctx context.Context, runID, jobKey string) (string, error) {
	var run struct {
		Jobs []struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	if err := c.api().get(ctx, "/runs/"+runID, nil, &run); err != nil {
		return "", fmt.Errorf("resolve %s: %w", runID, err)
	}
	var live []string
	for _, j := range run.Jobs {
		if j.Status != "in_progress" {
			continue
		}
		if jobKey != "" && j.Key != jobKey && j.Name != jobKey {
			continue
		}
		live = append(live, j.ID)
	}
	switch len(live) {
	case 0:
		return "", fmt.Errorf("no in_progress job on run %s matching --job %q", runID, jobKey)
	case 1:
		return live[0], nil
	default:
		return "", fmt.Errorf("run %s has %d in_progress jobs — pass --job <key>", runID, len(live))
	}
}

// streamExec pumps session output to stdout and local stdin to the remote
// pipe until the session reaches a terminal state. Exit code follows the
// remote command.
func (c *cli) streamExec(ctx context.Context, execID string, interactive bool) error {
	stdinDone := make(chan struct{})
	if interactive || !term.IsTerminal(int(os.Stdin.Fd())) {
		go c.pumpSSHStdin(ctx, execID, stdinDone)
	} else {
		close(stdinDone)
	}
	var off int64
	for {
		var out struct {
			Exec struct {
				State    string `json:"state"`
				ExitCode *int   `json:"exit_code"`
				Error    string `json:"error"`
			} `json:"exec"`
			Data string `json:"data"`
			Size int64  `json:"size"`
		}
		q := url.Values{"after": {fmt.Sprint(off)}}
		if err := c.api().get(ctx, "/exec/"+execID, q, &out); err != nil {
			return err
		}
		if out.Data != "" {
			chunk, _ := base64.StdEncoding.DecodeString(out.Data)
			os.Stdout.Write(chunk)
		}
		off = out.Size
		switch out.Exec.State {
		case "done", "failed", "cancelled":
			// The stdin pump is left for process exit — a TTY reader would
			// otherwise block the exit path forever.
			_ = stdinDone
			if out.Exec.Error != "" {
				fmt.Fprintln(os.Stderr, out.Exec.Error)
			}
			if out.Exec.ExitCode != nil && *out.Exec.ExitCode != 0 {
				os.Exit(*out.Exec.ExitCode)
			}
			if out.Exec.State == "failed" {
				os.Exit(1)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// pumpSSHStdin forwards local stdin to the session blob. Raw mode when the
// terminal is interactive so keys arrive without line buffering.
func (c *cli) pumpSSHStdin(ctx context.Context, execID string, done chan<- struct{}) {
	defer close(done)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, old)
		}
	}
	rd := bufio.NewReaderSize(os.Stdin, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			if perr := c.postSSHStdin(ctx, execID, buf[:n]); perr != nil {
				return
			}
		}
		if err != nil {
			_ = c.postSSHStdinEOF(ctx, execID)
			return
		}
	}
}

// postSSHStdin ships one chunk; the API wants the raw bytes, not JSON.
func (c *cli) postSSHStdin(ctx context.Context, execID string, data []byte) error {
	cl := c.api()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cl.base+"/exec/"+execID+"/stdin", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.token)
	resp, err := cl.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("stdin post: %s", resp.Status)
	}
	return nil
}

func (c *cli) postSSHStdinEOF(ctx context.Context, execID string) error {
	cl := c.api()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cl.base+"/exec/"+execID+"/stdin?eof=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.token)
	resp, err := cl.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
