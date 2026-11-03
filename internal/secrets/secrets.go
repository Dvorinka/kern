// Package secrets provides at-rest encryption and log masking for secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
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

func (c *Cipher) Encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return c.aead.Seal(nil, nonce, []byte(plaintext), nil), nonce, nil
}

func (c *Cipher) Decrypt(ciphertext, nonce []byte) (string, error) {
	out, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(out), nil
}

// Masker replaces secret values in log output before it is stored.
type Masker struct {
	values []string
}

func NewMasker(values []string) *Masker {
	uniq := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || uniq[v] {
			continue
		}
		uniq[v] = true
		out = append(out, v)
	}
	// Replace longer values first so a secret containing another secret is fully masked.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return &Masker{values: out}
}

func (m *Masker) Mask(s string) string {
	for _, v := range m.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// MaskBytes masks a log chunk without breaking ANSI escape sequences.
func (m *Masker) MaskBytes(b []byte) []byte {
	if len(m.values) == 0 {
		return b
	}
	return []byte(m.Mask(string(b)))
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
