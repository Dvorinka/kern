// Package update verifies a signed release manifest and decides when a
// version is newer. It does not download or apply anything.
package update

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RunnerImage is the only image a fleet runner is allowed to pull for a
// self-replace. The tag is ignored; the digest is the pin.
const RunnerImage = "ghcr.io/dvorinka/kern-runner"

var (
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sumRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	targetRe = regexp.MustCompile(`^(linux|darwin|windows)-(amd64|arm64)$`)
)

// Binary is a signed per-platform runner tarball entry. Host runners —
// which have no container to replace — consume these instead of the image.
type Binary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Manifest is the exact document a release publishes as kern-update.json.
// The signature covers those bytes, not a re-encoded form.
type Manifest struct {
	Version      string            `json:"version"`
	GitSHA       string            `json:"git_sha"`
	RunnerImage  string            `json:"runner_image"`
	RunnerDigest string            `json:"runner_digest"`
	Binaries     map[string]Binary `json:"binaries,omitempty"`
}

// Verify checks the ed25519 signature and then the field rules.
func Verify(manifest, sig []byte) (Manifest, error) {
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey(), manifest, sig) {
		return Manifest{}, fmt.Errorf("manifest signature does not match the compiled-in key")
	}
	return Parse(manifest)
}

// Parse decodes a manifest and rejects anything the updater must not act on.
func Parse(manifest []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest is not JSON: %w", err)
	}
	m.Version = strings.TrimSpace(m.Version)
	m.GitSHA = strings.TrimSpace(m.GitSHA)
	m.RunnerImage = strings.TrimSpace(m.RunnerImage)
	m.RunnerDigest = strings.TrimSpace(m.RunnerDigest)
	if !Comparable(m.Version) {
		return Manifest{}, fmt.Errorf("manifest version %q is not a dotted triple", m.Version)
	}
	if !shaRe.MatchString(m.GitSHA) {
		return Manifest{}, fmt.Errorf("manifest git_sha must be 40 hex characters")
	}
	if _, err := PullRef(m.RunnerImage, m.RunnerDigest); err != nil {
		return Manifest{}, err
	}
	for target, b := range m.Binaries {
		if !targetRe.MatchString(target) {
			return Manifest{}, fmt.Errorf("binaries key %q must be <os>-<arch>", target)
		}
		if !strings.HasPrefix(b.URL, "https://") {
			return Manifest{}, fmt.Errorf("binaries[%s] url must be https", target)
		}
		if !sumRe.MatchString(b.SHA256) {
			return Manifest{}, fmt.Errorf("binaries[%s] sha256 must be 64 hex characters", target)
		}
	}
	return m, nil
}

// PullRef returns image@digest. The tag on image is ignored so a moved
// tag cannot change what the runner pulls.
func PullRef(image, digest string) (string, error) {
	base := image
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i:], "/") {
		base = image[:i]
	}
	if base != RunnerImage {
		return "", fmt.Errorf("runner image must be %s", RunnerImage)
	}
	if !digestRe.MatchString(digest) {
		return "", fmt.Errorf("runner digest must be sha256 and 64 hex characters")
	}
	return base + "@" + digest, nil
}

// Comparable reports whether Newer can order this version. "dev" and git
// describe strings are not comparable, and auto-apply must refuse them.
func Comparable(v string) bool {
	_, ok := parseVer(v)
	return ok
}

// RunnerShouldOffer is the per-runner gate. The lease then keeps the
// fleet to one replace at a time. A non-comparable current version
// (the "dev" stamp) never offers.
func RunnerShouldOffer(optedIn bool, active int, draining bool, current, target string) bool {
	return optedIn && active == 0 && !draining && Newer(current, target)
}

// Newer reports whether latest is a higher dotted triple than current.
// Unparseable versions, including "dev", return false.
func Newer(current, latest string) bool {
	cur, ok1 := parseVer(current)
	lat, ok2 := parseVer(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range cur {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

func parseVer(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimRight(p, "+-"))
		if err != nil {
			n, err = strconv.Atoi(strings.SplitN(p, "-", 2)[0])
			if err != nil {
				return out, false
			}
		}
		out[i] = n
	}
	return out, true
}
