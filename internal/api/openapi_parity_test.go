package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/dvorinka/kern/internal/config"
)

var ginParamRe = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

// TestOpenAPIRouteParity guards against openapi.yaml drifting behind live Gin
// routes. Every route registered on the server must appear in openapi.yaml
// under the matching HTTP method and normalized path.
func TestOpenAPIRouteParity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := &Server{Cfg: &config.Config{}, Log: zap.NewNop()}
	r := srv.buildRouter()

	data, err := os.ReadFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	documented := map[string]bool{}
	for p, methods := range spec.Paths {
		for m := range methods {
			documented[strings.ToUpper(m)+" "+p] = true
		}
	}

	var missing []string
	for _, rt := range r.Routes() {
		p := strings.TrimPrefix(rt.Path, "/api/v1")
		p = ginParamRe.ReplaceAllString(p, "{$1}")
		key := strings.ToUpper(rt.Method) + " " + p
		if !documented[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes registered in api.go missing from openapi.yaml (%d):\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
