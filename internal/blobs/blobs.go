// Package blobs stores log streams and artifacts outside PostgreSQL.
//
// The filesystem driver is the default for single-host installs; the Store interface is
// what an S3/MinIO driver will implement without touching callers.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Store interface {
	// Append adds data to a log stream and returns the total size after the write.
	Append(key string, data []byte) (int64, error)
	// Put writes an artifact and returns its size and sha256.
	Put(key string, r io.Reader) (int64, string, error)
	// ReadFrom returns up to max bytes starting at offset, plus the total size.
	ReadFrom(key string, offset int64, max int) ([]byte, int64, error)
	// Open streams the whole object; the caller closes it. Returns ErrNotFound-able
	// nil reader semantics: (nil, 0, nil) when the key does not exist.
	Open(key string) (io.ReadCloser, int64, error)
	// Size returns the current size of a key.
	Size(key string) (int64, error)
	Delete(key string) error
}

type Filesystem struct {
	root string
	mu   sync.Mutex
}

func NewFilesystem(root string) (*Filesystem, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	return &Filesystem{root: root}, nil
}

// path validates keys so callers cannot escape the blob root.
func (f *Filesystem) path(key string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(key, "/"))
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid blob key %q", key)
	}
	return filepath.Join(f.root, clean), nil
}

func (f *Filesystem) Append(key string, data []byte) (int64, error) {
	p, err := f.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fh, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	if _, err := fh.Write(data); err != nil {
		return 0, err
	}
	info, err := fh.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (f *Filesystem) Put(key string, r io.Reader) (int64, string, error) {
	p, err := f.path(key)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fh, err := os.Create(p)
	if err != nil {
		return 0, "", err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(fh, h), r)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (f *Filesystem) ReadFrom(key string, offset int64, max int) ([]byte, int64, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	size := info.Size()
	if offset >= size {
		return nil, size, nil
	}
	if max <= 0 || max > 1<<20 {
		max = 256 << 10
	}
	fh, err := os.Open(p)
	if err != nil {
		return nil, size, err
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		return nil, size, err
	}
	buf := make([]byte, max)
	n, err := fh.Read(buf)
	if err != nil && err != io.EOF {
		return nil, size, err
	}
	return buf[:n], size, nil
}

func (f *Filesystem) Open(key string) (io.ReadCloser, int64, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, 0, err
	}
	fh, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	info, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, 0, err
	}
	return fh, info.Size(), nil
}

func (f *Filesystem) Size(key string) (int64, error) {
	p, err := f.path(key)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(p)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (f *Filesystem) Delete(key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LogKey is the canonical blob key for a step log.
func LogKey(runID, jobID string, stepNumber int) string {
	return fmt.Sprintf("logs/%s/%s/%d.log", runID, jobID, stepNumber)
}
