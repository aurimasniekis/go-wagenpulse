package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "wagenpulse.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProductionRequiresPublicURLAndKEK(t *testing.T) {
	_, err := Load("", nil)
	if err == nil || !strings.Contains(err.Error(), "public_url is required") || !strings.Contains(err.Error(), "kek.file is required") {
		t.Fatalf("got %v", err)
	}
}

func TestDevelopmentMinimal(t *testing.T) {
	p := writeFile(t, "mode: development\ndata_dir: /tmp/wagenpulse-dev\npublic_url: http://localhost:8080\n")
	c, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Listen != "127.0.0.1:8080" || c.Management.Listen != "127.0.0.1:9464" || c.Ingest.MaxUploadBytes != 50<<20 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.SecureCookies() {
		t.Fatal("http public_url must not use secure cookies")
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	p := writeFile(t, "mode: development\npublic_url: http://localhost:8080\nhttp:\n  listn: x\n")
	if _, err := Load(p, nil); err == nil || !strings.Contains(err.Error(), "listn") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvOverridesAndUnknownEnv(t *testing.T) {
	env := []string{"WAGENPULSE_MODE=development", "WAGENPULSE_PUBLIC_URL=http://localhost:9000", "WAGENPULSE_DATA_DIR=/tmp/x",
		"WAGENPULSE_PROXY_TRUSTED_PROXIES=10.0.0.0/8, 192.168.0.0/16", "WAGENPULSE_SESSION_MAX_IDLE=1h"}
	c, err := Load("", env)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Proxy.TrustedProxies) != 2 || c.Session.MaxIdle.D() != time.Hour {
		t.Fatalf("%+v", c.Proxy)
	}
	if _, err := Load("", append(env, "WAGENPULSE_NOPE=1")); err == nil || !strings.Contains(err.Error(), "WAGENPULSE_NOPE") {
		t.Fatalf("unknown env: %v", err)
	}
	// Non-config variables with the prefix are tolerated.
	if _, err := Load("", append(env, "WAGENPULSE_KEK=abc", "WAGENPULSE_CONFIG=/x")); err != nil {
		t.Fatalf("non-config env: %v", err)
	}
}

func TestProductionRestrictions(t *testing.T) {
	p := writeFile(t, `public_url: http://wagen.example.com
data_dir: /var/lib/wagenpulse
kek:
  file: /var/lib/wagenpulse/kek
storage:
  allow_network_fs: true
`)
	_, err := Load(p, nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"public_url must use https", "kek.file must be stored outside data_dir", "allow_network_fs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestIngestLimitsValidated(t *testing.T) {
	env := []string{"WAGENPULSE_MODE=development", "WAGENPULSE_PUBLIC_URL=http://localhost:9000", "WAGENPULSE_DATA_DIR=/tmp/x",
		"WAGENPULSE_INGEST_MAX_RATIO=1", "WAGENPULSE_INGEST_MAX_EXTRACTED_BYTES=10"}
	_, err := Load("", env)
	if err == nil || !strings.Contains(err.Error(), "max_ratio") || !strings.Contains(err.Error(), "max_extracted_bytes") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvKeysDocumented(t *testing.T) {
	keys := EnvKeys()
	if len(keys) < 20 {
		t.Fatalf("too few keys: %d", len(keys))
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, EnvPrefix) {
			t.Fatalf("bad key %s", k)
		}
	}
}
