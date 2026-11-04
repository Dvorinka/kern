package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// verifyJWT checks the bearer token on a request is a correctly-signed app JWT.
func verifyJWT(t *testing.T, r *http.Request, pub *rsa.PublicKey, wantISS string) {
	t.Helper()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(auth, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("jwt signature encoding: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature invalid: %v", err)
	}
	var claims struct {
		ISS string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("jwt claims: %v", err)
	}
	if claims.ISS != wantISS {
		t.Fatalf("iss = %q, want %q", claims.ISS, wantISS)
	}
	if d := time.Until(time.Unix(claims.Exp, 0)); d < 7*time.Minute || d > 10*time.Minute {
		t.Fatalf("jwt exp window %v outside 7-10m", d)
	}
}

func TestAppInstallationTokenFlow(t *testing.T) {
	key, pemStr := testKey(t)
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r, &key.PublicKey, "42")
		switch r.URL.Path {
		case "/repos/o/r/installation":
			json.NewEncoder(w).Encode(map[string]any{"id": 9001})
		case "/app/installations/9001/access_tokens":
			tokenCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_installation_token",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	app, err := NewApp("42", pemStr, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		cl, err := app.Client(ctx, "o/r")
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		if cl.token != "ghs_installation_token" {
			t.Fatalf("client token = %q", cl.token)
		}
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("access_tokens called %d times, want 1 (cache miss on second call)", got)
	}
	if id, err := app.InstallationID(ctx, "o/r"); err != nil || id != 9001 {
		t.Fatalf("installation id = %d, %v", id, err)
	}
}

func TestAppRejectsBadKey(t *testing.T) {
	if _, err := NewApp("42", "not a pem", ""); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestResolverPrefersApp(t *testing.T) {
	key, pemStr := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r, &key.PublicKey, "1")
		if r.URL.Path == "/repos/o/r/installation" {
			json.NewEncoder(w).Encode(map[string]any{"id": 7})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer srv.Close()
	app, err := NewApp("1", pemStr, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{App: app, PAT: New(srv.URL, "pat-token")}
	cl, err := r.Client(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if cl.token != "ghs_x" {
		t.Fatalf("resolver returned pat client, want app token (got %q)", cl.token)
	}
}

func TestResolverFallsBackToPAT(t *testing.T) {
	pat := New("https://api.github.com", "pat-token")
	r := &Resolver{PAT: pat}
	cl, err := r.Client(context.Background(), "o/r")
	if err != nil || cl != pat {
		t.Fatalf("want pat client, got %v %v", cl, err)
	}
}
