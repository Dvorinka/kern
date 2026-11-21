package main

import (
	"context"
	"encoding/json"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dvorinka/kern/internal/version"
)

// cmdUpgrade self-updates the CLI from the latest GitHub release (#376):
// resolves the asset matching GOOS/GOARCH, downloads to a temp file, then
// atomically renames over the running binary.
func (c *cli) cmdUpgrade(ctx context.Context, args []string) error {
	const repo = "Dvorinka/kern"
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release lookup: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return err
	}
	if rel.TagName == version.Version {
		fmt.Println("already on", version.Version)
		return nil
	}
	want := fmt.Sprintf("kern-%s-%s", runtime.GOOS, runtime.GOARCH)
	dl := ""
	for _, a := range rel.Assets {
		if a.Name == want || strings.HasPrefix(a.Name, want+".") || strings.HasPrefix(a.Name, want+"-") {
			dl = a.BrowserDownloadURL
			break
		}
	}
	if dl == "" {
		return fmt.Errorf("release %s has no asset for %s", rel.TagName, want)
	}
	sumURL := ""
	for _, a := range rel.Assets {
		if a.Name == want+".sha256" || a.Name == want+"-sha256" || a.Name == "checksums.txt" {
			sumURL = a.BrowserDownloadURL
			break
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	fmt.Printf("downloading %s (%s)\n", rel.TagName, want)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, dl, nil)
	if err != nil {
		return err
	}
	resp2, err := (&http.Client{}).Do(req2)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp2.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(self), ".kern-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(resp2.Body, h)); err != nil {
		tmp.Close()
		return err
	}
	// Verify against the sibling checksum asset when the release ships one;
	// a release with no checksum file installs unverified (logged).
	if sumURL != "" {
		hexSum := hex.EncodeToString(h.Sum(nil))
		if err := verifyChecksum(ctx, sumURL, want, hexSum); err != nil {
			tmp.Close()
			return fmt.Errorf("checksum mismatch: %w", err)
		}
		fmt.Println("checksum verified")
	} else {
		fmt.Fprintln(os.Stderr, "warning: release ships no checksum asset — installing unverified")
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), self); err != nil {
		return fmt.Errorf("replace %s: %w", self, err)
	}
	fmt.Printf("kern %s -> %s\n", version.Version, rel.TagName)
	return nil
}

// verifyChecksum downloads the checksum asset and confirms the binary's
// sha256 appears next to the asset name (checksums.txt) or is the whole
// file body (<asset>.sha256).
func verifyChecksum(ctx context.Context, url, asset, hexSum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksum fetch: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) >= 1 && strings.EqualFold(f[0], hexSum) {
			if len(f) < 2 || strings.Contains(f[1], asset) {
				return nil
			}
		}
	}
	return fmt.Errorf("sha256 %s not found in %s", hexSum[:12]+"…", url)
}
