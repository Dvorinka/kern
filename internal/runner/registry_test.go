package runner

import "testing"

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"registry.internal:5000/db:16":    "registry.internal:5000",
		"registry.internal:5000/db":       "registry.internal:5000",
		"gcr.io/proj/img:latest":          "gcr.io",
		"localhost:5000/img":              "localhost:5000",
		"docker.io/library/ubuntu:24.04":  "docker.io",
		"ubuntu:24.04":                    "",
		"postgres":                        "",
		"myregistry.io/ns/img@sha256:abc": "myregistry.io",
	}
	for in, want := range cases {
		if got := registryHost(in); got != want {
			t.Errorf("registryHost(%q) = %q, want %q", in, got, want)
		}
	}
}
