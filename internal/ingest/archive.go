package ingest

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// Limits bound what one delivery may expand to (ADR-0013).
type Limits struct {
	MaxExtractedBytes int64
	MaxEntries        int
	MaxRatio          int
	MaxEntryBytes     int64
}

// DefaultLimits mirror the configuration defaults.
var DefaultLimits = Limits{MaxExtractedBytes: 200 << 20, MaxEntries: 1000, MaxRatio: 100, MaxEntryBytes: 64 << 20}

// RejectError is a safe, user-facing reason a delivery was rejected (never echoes untrusted content verbatim).
type RejectError struct {
	Code   string
	Detail string
}

func (e *RejectError) Error() string { return e.Code + ": " + e.Detail }

func reject(code, format string, args ...any) error {
	return &RejectError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Document is one JSON document inside a delivery.
type Document struct {
	Name string // sanitized member name ("" for a bare JSON upload)
	Data []byte
}

var zipMagic = []byte("PK\x03\x04")

// ReadDelivery opens a stored delivery (a ZIP archive or a bare JSON document) and returns its JSON documents within
// the limits. Path traversal, absolute paths, symlinks, nested archives, duplicate names, encrypted entries and
// suspicious compression ratios are rejected.
func ReadDelivery(f *os.File, size int64, lim Limits) ([]Document, error) {
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if n == 4 && bytes.Equal(head, zipMagic) {
		return readZip(f, size, lim)
	}
	if size > lim.MaxEntryBytes {
		return nil, reject("too_large", "the document is larger than %d MiB", lim.MaxEntryBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, lim.MaxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimLeft(data, " \t\r\n\xef\xbb\xbf")
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, reject("unsupported_content", "the file is neither a ZIP archive nor a JSON document")
	}
	return []Document{{Data: data}}, nil
}

func readZip(f *os.File, size int64, lim Limits) ([]Document, error) {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return nil, reject("malformed_archive", "the ZIP archive cannot be read")
	}
	if len(zr.File) > lim.MaxEntries {
		return nil, reject("too_many_entries", "the archive has more than %d entries", lim.MaxEntries)
	}
	var total int64
	seen := map[string]bool{}
	var docs []Document
	for _, zf := range zr.File {
		name, err := safeName(zf.Name)
		if err != nil {
			return nil, err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		if zf.Mode()&os.ModeSymlink != 0 || zf.Mode()&os.ModeType != 0 {
			return nil, reject("unsafe_entry", "the archive contains a link or special file")
		}
		if zf.Flags&0x1 != 0 {
			return nil, reject("encrypted_entry", "the archive contains an encrypted entry")
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return nil, reject("duplicate_entry", "the archive contains duplicate entry names")
		}
		seen[lower] = true
		switch path.Ext(lower) {
		case ".zip", ".gz", ".tgz", ".7z", ".rar", ".tar", ".bz2", ".xz":
			return nil, reject("nested_archive", "nested archives are not accepted; upload the inner files directly")
		case ".json":
		default:
			continue // ignore non-JSON members (e.g. readme files)
		}
		if zf.UncompressedSize64 > uint64(lim.MaxEntryBytes) { //nolint:gosec // positive limit
			return nil, reject("too_large", "an archive entry is larger than %d MiB", lim.MaxEntryBytes>>20)
		}
		if zf.CompressedSize64 > 0 && zf.UncompressedSize64/zf.CompressedSize64 > uint64(lim.MaxRatio) && zf.UncompressedSize64 > 1<<20 { //nolint:gosec // positive
			return nil, reject("compression_ratio", "an archive entry has a suspicious compression ratio")
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, reject("malformed_archive", "an archive entry cannot be opened")
		}
		// The declared size may lie: read with a hard cap.
		data, err := io.ReadAll(io.LimitReader(rc, lim.MaxEntryBytes+1))
		_ = rc.Close()
		if err != nil {
			if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) {
				return nil, reject("malformed_archive", "an archive entry is corrupt")
			}
			return nil, err
		}
		if int64(len(data)) > lim.MaxEntryBytes {
			return nil, reject("too_large", "an archive entry is larger than %d MiB", lim.MaxEntryBytes>>20)
		}
		total += int64(len(data))
		if total > lim.MaxExtractedBytes {
			return nil, reject("too_large", "the archive expands to more than %d MiB", lim.MaxExtractedBytes>>20)
		}
		if size > 0 && total/size > int64(lim.MaxRatio) && total > 1<<20 {
			return nil, reject("compression_ratio", "the archive has a suspicious compression ratio")
		}
		docs = append(docs, Document{Name: name, Data: data})
	}
	if len(docs) == 0 {
		return nil, reject("no_documents", "the archive contains no JSON documents")
	}
	return docs, nil
}

// safeName validates an archive member name: relative, no traversal, printable, bounded.
func safeName(n string) (string, error) {
	if n == "" || len(n) > 255 || !utf8.ValidString(n) {
		return "", reject("unsafe_entry", "the archive contains an invalid entry name")
	}
	n = strings.ReplaceAll(n, "\\", "/")
	if strings.HasPrefix(n, "/") || (len(n) > 1 && n[1] == ':') {
		return "", reject("unsafe_entry", "the archive contains an absolute path")
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", reject("unsafe_entry", "the archive contains a path traversal entry")
		}
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", reject("unsafe_entry", "the archive contains an entry name with control characters")
		}
	}
	return path.Clean(n), nil
}

// SanitizeName makes an untrusted file name safe to store and display (base name, printable, ≤ 120 chars).
func SanitizeName(n string) string {
	n = strings.ReplaceAll(n, "\\", "/")
	n = path.Base(n)
	var b strings.Builder
	for _, r := range n {
		if r >= 0x20 && r != 0x7f && r != '<' && r != '>' && r != '"' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 120 {
		out = out[:120]
	}
	if out == "." || out == "/" {
		return ""
	}
	return out
}
