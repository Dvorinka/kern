package secrets

import (
	"strings"
	"testing"
)

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

func TestMaskerMultilineAndMinLen(t *testing.T) {
	pem := "-----BEGIN KEY-----\nabc123secret\n-----END KEY-----"
	m := NewMasker([]string{pem, "ab", "xy"})
	// The whole value masks as a unit.
	if got := m.Mask("pre "+pem+" post"); got != "pre *** post" {
		t.Fatalf("whole mask: %q", got)
	}
	// Each meaningful line masks when echoed on its own — a PEM or JSON
	// credential cannot leak line by line (#368).
	for _, line := range []string{"abc123secret", "-----BEGIN KEY-----"} {
		if got := m.Mask("echo "+line); got != "echo ***" {
			t.Fatalf("line %q: %q", line, got)
		}
	}
	// Values under three characters are ignored rather than shredding text.
	if got := m.Mask("ab xy"); got != "ab xy" {
		t.Fatalf("short values: %q", got)
	}
}

func TestStreamSplitSecret(t *testing.T) {
	m := NewMasker([]string{"supersecretvalue", "tail"})
	st := m.NewStream()
	var out strings.Builder
	out.Write(st.Write([]byte("prefix supersec")))
	out.Write(st.Write([]byte("retvalue and ta")))
	out.Write(st.Write([]byte("il end")))
	out.Write(st.Flush())
	got := out.String()
	if strings.Contains(got, "supersecretvalue") || strings.Contains(got, "tail") {
		t.Fatalf("leaked across chunks: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("expected masked output: %q", got)
	}
}

func TestStreamAddMask(t *testing.T) {
	m := NewMasker(nil)
	st := m.NewStream()
	// What the runner's ::add-mask:: parsing feeds before masking the line.
	st.Add("jobtoken42")
	out := st.Write([]byte("::add-mask::jobtoken42\nvalue=jobtoken42\n"))
	out = append(out, st.Write([]byte("again jobtoken42\n"))...)
	out = append(out, st.Flush()...)
	if strings.Contains(string(out), "jobtoken42") {
		t.Fatalf("add-mask value leaked: %q", out)
	}
}

func TestCipherAAD(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := c.EncryptAAD("s3cret", "org|proj|name")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.DecryptAAD(ct, nonce, "org|proj|name"); err != nil || got != "s3cret" {
		t.Fatalf("aad decrypt: %q %v", got, err)
	}
	// Wrong context must not open the ciphertext.
	if _, err := c.DecryptAAD(ct, nonce, "org|other|name"); err == nil {
		t.Fatal("decrypt with wrong AAD succeeded")
	}
	// Legacy nil-AAD rows still decrypt through the fallback.
	ct2, nonce2, err := c.Encrypt("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.DecryptAAD(ct2, nonce2, "any|aad"); err != nil || got != "legacy" {
		t.Fatalf("legacy fallback: %q %v", got, err)
	}
}
