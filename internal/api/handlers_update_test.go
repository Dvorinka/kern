package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyBlock(t *testing.T) {
	if got := applyBlock(updateInfo{AutoUpdate: "off", OK: true, ManifestOK: true, Current: "v0.3.0"}); got != "auto_update is off" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "dev", Available: true}); got == "" {
		t.Fatal("dev must not apply")
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "v0.3.0", Available: true}); got != "" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: false, ManifestError: "release has no signed manifest", Current: "v0.3.0"}); got != "release has no signed manifest" {
		t.Fatal(got)
	}
	if got := applyBlock(updateInfo{AutoUpdate: "on", OK: true, ManifestOK: true, Current: "v0.4.0", Available: false}); got != "" {
		t.Fatal(got)
	}
}

func TestAssetBytesFallsBackToAssetAPI(t *testing.T) {
	var sawOctetStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl/asset":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/asset":
			sawOctetStream = r.Header.Get("Accept") == "application/octet-stream"
			w.Write([]byte("payload"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	body, err := assetBytes(context.Background(), ghAsset{
		Name:               "kern-update.json",
		URL:                srv.URL + "/api/asset",
		BrowserDownloadURL: srv.URL + "/dl/asset",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q", body)
	}
	if !sawOctetStream {
		t.Fatal("asset api fallback did not send Accept: application/octet-stream")
	}
}

func TestAssetBytesKeepsPrimaryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := assetBytes(context.Background(), ghAsset{
		Name:               "kern-update.json",
		URL:                srv.URL + "/api/asset",
		BrowserDownloadURL: srv.URL + "/dl/asset",
	}, "")
	if err == nil || !strings.Contains(err.Error(), "asset api fallback") {
		t.Fatalf("err = %v", err)
	}
}
