package api

import (
	"testing"

	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/store"
)

func TestJobImageFor(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		DefaultImage: "ghcr.io/dvorinka/kern-ubuntu:24.04",
		Arm64Image:   "registry.local/kern-ubuntu:24.04-arm64",
	}}
	arm := store.Runner{Arch: "arm64"}
	amd := store.Runner{Arch: "amd64"}

	if got := s.jobImageFor(arm, s.Cfg.DefaultImage); got != s.Cfg.Arm64Image {
		t.Fatalf("arm64 default image: got %q", got)
	}
	if got := s.jobImageFor(amd, s.Cfg.DefaultImage); got != s.Cfg.DefaultImage {
		t.Fatalf("amd64 default image: got %q", got)
	}
	if got := s.jobImageFor(arm, "postgres:16"); got != "postgres:16" {
		t.Fatalf("explicit image must not substitute: got %q", got)
	}

	s.Cfg.Arm64Image = ""
	if got := s.jobImageFor(arm, s.Cfg.DefaultImage); got != s.Cfg.DefaultImage {
		t.Fatalf("empty Arm64Image must not substitute: got %q", got)
	}
}
