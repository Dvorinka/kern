package update

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"
)

func TestEmbeddedKeyMatchesOpenSSL(t *testing.T) {
	// openssl pkeyutl -sign -rawin of the four bytes "kern".
	sig, err := hex.DecodeString("46a381f59e9ed0694033a42f76587d90c0f51ed1b685026dd645a3ecf23ce4f428f5c72eccb1aac969e98866215f23c9e2284b464bb58a87fda34e10b585720a")
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(publicKey(), []byte("kern"), sig) {
		t.Fatal("compiled-in key does not verify the openssl signature")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":"v0.4.0","git_sha":"0123456789abcdef0123456789abcdef01234567","runner_image":"ghcr.io/dvorinka/kern-runner","runner_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	sig := ed25519.Sign(priv, raw)
	if _, err := Verify(raw, sig); err == nil {
		t.Fatal("a different key must not verify")
	}
	if !ed25519.Verify(pub, raw, sig) {
		t.Fatal("generated key did not round-trip")
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.GitSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatal(m.GitSHA)
	}
	ref, err := PullRef(m.RunnerImage+":v0.4.0", m.RunnerDigest)
	if err != nil {
		t.Fatal(err)
	}
	if ref != RunnerImage+"@"+m.RunnerDigest {
		t.Fatal(ref)
	}
}

func TestParseRejects(t *testing.T) {
	good := Manifest{
		Version:      "v0.4.0",
		GitSHA:       "0123456789abcdef0123456789abcdef01234567",
		RunnerImage:  RunnerImage,
		RunnerDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	cases := []Manifest{
		{Version: "dev", GitSHA: good.GitSHA, RunnerImage: good.RunnerImage, RunnerDigest: good.RunnerDigest},
		{Version: good.Version, GitSHA: "abc", RunnerImage: good.RunnerImage, RunnerDigest: good.RunnerDigest},
		{Version: good.Version, GitSHA: good.GitSHA, RunnerImage: "docker.io/library/alpine", RunnerDigest: good.RunnerDigest},
		{Version: good.Version, GitSHA: good.GitSHA, RunnerImage: good.RunnerImage, RunnerDigest: "sha256:abcd"},
	}
	for _, c := range cases {
		b := []byte(`{"version":"` + c.Version + `","git_sha":"` + c.GitSHA + `","runner_image":"` + c.RunnerImage + `","runner_digest":"` + c.RunnerDigest + `"}`)
		if _, err := Parse(b); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "v0.3.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"v0.2.0", "v0.2.1", true},
		{"v0.2.0", "v1.0.0", true},
		{"dev", "v0.2.0", false},
		{"v0.2.0", "latest", false},
		{"v0.2.0-rc1", "v0.2.0", false},
		{"0.2", "v0.2.0", false},
		{"", "v0.2.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
	if Comparable("dev") || !Comparable("v0.3.0") {
		t.Fatal("Comparable")
	}
	if !RunnerShouldOffer(true, 0, false, "v0.3.0", "v0.4.0") {
		t.Fatal("offer")
	}
	if RunnerShouldOffer(true, 1, false, "v0.3.0", "v0.4.0") ||
		RunnerShouldOffer(false, 0, false, "v0.3.0", "v0.4.0") ||
		RunnerShouldOffer(true, 0, true, "v0.3.0", "v0.4.0") ||
		RunnerShouldOffer(true, 0, false, "dev", "v0.4.0") {
		t.Fatal("offer must stay closed")
	}
}

func TestLeaseOneAtATime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := &Lease{Now: func() time.Time { return now }}
	if !l.Allow("a", time.Minute) {
		t.Fatal("a")
	}
	if l.Allow("b", time.Minute) {
		t.Fatal("b should wait")
	}
	if !l.Allow("a", time.Minute) {
		t.Fatal("a keeps the window")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("b", time.Minute) {
		t.Fatal("b after the window")
	}
	if l.Allow("", time.Minute) {
		t.Fatal("empty id")
	}
}

func TestManifestBinaries(t *testing.T) {
	raw := []byte(`{"version":"v0.4.0","git_sha":"0123456789abcdef0123456789abcdef01234567","runner_image":"ghcr.io/dvorinka/kern-runner","runner_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","binaries":{"darwin-amd64":{"url":"https://github.com/x/releases/download/v0.4.0/kern-v0.4.0-darwin-amd64.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Binaries["darwin-amd64"].SHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("binary entry lost")
	}
	bad := []byte(`{"version":"v0.4.0","git_sha":"0123456789abcdef0123456789abcdef01234567","runner_image":"ghcr.io/dvorinka/kern-runner","runner_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","binaries":{"plan9-mips":{"url":"https://x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
	if _, err := Parse(bad); err == nil {
		t.Fatal("bad target must fail")
	}
}
