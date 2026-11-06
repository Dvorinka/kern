package geoip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewDisabled(t *testing.T) {
	for _, v := range []string{"", "off", "none", "  "} {
		if New(v) != nil {
			t.Fatalf("New(%q) should disable the resolver", v)
		}
	}
	if New("https://ipapi.co/{ip}/json/") == nil {
		t.Fatal("New(url) should build a resolver")
	}
}

func TestIsPublic(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "2a00:1450:4001::1"} {
		if !IsPublic(ip) {
			t.Fatalf("IsPublic(%s) = false, want true", ip)
		}
	}
	for _, ip := range []string{"10.0.0.65", "192.168.1.10", "172.16.0.1", "127.0.0.1", "::1", "fe80::1", "not-an-ip", ""} {
		if IsPublic(ip) {
			t.Fatalf("IsPublic(%s) = true, want false", ip)
		}
	}
}

func TestLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/8.8.8.8/json/" && r.URL.Path != "/json/" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"latitude": 50.08, "longitude": 14.46,
			"city": "Prague", "country_name": "Czechia",
		})
	}))
	defer srv.Close()

	r := New(srv.URL + "/{ip}/json/")
	g, err := r.Lookup(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if g.Lat != 50.08 || g.Lng != 14.46 || g.City != "Prague" || g.Country != "Czechia" {
		t.Fatalf("unexpected geo %+v", g)
	}
	if _, err := r.Lookup(context.Background(), ""); err != nil {
		t.Fatalf("own-egress Lookup: %v", err)
	}
}

func TestLookupRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"error": true, "reason": "RateLimited"})
	}))
	defer srv.Close()
	r := New(srv.URL + "/{ip}/json/")
	if _, err := r.Lookup(context.Background(), "8.8.8.8"); err == nil {
		t.Fatal("rate-limited lookup should fail")
	}
}
