package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cmdExec ships the working tree to the fleet, runs a workflow on it and
// streams the result back so it looks local: bundle → upload → trigger →
// live logs → optional artifact pull. Exit code follows the run conclusion.
func (c *cli) cmdExec(ctx context.Context, args []string) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	opts, pos := parseFlags(args, "workflow", "workflow-file", "dir", "pull", "event", "ref", "sha")
	wf := opts["workflow"]
	if wf == "" && len(pos) > 0 {
		wf = pos[0]
	}
	slug := c.project
	if slug == "" {
		return fmt.Errorf("project required — --project or `kern config set project <slug>`")
	}
	var wfYAML, wfPath string
	if file := opts["workflow-file"]; file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("--workflow-file: %w", err)
		}
		wfYAML = string(b)
		abs, _ := filepath.Abs(file)
		base, _ := filepath.Abs(first(opts["dir"], "."))
		wfPath = filepath.Base(file)
		if rel, rerr := filepath.Rel(base, abs); rerr == nil && !strings.HasPrefix(rel, "..") {
			wfPath = filepath.ToSlash(rel)
		}
	}
	if wf == "" && wfYAML == "" {
		return fmt.Errorf("usage: kern exec <workflow> [--workflow-file F] [--dir DIR] [--pull DIR] [--ref R] [--sha S]")
	}
	dir, err := filepath.Abs(first(opts["dir"], "."))
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("--dir %s is not a readable directory", dir)
	}

	w := os.Stdout
	if c.json {
		w = os.Stderr // keep stdout for the final JSON result
	}

	bundle, files, digest, err := bundleDir(dir)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	defer os.Remove(bundle)
	binfo, _ := os.Stat(bundle)
	fmt.Fprintf(w, "bundled %d files from %s (%s)\n", files, dir, humanBytes(binfo.Size()))

	// Content-addressed dedupe: an identical tree already uploaded is reused
	// without re-sending the archive.
	var up struct {
		BundleRef string `json:"bundle_ref"`
		SizeBytes int64  `json:"size_bytes"`
		Deduped   bool   `json:"deduped"`
	}
	resolveErr := c.api().get(ctx, "/projects/"+slug+"/source/resolve",
		url.Values{"sha256": {digest}}, &up)
	switch {
	case resolveErr == nil:
		fmt.Fprintf(w, "reusing %s (content already uploaded)\n", up.BundleRef)
	case isStatus(resolveErr, 404):
		fh, err := os.Open(bundle)
		if err != nil {
			return err
		}
		err = c.api().putFile(ctx, "/projects/"+slug+"/source?sha256="+digest, fh, &up)
		fh.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "uploaded %s\n", up.BundleRef)
	default:
		return resolveErr
	}

	var trig struct {
		Run run `json:"run"`
	}
	body := map[string]any{
		"event": first(opts["event"], "workflow_dispatch"),
		"ref":   opts["ref"], "sha": opts["sha"],
		"bundle_ref": up.BundleRef,
	}
	if wfYAML != "" {
		// Inline workflow: the file need not exist in the repo or be a
		// synced workflow — unmerged and ad-hoc workflow testing.
		body["workflow_path"] = wfPath
		body["workflow_yaml"] = wfYAML
		err = c.api().post(ctx, "/projects/"+slug+"/runs", body, &trig)
	} else {
		workflowID, werr := c.resolveWorkflow(ctx, slug, wf)
		if werr != nil {
			return werr
		}
		err = c.api().post(ctx, "/projects/"+slug+"/workflows/"+workflowID+"/runs", body, &trig)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "run %s (#%d) started\n", trig.Run.ID, trig.Run.RunNumber)

	final, err := c.streamRun(ctx, w, trig.Run.ID)
	if err != nil {
		return err
	}
	if c.json {
		printJSON(final)
	} else {
		fmt.Fprintf(w, "run %s: %s\n", shortID(final.ID), final.Conclusion)
	}

	if pull := opts["pull"]; pull != "" {
		if err := c.pullArtifacts(ctx, w, final.ID, pull); err != nil {
			return err
		}
	}
	if final.Conclusion != "success" {
		return fmt.Errorf("run concluded %s", first(final.Conclusion, final.Status))
	}
	return nil
}

// streamRun tails every step log in a run until the run reaches a terminal
// status. Polling multiplexes across parallel jobs without an SSE client.
func (c *cli) streamRun(ctx context.Context, w io.Writer, runID string) (run, error) {
	offsets := map[string]int64{}
	announced := map[string]bool{}
	drained := map[string]bool{}
	for {
		var out runDetail
		if err := c.api().get(ctx, "/runs/"+runID, nil, &out); err != nil {
			return run{}, err
		}
		for _, j := range out.Jobs {
			for _, st := range j.Steps {
				terminal := st.Status != "running" && st.Status != "queued"
				if !announced[st.ID] && (st.Status == "running" || terminal) {
					fmt.Fprintf(w, "== %s › %s\n", j.Name, st.Name)
					announced[st.ID] = true
				}
				if drained[st.ID] || st.Status == "queued" {
					continue
				}
				var chunk stepLog
				q := url.Values{"offset": {strconv.FormatInt(offsets[st.ID], 10)}}
				if err := c.api().get(ctx, "/steps/"+st.ID+"/logs", q, &chunk); err != nil {
					continue
				}
				if chunk.Text != "" {
					fmt.Fprint(w, chunk.Text)
				}
				offsets[st.ID] = chunk.NextOffset
				if terminal && offsets[st.ID] >= chunk.Size {
					drained[st.ID] = true
					fmt.Fprintf(w, "-- %s: %s\n", st.Name, first(chunk.Conclusion, st.Conclusion, "done"))
				}
			}
		}
		if out.Run.Status == "completed" || out.Run.Status == "cancelled" {
			return out.Run, nil
		}
		select {
		case <-ctx.Done():
			return out.Run, ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
}

// pullArtifacts downloads this run's artifact archives into dir.
func (c *cli) pullArtifacts(ctx context.Context, w io.Writer, runID, dir string) error {
	var out struct {
		Artifacts []artifact `json:"artifacts"`
	}
	if err := c.api().get(ctx, "/artifacts", url.Values{"limit": {"500"}}, &out); err != nil {
		return err
	}
	mine := []artifact{}
	for _, a := range out.Artifacts {
		if a.RunID == runID {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		fmt.Fprintln(w, "no artifacts produced")
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range mine {
		path := filepath.Join(dir, a.Name+".tar.gz")
		fh, err := os.Create(path)
		if err != nil {
			return err
		}
		err = c.api().download(ctx, "/artifacts/"+a.ID+"/download", fh)
		fh.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "pulled %s → %s\n", a.Name, path)
	}
	return nil
}

// bundleDir packs dir into a gzipped tar and returns its path, the file count
// and a content digest of the tree. Inside a git worktree it ships tracked +
// untracked-nonignored files (git ls-files --exclude-standard); elsewhere it
// walks the tree, always excluding .git.
//
// The digest covers each entry's path and content (or symlink target), so it
// is stable across runs even though tar metadata like mtimes is not.
func bundleDir(dir string) (string, int, string, error) {
	entries, err := listBundleFiles(dir)
	if err != nil {
		return "", 0, "", err
	}
	tmp, err := os.CreateTemp("", "kern-exec-*.tar.gz")
	if err != nil {
		return "", 0, "", err
	}
	digest := sha256.New()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	for _, rel := range entries {
		full := filepath.Join(dir, rel)
		fi, err := os.Lstat(full)
		if err != nil {
			continue // vanished between listing and packing
		}
		link := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(full); err != nil {
				continue
			}
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			continue
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			tmp.Close()
			return "", 0, "", err
		}
		fmt.Fprintf(digest, "%s\x00", hdr.Name)
		if fi.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(digest, "symlink:%s\x00", link)
			continue
		}
		if fi.Mode().IsRegular() {
			fh := sha256.New()
			src, err := os.Open(full)
			if err != nil {
				tmp.Close()
				return "", 0, "", err
			}
			_, copyErr := io.Copy(tw, io.TeeReader(src, fh))
			src.Close()
			if copyErr != nil {
				tmp.Close()
				return "", 0, "", copyErr
			}
			fmt.Fprintf(digest, "file:%x\x00", fh.Sum(nil))
			continue
		}
		fmt.Fprintf(digest, "mode:%v\x00", fi.Mode())
	}
	if err := tw.Close(); err != nil {
		tmp.Close()
		return "", 0, "", err
	}
	if err := gz.Close(); err != nil {
		tmp.Close()
		return "", 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", err
	}
	return tmp.Name(), len(entries), hex.EncodeToString(digest.Sum(nil)), nil
}

// isStatus reports whether err is an apiError with the given HTTP status.
func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

func listBundleFiles(dir string) ([]string, error) {
	if insideWorkTree(dir) {
		cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--", ".")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w", err)
		}
		var files []string
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				files = append(files, p)
			}
		}
		return files, nil
	}
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

func insideWorkTree(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
