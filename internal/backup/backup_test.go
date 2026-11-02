package backup

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// fixture opens a fresh store with an installation row and two synthetic raw deliveries whose blobs exist.
func fixture(t *testing.T) (*store.Store, [][]byte) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, store.Options{DataDir: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	blobs, err := ingest.NewBlobs(filepath.Join(s.Dir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	var sums [][]byte
	for i, body := range []string{`{"synthetic":1}`, `{"synthetic":2}`} {
		sum, size, err := blobs.Put(strings.NewReader(body), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, sum)
		if err := s.W().Tx(ctx, func(tx *store.Tx) error {
			if i == 0 {
				if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO installation (id, installation_id, created_at, updated_at) VALUES (1, 'ins_test', 1, 1)`); err != nil {
					return err
				}
			}
			_, err := tx.SQL.ExecContext(ctx, `INSERT INTO raw_deliveries (id, sha256, size, origin, received_at, status) VALUES (?, ?, ?, 'upload', 1, 'stored')`,
				"rd_"+string(rune('a'+i)), sum, size)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s, sums
}

func TestCreateVerifyRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, sums := fixture(t)
	out := filepath.Join(t.TempDir(), "backup-1")
	m, err := Create(ctx, s, out, "test")
	if err != nil {
		t.Fatal(err)
	}
	if m.InstallationID != "ins_test" || m.SchemaVersion == 0 || len(m.Files) != 3 || len(m.MissingBlobs) != 0 {
		t.Fatalf("manifest: %+v", m)
	}
	if _, err := Verify(ctx, out); err != nil {
		t.Fatalf("verify: %v", err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(ctx, out, target); err != nil {
		t.Fatalf("restore: %v", err)
	}
	r, err := store.Open(ctx, store.Options{DataDir: target})
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer func() { _ = r.Close() }()
	var n int
	if err := r.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM raw_deliveries`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("restored deliveries=%d err=%v", n, err)
	}
	for _, sum := range sums {
		p := filepath.Join(target, "blobs", blobRel(hexs(sum)))
		data, err := os.ReadFile(p) //nolint:gosec // test path
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256.Sum256(data); string(got[:]) != string(sum) {
			t.Fatal("restored blob content differs")
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("blob mode %v", fi.Mode().Perm())
		}
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	ctx := context.Background()
	s, sums := fixture(t)
	cases := map[string]func(dir string){
		"modified blob": func(dir string) {
			p := filepath.Join(dir, "blobs", blobRel(hexs(sums[0])))
			_ = os.Chmod(p, 0o600)
			_ = os.WriteFile(p, []byte(`{"tampered":true}`), 0o600)
		},
		"removed database": func(dir string) { _ = os.Remove(filepath.Join(dir, store.DBFile)) },
		"unlisted file":    func(dir string) { _ = os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o600) },
		"bad manifest":     func(dir string) { _ = os.WriteFile(filepath.Join(dir, ManifestFile), []byte("{"), 0o600) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "b")
			if _, err := Create(ctx, s, out, "test"); err != nil {
				t.Fatal(err)
			}
			mutate(out)
			if _, err := Verify(ctx, out); err == nil {
				t.Fatal("expected verification to fail")
			}
			if _, err := Restore(ctx, out, filepath.Join(t.TempDir(), "r")); err == nil {
				t.Fatal("restore must refuse a backup that fails verification")
			}
		})
	}
}

func TestMissingBlobIsRecorded(t *testing.T) {
	ctx := context.Background()
	s, sums := fixture(t)
	if err := os.Remove(filepath.Join(s.Dir(), "blobs", blobRel(hexs(sums[1])))); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b")
	m, err := Create(ctx, s, out, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.MissingBlobs) != 1 || m.MissingBlobs[0] != hexs(sums[1]) {
		t.Fatalf("missing=%v", m.MissingBlobs)
	}
	if _, err := Verify(ctx, out); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesNonEmptyOrOwnedTarget(t *testing.T) {
	ctx := context.Background()
	s, _ := fixture(t)
	out := filepath.Join(t.TempDir(), "b")
	if _, err := Create(ctx, s, out, "test"); err != nil {
		t.Fatal(err)
	}
	// The live data directory: owned by the open store and not empty.
	if _, err := Restore(ctx, out, s.Dir()); err == nil {
		t.Fatal("restore into a live data directory must fail")
	}
	busy := filepath.Join(t.TempDir(), "busy")
	if err := os.MkdirAll(busy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(busy, "something"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, out, busy); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected not-empty refusal, got %v", err)
	}
}

func TestCreateRefusesTargetsInsideDataDirOrExisting(t *testing.T) {
	ctx := context.Background()
	s, _ := fixture(t)
	if _, err := Create(ctx, s, filepath.Join(s.Dir(), "backups", "x"), "test"); err == nil {
		t.Fatal("backup inside the data directory must be refused")
	}
	existing := t.TempDir()
	if _, err := Create(ctx, s, existing, "test"); err == nil {
		t.Fatal("existing target must be refused")
	}
}

// ADR-0027: a backup can be taken by a second process while the server owns the data directory.
func TestCreateWhileServerOwnsDirectory(t *testing.T) {
	ctx := context.Background()
	owner, _ := fixture(t)
	att, err := store.Open(ctx, store.Options{DataDir: owner.Dir(), AllowAttach: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = att.Close() }()
	if !att.Attached() {
		t.Fatal("expected attached store")
	}
	out := filepath.Join(t.TempDir(), "b")
	if _, err := Create(ctx, att, out, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, out); err != nil {
		t.Fatal(err)
	}
}

func hexs(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
