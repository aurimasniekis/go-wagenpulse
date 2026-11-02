// Package ingest turns raw deliveries into typed observations (ADR-0013): content-addressed raw storage, safe
// archive extraction with size/entry/ratio limits, format detection and parsing of verified formats, normalization
// with dictionary metadata, and durable import jobs with per-delivery checkpoints. Acquisition (uploads, the portal
// collector) only stores raw deliveries; everything else happens here.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Blobs stores raw deliveries under dir/sha256/<2>/<64> with mode 0600.
type Blobs struct {
	dir string
}

// NewBlobs returns a blob store rooted at dir (created with 0700).
func NewBlobs(dir string) (*Blobs, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sha256"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o700); err != nil {
		return nil, err
	}
	return &Blobs{dir: dir}, nil
}

// ErrTooLarge is returned when a blob exceeds its size limit.
var ErrTooLarge = errors.New("content exceeds the size limit")

// Put streams r into the store (at most limit bytes) and returns its SHA-256 and size. Existing content is kept.
func (b *Blobs) Put(r io.Reader, limit int64) (sum []byte, size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(b.dir, "tmp"), "upload-*")
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if n > limit {
		return nil, n, ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return nil, 0, err
	}
	if err := tmp.Close(); err != nil {
		return nil, 0, err
	}
	sum = h.Sum(nil)
	dst := b.Path(sum)
	if _, err := os.Stat(dst); err == nil {
		return sum, n, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, 0, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return nil, 0, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return nil, 0, fmt.Errorf("store blob: %w", err)
	}
	return sum, n, nil
}

// Path returns the file path of a blob.
func (b *Blobs) Path(sum []byte) string {
	h := hex.EncodeToString(sum)
	return filepath.Join(b.dir, "sha256", h[:2], h)
}

// Open opens a stored blob.
func (b *Blobs) Open(sum []byte) (*os.File, error) {
	return os.Open(b.Path(sum))
}

// Delete removes a blob (retention); a missing blob is not an error.
func (b *Blobs) Delete(sum []byte) error {
	err := os.Remove(b.Path(sum))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Dir returns the root directory.
func (b *Blobs) Dir() string { return b.dir }
