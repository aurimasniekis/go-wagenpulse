package secretref

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAndResolve(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, "nats.creds"), []byte("creds"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "kek")
	if err := os.WriteFile(outside, []byte("kek"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(allowed, "link")); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{EnvPrefix: "WAGENPULSE_SECRET_", FileDirs: []string{allowed}, Getenv: func(k string) string {
		if k == "WAGENPULSE_SECRET_OIDC" {
			return "s3cr3t"
		}
		return ""
	}}
	for _, bad := range []string{"plain", "env:HOME", "env:WAGENPULSE_KEK", "env:WAGENPULSE_SECRET_", "file:relative",
		"file:" + outside, "file:" + allowed + "/../kek", "vault:x"} {
		if err := r.Validate(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if b, err := r.Resolve("env:WAGENPULSE_SECRET_OIDC"); err != nil || string(b) != "s3cr3t" {
		t.Fatalf("env: %q %v", b, err)
	}
	if _, err := r.Resolve("env:WAGENPULSE_SECRET_MISSING"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("missing env: %v", err)
	}
	if b, err := r.Resolve("file:" + filepath.Join(allowed, "nats.creds")); err != nil || string(b) != "creds" {
		t.Fatalf("file: %q %v", b, err)
	}
	if _, err := r.Resolve("file:" + filepath.Join(allowed, "link")); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("symlink escape: %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint([]byte("k"), []byte("secret"))
	if len(a) != 12 || a == Fingerprint([]byte("k2"), []byte("secret")) || a != Fingerprint([]byte("k"), []byte("secret")) {
		t.Fatal(a)
	}
}
