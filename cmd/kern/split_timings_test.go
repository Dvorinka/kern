package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLPTBuckets(t *testing.T) {
	b := lptBuckets([]string{"a", "b", "c", "d"}, map[string]float64{"a": 10, "b": 6, "c": 4}, 2)
	if len(b) != 2 || len(b[0])+len(b[1]) != 4 {
		t.Fatalf("bad buckets %v", b)
	}
	if len(b[0]) == 0 || len(b[1]) == 0 {
		t.Fatal("empty shard")
	}
}

func TestLoadTimingsJUnit(t *testing.T) {
	dir := t.TempDir()
	j := filepath.Join(dir, "j.xml")
	os.WriteFile(j, []byte(`<testsuite><testcase classname="x" file="pkg/a_test.go" time="2.5"/><testcase classname="x" file="pkg/b_test.go" time="1"/></testsuite>`), 0o644)
	m, err := loadTimings(j, []string{"pkg/a_test.go", "pkg/b_test.go", "pkg/c_test.go"}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if m["pkg/a_test.go"] != 2.5 || m["pkg/b_test.go"] != 1 {
		t.Fatalf("%v", m)
	}
}

func TestLoadTimingsGoJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t_test.go"), []byte("package t\nfunc TestSlow(t *testing.T) {}"), 0o644)
	j := filepath.Join(dir, "go.json")
	os.WriteFile(j, []byte(`{"Action":"pass","Package":"x","Test":"TestSlow","Elapsed":3.0}`+"\n"), 0o644)
	m, err := loadTimings(j, []string{"t_test.go"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if m["t_test.go"] != 3.0 {
		t.Fatalf("%v", m)
	}
}
