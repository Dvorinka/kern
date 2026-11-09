package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/config"
)

func testGHCacheServer() *Server {
	return &Server{
		Cfg:     &config.Config{SessionSecret: "test-session-secret-0123456789abcdef"},
		ghCache: newGHCacheState(),
	}
}

func TestCacheJobTokenRoundTrip(t *testing.T) {
	s := testGHCacheServer()
	jobID := uuid.New()
	token := s.cacheJobToken(jobID)
	got, ok := s.parseCacheJobToken(token)
	if !ok || got != jobID {
		t.Fatalf("round trip failed: got %v ok=%v", got, ok)
	}
}

func TestCacheJobTokenRejects(t *testing.T) {
	s := testGHCacheServer()
	jobID := uuid.New()
	token := s.cacheJobToken(jobID)
	for name, bad := range map[string]string{
		"empty":      "",
		"garbage":    "not-a-token",
		"runner-ish": "run_abc.def",
		"wrong sig":  token[:len(token)-2] + "xx",
	} {
		if _, ok := s.parseCacheJobToken(bad); ok {
			t.Fatalf("%s: token unexpectedly accepted", name)
		}
	}
	// A token minted for another job parses fine — the route layer binds it
	// to the :jobID param, so cross-job use is rejected there.
	other := s.cacheJobToken(uuid.New())
	if got, ok := s.parseCacheJobToken(other); !ok || got == jobID {
		t.Fatalf("other-job token should parse to its own job id, got %v ok=%v", got, ok)
	}
}

func TestCacheJobTokenExpiry(t *testing.T) {
	s := testGHCacheServer()
	// Forge an expired token carrying a valid signature.
	jobID := uuid.New()
	exp := time.Now().Add(-time.Hour).Unix()
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte(fmt.Sprintf("ghcache|%s|%d", jobID, exp)))
	token := fmt.Sprintf("kc1.%s.%d.%s", jobID, exp,
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	if _, ok := s.parseCacheJobToken(token); ok {
		t.Fatal("expired token accepted")
	}
}

func TestParseContentRange(t *testing.T) {
	for _, tc := range []struct {
		h          string
		start, end int64
		ok         bool
	}{
		{"bytes 0-99/*", 0, 99, true},
		{"bytes 100-199/1024", 100, 199, true},
		{"bytes 0-0/*", 0, 0, true},
		{"", 0, 0, false},
		{"bytes */*", 0, 0, false},
		{"bytes 5-3/*", 0, 0, false},
		{"items 0-9/*", 0, 0, false},
	} {
		st, en, ok := parseContentRange(tc.h)
		if ok != tc.ok || st != tc.start || en != tc.end {
			t.Fatalf("parseContentRange(%q) = %d,%d,%v; want %d,%d,%v",
				tc.h, st, en, ok, tc.start, tc.end, tc.ok)
		}
	}
}
