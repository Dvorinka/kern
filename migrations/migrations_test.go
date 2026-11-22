package migrations

// Migration integrity gate (#incident: goose "duplicate version 37" panic
// crash-looped the API in production when two parallel PRs claimed the same
// version). These checks run under `go test ./...` in both CI pipelines —
// KERN dogfood and GitHub Actions — so a bad migration can never merge.

import (
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

var migNameRe = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

func TestMigrationIntegrity(t *testing.T) {
	var names []string
	err := fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".sql") {
			names = append(names, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)

	seen := map[int64]string{}
	var versions []int64
	for _, name := range names {
		base := name[strings.LastIndex(name, "/")+1:]
		m := migNameRe.FindStringSubmatch(base)
		if m == nil {
			t.Errorf("bad migration filename %q — want NNNN_snake_name.sql", base)
			continue
		}
		v, _ := strconv.ParseInt(m[1], 10, 64)
		if prev, dup := seen[v]; dup {
			t.Errorf("duplicate migration version %04d: %s and %s", v, prev, base)
		}
		seen[v] = base
		versions = append(versions, v)

		body, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "-- +goose Up") {
			t.Errorf("%s: missing '-- +goose Up' section", base)
		}
		if !strings.Contains(text, "-- +goose Down") {
			t.Errorf("%s: missing '-- +goose Down' section", base)
		}
	}

	// Strictly sequential, no gaps: keeps "which migration is next" obvious
	// during parallel work — the class of bug that produced the 0037 clash.
	for i, v := range versions {
		if want := int64(i + 1); v != want {
			t.Fatalf("migration sequence gap: position %d has version %04d, want %04d", i+1, v, want)
		}
	}

	// The real parser: goose panics on duplicate versions inside
	// sortAndConnectMigrations — exactly the production failure mode.
	goose.SetBaseFS(FS)
	if _, err := goose.CollectMigrations(".", 0, math.MaxInt64); err != nil {
		t.Fatalf("goose cannot parse migrations: %v", err)
	}
}
