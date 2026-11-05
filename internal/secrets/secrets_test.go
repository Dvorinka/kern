package secrets

import "testing"

func TestMaskerBreadth(t *testing.T) {
	m := NewMasker([]string{"", "secret", "secret", "supersecret", "tok"})
	// Longer values first, so a secret that contains another is not left half-masked.
	if got := m.Mask("supersecret and secret and secret"); got != "*** and *** and ***" {
		t.Fatalf("mask: %q", got)
	}
	if got := m.Mask("prefix tok\nmid secret end"); got != "prefix ***\nmid *** end" {
		t.Fatalf("positions: %q", got)
	}
	if got := m.Mask("Secret"); got != "Secret" {
		t.Fatalf("case: %q", got)
	}
	if got := m.Mask("nothing"); got != "nothing" {
		t.Fatalf("untouched: %q", got)
	}
	// Empty input is dropped; a repeated value is stored once.
	if len(m.values) != 3 {
		t.Fatalf("values: %#v", m.values)
	}
}

func TestMaskerEmpty(t *testing.T) {
	m := NewMasker([]string{"", ""})
	in := []byte("leave me")
	out := m.MaskBytes(in)
	if &out[0] != &in[0] {
		t.Fatal("empty masker should return the input chunk")
	}
	if got := m.Mask("leave me"); got != "leave me" {
		t.Fatalf("empty mask: %q", got)
	}
}
