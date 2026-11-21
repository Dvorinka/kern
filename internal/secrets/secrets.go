// Package secrets provides at-rest encryption and log masking for secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Cipher encrypts secrets with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext without additional authenticated data.
func (c *Cipher) Encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	return c.EncryptAAD(plaintext, "")
}

// EncryptAAD seals plaintext bound to context (org/project/environment/name).
// Rows written before AAD binding existed keep decrypting via the nil-AAD
// fallback in DecryptAAD; they re-encrypt with AAD on their next write.
func (c *Cipher) EncryptAAD(plaintext, aad string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return c.aead.Seal(nil, nonce, []byte(plaintext), []byte(aad)), nonce, nil
}

func (c *Cipher) Decrypt(ciphertext, nonce []byte) (string, error) {
	return c.DecryptAAD(ciphertext, nonce, "")
}

// DecryptAAD opens ciphertext sealed with aad, falling back to the legacy
// unauthenticated form so pre-AAD rows keep working.
func (c *Cipher) DecryptAAD(ciphertext, nonce []byte, aad string) (string, error) {
	out, err := c.aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil && aad != "" {
		out, err = c.aead.Open(nil, nonce, ciphertext, nil)
	}
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(out), nil
}

// minMaskLen is the shortest value the masker will hide. Anything shorter
// (a 1-2 char secret) would shred ordinary log text for negligible gain.
const minMaskLen = 3

// Masker replaces secret values in log output before it is stored. Values
// shorter than minMaskLen are ignored; multiline secrets contribute each of
// their lines so a PEM block or JSON credential cannot leak line by line.
// Use Mask for complete strings and Stream for chunked byte streams.
type Masker struct {
	mu     sync.Mutex
	values []string // sorted longest-first
	pis    [][]int  // KMP prefix tables parallel to values
	maxLen int
}

// NewMasker builds a masker over the given secret values.
func NewMasker(values []string) *Masker {
	m := &Masker{}
	for _, v := range values {
		m.add(v)
	}
	return m
}

// add registers one value and, for multiline values, each of its lines.
// Caller must hold m.mu.
func (m *Masker) add(v string) {
	if v == "" {
		return
	}
	m.addOne(v)
	if strings.Contains(v, "\n") {
		for _, line := range strings.Split(v, "\n") {
			m.addOne(strings.TrimSuffix(line, "\r"))
		}
	}
}

func (m *Masker) addOne(v string) {
	if len(v) < minMaskLen {
		return
	}
	for _, e := range m.values {
		if e == v {
			return
		}
	}
	pi := prefixFunc(v)
	i := sort.Search(len(m.values), func(i int) bool { return len(m.values[i]) < len(v) })
	m.values = append(m.values, "")
	copy(m.values[i+1:], m.values[i:])
	m.values[i] = v
	m.pis = append(m.pis, nil)
	copy(m.pis[i+1:], m.pis[i:])
	m.pis[i] = pi
	if len(v) > m.maxLen {
		m.maxLen = len(v)
	}
}

// Add registers a value discovered at runtime — the runner's ::add-mask::
// workflow command feeds this so job-emitted masks apply to later output.
func (m *Masker) Add(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.add(v)
}

// Mask replaces every occurrence of a known value in s.
func (m *Masker) Mask(s string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maskLocked(s)
}

func (m *Masker) maskLocked(s string) string {
	for _, v := range m.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// MaskBytes masks a complete buffer. For chunked streams use Stream — a
// secret split across two MaskBytes calls would not be caught here.
func (m *Masker) MaskBytes(b []byte) []byte {
	if len(m.values) == 0 {
		return b
	}
	return []byte(m.Mask(string(b)))
}

// NewStream returns a chunked masking stream over the same values.
func (m *Masker) NewStream() *Stream {
	return &Stream{m: m}
}

// Stream masks a byte stream fed in arbitrary chunks (log flushes). A
// suffix that could be the start of a secret split across a chunk boundary
// is held back and re-examined with the next Write; Flush releases the
// remainder when the stream ends.
type Stream struct {
	mu      sync.Mutex
	m       *Masker
	pending []byte
}

// Write masks b and returns the bytes safe to emit now; a trailing partial
// secret stays buffered for the next call.
func (s *Stream) Write(b []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if len(s.m.values) == 0 {
		return b
	}
	work := append(s.pending, b...)
	hold := s.m.holdLen(work)
	emit := s.m.maskLocked(string(work[:len(work)-hold]))
	tail := work[len(work)-hold:]
	if cap(s.pending) < len(tail) {
		s.pending = make([]byte, 0, s.m.maxLen+1)
	}
	s.pending = append(s.pending[:0], tail...)
	return []byte(emit)
}

// Flush masks and returns any held-back tail. The stream must not be
// written to afterwards.
func (s *Stream) Flush() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := []byte(s.m.maskLocked(string(s.pending)))
	s.pending = s.pending[:0]
	return out
}

// Add registers a runtime mask value (see Masker.Add).
func (s *Stream) Add(v string) { s.m.Add(v) }

// holdLen returns the length of the longest suffix of work that is a proper
// prefix of some secret — the bytes that must be held back because the
// secret may complete in the next chunk. Caller must hold m.mu.
func (m *Masker) holdLen(work []byte) int {
	if m.maxLen == 0 {
		return 0
	}
	scan := work
	if len(scan) > m.maxLen {
		scan = scan[len(scan)-m.maxLen:]
	}
	hold := 0
	for i, v := range m.values {
		// KMP state after scan = longest prefix of v that is a suffix of
		// scan. q == len(v) means the whole secret sits at the end and is
		// masked in place — no hold needed from this value.
		if q := kmpState(m.pis[i], v, scan); q < len(v) && q > hold {
			hold = q
		}
	}
	return hold
}

// prefixFunc computes the KMP failure table for pat.
func prefixFunc(pat string) []int {
	pi := make([]int, len(pat))
	for i := 1; i < len(pat); i++ {
		j := pi[i-1]
		for j > 0 && pat[i] != pat[j] {
			j = pi[j-1]
		}
		if pat[i] == pat[j] {
			j++
		}
		pi[i] = j
	}
	return pi
}

// kmpState returns the length of the longest prefix of pat that is a suffix
// of text, after scanning text with KMP.
func kmpState(pi []int, pat string, text []byte) int {
	q := 0
	for _, c := range text {
		for q > 0 && (q == len(pat) || pat[q] != c) {
			q = pi[q-1]
		}
		if q < len(pat) && pat[q] == c {
			q++
		}
	}
	return q
}

// Encode seals a value into the printable form used by the API.
func (c *Cipher) Encode(plaintext string) (string, string, error) {
	ct, nonce, err := c.Encrypt(plaintext)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(ct), base64.StdEncoding.EncodeToString(nonce), nil
}

func (c *Cipher) Decode(ciphertextB64, nonceB64 string) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return "", err
	}
	return c.Decrypt(ct, nonce)
}
