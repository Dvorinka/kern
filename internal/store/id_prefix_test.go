package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseIDPrefix(t *testing.T) {
	u := uuid.New()
	if got, pfx, ok := ParseIDPrefix(u.String()); !ok || got != u || pfx != "" {
		t.Fatalf("full uuid: got=%v pfx=%q ok=%v", got, pfx, ok)
	}
	if got, pfx, ok := ParseIDPrefix("ABCD1234"); !ok || got != uuid.Nil || pfx != "abcd1234" {
		t.Fatalf("short hex: got=%v pfx=%q ok=%v", got, pfx, ok)
	}
	if got, pfx, ok := ParseIDPrefix("abcd1234-ef56"); !ok || got != uuid.Nil || pfx != "abcd1234ef56" {
		t.Fatalf("hyphenated prefix: got=%v pfx=%q ok=%v", got, pfx, ok)
	}
	for _, bad := range []string{"", "abc", "zzzz1234", "123456789012345678901234567890123"} {
		if _, _, ok := ParseIDPrefix(bad); ok {
			t.Errorf("expected invalid for %q", bad)
		}
	}
}
