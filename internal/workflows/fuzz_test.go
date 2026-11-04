package workflows

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzCompile feeds arbitrary bytes through the workflow compiler. The contract
// under fuzz: never panic, and a Valid result always carries a Plan.
func FuzzCompile(f *testing.F) {
	corpus, _ := filepath.Glob("testdata/*.yml")
	for _, p := range corpus {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(string(data))
		}
	}
	f.Add("")
	f.Add("on: push\n")
	f.Add("on: [push]\njobs:\n  x:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n")

	c := Compiler{
		DefaultImage: "ubuntu:24.04",
		LabelImages:  map[string]string{"ubuntu-latest": "ubuntu:24.04"},
	}
	f.Fuzz(func(t *testing.T, content string) {
		res, err := Compile(content, c)
		if err != nil {
			return // undecodable input may return an error; panics are the bug
		}
		if res.Valid && res.Plan == nil {
			t.Fatalf("valid result without a plan")
		}
	})
}
