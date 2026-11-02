// Package backup creates, verifies and restores WagenPulse backups (ADR-0025).
//
// A backup is a directory holding a consistent copy of the database (SQLite VACUUM INTO, safe while the server runs),
// the raw delivery blobs that copy references, and manifest.json with the SHA-256 and size of every file. The KEK is
// never included: sealed columns in the backup can only be opened with the KEK of the installation that wrote it.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// FormatVersion is the manifest format this binary writes and reads.
const FormatVersion = 1

// ManifestFile is the manifest's name inside a backup directory.
const ManifestFile = "manifest.json"

const blobsDir = "blobs"

// File is one file of a backup.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a backup.
type Manifest struct {
	Format         int       `json:"format"`
	CreatedAt      time.Time `json:"created_at"`
	AppVersion     string    `json:"app_version"`
	SchemaVersion  int64     `json:"schema_version"`
	InstallationID string    `json:"installation_id"`
	Files          []File    `json:"files"`
	// MissingBlobs are raw deliveries the database copy marks present whose blob was gone when it was copied (for
	// example removed by retention in between). Their metadata and observations are intact; only reprocessing them is
	// impossible.
	MissingBlobs []string `json:"missing_blobs,omitempty"`
}

// Create writes a backup of the open store into out, which must not exist. It works while the server runs.
func Create(ctx context.Context, s *store.Store, out, appVersion string) (*Manifest, error) {
	out, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(out); err == nil {
		return nil, fmt.Errorf("backup target %s already exists", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if inside(out, s.Dir()) {
		return nil, fmt.Errorf("backup target %s is inside the data directory; write backups to separate storage", out)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(out)
		}
	}()
	dbPath := filepath.Join(out, store.DBFile)
	if err := s.Snapshot(ctx, dbPath); err != nil {
		return nil, err
	}
	m := &Manifest{Format: FormatVersion, CreatedAt: time.Now().UTC(), AppVersion: appVersion}
	sums, err := inspect(ctx, dbPath, m)
	if err != nil {
		return nil, err
	}
	// Blobs are written before the row that references them commits, so every blob the snapshot references existed
	// when the snapshot was taken; one can only be missing if retention removed it afterwards.
	for _, sum := range sums {
		rel := blobRel(sum)
		err := copyFile(filepath.Join(s.Dir(), blobsDir, rel), filepath.Join(out, blobsDir, rel), sum)
		if errors.Is(err, os.ErrNotExist) {
			m.MissingBlobs = append(m.MissingBlobs, sum)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	if err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, path)
		f, err := describe(path, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		m.Files = append(m.Files, f)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := writeManifest(out, m); err != nil {
		return nil, err
	}
	if err := syncTree(out); err != nil {
		return nil, err
	}
	ok = true
	return m, nil
}

// Report is the result of verifying a backup.
type Report struct {
	Manifest *Manifest
	// LatestSchema is the newest schema version this binary knows; a newer backup cannot be restored by it.
	LatestSchema int64
}

// Verify checks a backup directory: manifest format, every file's size and SHA-256, no unlisted files, the database
// integrity and schema version, and that each blob's content matches its name.
func Verify(ctx context.Context, dir string) (*Report, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile)) //nolint:gosec // operator-supplied backup path
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Format != FormatVersion {
		return nil, fmt.Errorf("unsupported backup format %d (this binary reads %d)", m.Format, FormatVersion)
	}
	listed := map[string]bool{}
	hasDB := false
	for _, f := range m.Files {
		if !validRel(f.Path) {
			return nil, fmt.Errorf("manifest lists an unsafe path %q", f.Path)
		}
		listed[f.Path] = true
		got, err := describe(filepath.Join(dir, filepath.FromSlash(f.Path)), f.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if got.Size != f.Size || got.SHA256 != f.SHA256 {
			return nil, fmt.Errorf("%s: content does not match the manifest (corrupted or modified)", f.Path)
		}
		if f.Path == store.DBFile {
			hasDB = true
		} else if strings.HasPrefix(f.Path, blobsDir+"/sha256/") && filepath.Base(f.Path) != f.SHA256 {
			return nil, fmt.Errorf("%s: blob content does not match its name", f.Path)
		}
	}
	if !hasDB {
		return nil, errors.New("backup has no database file")
	}
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel != ManifestFile && !listed[rel] {
			return fmt.Errorf("unlisted file %s in backup", rel)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	d, err := store.OpenFileReadOnly(filepath.Join(dir, store.DBFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := store.FileIntegrityCheck(ctx, d); err != nil {
		return nil, err
	}
	cur, latest, err := store.FileSchemaVersion(ctx, d)
	if err != nil {
		return nil, err
	}
	if cur != m.SchemaVersion {
		return nil, fmt.Errorf("database schema %d differs from the manifest (%d)", cur, m.SchemaVersion)
	}
	return &Report{Manifest: &m, LatestSchema: latest}, nil
}

// Restore verifies the backup in from and copies it into dataDir, which must be empty or not exist. It holds the
// data-directory lock while copying, so a server cannot start on a half-restored directory.
func Restore(ctx context.Context, from, dataDir string) (*Report, error) {
	rep, err := Verify(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("backup failed verification, nothing restored: %w", err)
	}
	if rep.Manifest.SchemaVersion > rep.LatestSchema {
		return nil, fmt.Errorf("backup schema %d is newer than this binary supports (%d); restore with a newer wagenpulse",
			rep.Manifest.SchemaVersion, rep.LatestSchema)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := store.PrepareDataDir(dataDir); err != nil {
		return nil, err
	}
	lock, err := store.AcquireLock(filepath.Join(dataDir, store.LockFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == store.LockFile {
			continue
		}
		if e.Name() == store.BackupsDir && e.IsDir() {
			if sub, _ := os.ReadDir(filepath.Join(dataDir, e.Name())); len(sub) == 0 {
				continue
			}
		}
		return nil, fmt.Errorf("data directory %s is not empty (found %s); restore only into an empty directory", dataDir, e.Name())
	}
	for _, f := range rep.Manifest.Files {
		src := filepath.Join(from, filepath.FromSlash(f.Path))
		dst := filepath.Join(dataDir, filepath.FromSlash(f.Path))
		if err := copyFile(src, dst, f.SHA256); err != nil {
			return nil, fmt.Errorf("restore %s: %w", f.Path, err)
		}
	}
	if err := syncTree(dataDir); err != nil {
		return nil, err
	}
	return rep, nil
}

// inspect reads the schema version, installation ID and the referenced blob hashes from a database copy.
func inspect(ctx context.Context, path string, m *Manifest) ([]string, error) {
	d, err := store.OpenFileReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := store.FileIntegrityCheck(ctx, d); err != nil {
		return nil, err
	}
	if m.SchemaVersion, _, err = store.FileSchemaVersion(ctx, d); err != nil {
		return nil, err
	}
	if err := d.QueryRowContext(ctx, `SELECT installation_id FROM installation LIMIT 1`).Scan(&m.InstallationID); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT sha256 FROM raw_deliveries WHERE blob_present = 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var sums []string
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		sums = append(sums, hex.EncodeToString(b))
	}
	slices.Sort(sums)
	return sums, rows.Err()
}

func blobRel(sum string) string { return filepath.Join("sha256", sum[:2], sum) }

// copyFile copies src to dst (0600, parents 0700) and, when wantSHA is set, verifies the copied content.
func copyFile(src, dst, wantSHA string) error {
	in, err := os.Open(src) //nolint:gosec // paths derived from the data directory or a verified manifest
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // see above
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if wantSHA != "" && hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return fmt.Errorf("%s: content hash mismatch while copying", src)
	}
	return nil
}

func describe(path, rel string) (File, error) {
	f, err := os.Open(path) //nolint:gosec // path inside a backup directory
	if err != nil {
		return File{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return File{}, err
	}
	return File{Path: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func writeManifest(dir string, m *Manifest) error {
	slices.SortFunc(m.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestFile), append(b, '\n'), 0o600)
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return store.FsyncDir(path)
	})
}

func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return p == store.DBFile || strings.HasPrefix(p, blobsDir+"/sha256/")
}

func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
