package runner

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dvorinka/kern/internal/update"
)

// inContainer reports whether this runner is itself a container — the
// docker self-replace path — rather than a host install.
func inContainer() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// applyBinaryUpdate swaps the running executable for the signed release
// binary matching this platform, then relaunches. On unix it re-execs so
// the supervisor never sees an exit; on windows it spawns the new process
// detached before exiting (a running exe cannot be overwritten, only
// renamed).
func applyBinaryUpdate(ctx context.Context, ver string, bins map[string]update.Binary, busy func() bool) error {
	target := runtime.GOOS + "-" + runtime.GOARCH
	b, ok := bins[target]
	if !ok {
		return fmt.Errorf("no signed binary for %s", target)
	}
	if busy != nil && busy() {
		return fmt.Errorf("a job is running")
	}
	exe, err := selfPath()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kern-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	arc := filepath.Join(dir, "release.tar.gz")
	if err := downloadFile(ctx, b.URL, arc); err != nil {
		return err
	}
	sum, err := fileSHA256(arc)
	if err != nil {
		return err
	}
	if sum != b.SHA256 {
		return fmt.Errorf("binary checksum mismatch: got %s, manifest says %s", sum, b.SHA256)
	}
	// The staged binary lives next to the target so the swap rename stays
	// on one filesystem.
	newBin := exe + ".new"
	if err := extractRunner(arc, newBin); err != nil {
		return err
	}
	if busy != nil && busy() {
		return fmt.Errorf("a job started during the download")
	}
	if err := swapBinary(exe, newBin); err != nil {
		return err
	}
	return relaunchSelf(exe)
}

// selfPath resolves the running executable's real path — the file the
// supervisor will exec on restart.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractRunner pulls kern-runner (or kern-runner.exe) out of the release
// tarball into dest.
func extractRunner(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	want := "kern-runner"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no %s in release tarball", want)
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != want || hdr.FileInfo().IsDir() {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, tr)
		return err
	}
}
