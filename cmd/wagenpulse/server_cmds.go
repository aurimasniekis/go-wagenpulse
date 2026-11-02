// Adapted from Porch cmd/porch/server_cmds.go (Apache-2.0).

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-wagenpulse/internal/api"
	"github.com/aurimasniekis/go-wagenpulse/internal/app"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/config"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// configFlag registers -config (default $WAGENPULSE_CONFIG).
func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", os.Getenv("WAGENPULSE_CONFIG"), "path to wagenpulse.yaml (default $WAGENPULSE_CONFIG)")
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path, os.Environ())
}

func newLogger(w io.Writer, cfg *config.Config) *slog.Logger {
	l, _ := obs.NewLogger(w, cfg.Log.Level, cfg.Log.Format)
	return l
}

func openApp(ctx context.Context, cfgPath string, stderr io.Writer) (*app.App, error) {
	return openAppMode(ctx, cfgPath, stderr, false)
}

// openAppOnline opens the data directory for a short administrative command that also works while the server runs
// (ADR-0027): setup tokens, password resets, audit verification and key status. Commands that change in-memory server
// state (key rotation, migrations, restores) use openApp and need the server stopped.
func openAppOnline(ctx context.Context, cfgPath string, stderr io.Writer) (*app.App, error) {
	return openAppMode(ctx, cfgPath, stderr, true)
}

func openAppMode(ctx context.Context, cfgPath string, stderr io.Writer, attach bool) (*app.App, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	log := newLogger(stderr, cfg)
	slog.SetDefault(log)
	return app.Open(ctx, cfg, log, app.Options{Attach: attach})
}

func runServe(ctx context.Context, args []string, _, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	ls, err := a.Listen(ctx)
	if err != nil {
		return err
	}
	return a.Serve(ctx, ls)
}

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("migrate", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, store.Options{DataDir: cfg.DataDir, AllowNetworkFS: cfg.Storage.AllowNetworkFS,
		PreMigrationSnapshots: cfg.Storage.PreMigrationSnapshots, Logger: newLogger(stderr, cfg)})
	if err != nil {
		return err
	}
	defer s.Close()
	cur, latest, err := store.SchemaVersions(ctx, s.ReadDB(), store.EmbeddedMigrations())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "schema version %d (latest %d)\n", cur, latest)
	return err
}

func runConfigCheck(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("config check", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(stdout)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stderr, "configuration OK")
	return err
}

func runConfigEnv(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("config env", stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	for _, k := range config.EnvKeys() {
		if _, err := fmt.Fprintln(stdout, k); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "%s (base64 KEK when kek.source=env)\n%s (config file path)\n%s* (values for env: secret references)\n",
		config.EnvPrefix+"KEK", config.EnvPrefix+"CONFIG", config.SecretEnvPrefix)
	return err
}

func runOpenAPI(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("openapi", stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	b, err := app.OpenAPIDocument() //nolint:contextcheck // only builds the document
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(out, '\n'))
	return err
}

func runHealthcheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("healthcheck", stderr)
	url := fs.String("url", "http://127.0.0.1:9464/readyz", "readiness URL on the management listener")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return &exitError{code: 1, err: fmt.Errorf("%s: %s", resp.Status, body)}
	}
	_, err = stdout.Write(body)
	return err
}

func runKeysInit(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys init", stderr)
	out := fs.String("out", "", "path of the new KEK file (must not exist; keep it outside data_dir)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return &exitError{code: 2, err: errors.New("--out is required")}
	}
	k, err := keyring.GenerateKEK()
	if err != nil {
		return err
	}
	if err := keyring.WriteKEKFile(*out, k); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote KEK %s (id %s). Store a copy offline: losing it makes every stored secret unreadable.\n", *out, keyring.KEKID(k))
	return err
}

func runKeysStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys status", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openAppOnline(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"kek_id": a.Keyring.KEKID(), "purposes": a.Keyring.Status()})
}

func runKeysRotate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys rotate", stderr)
	cfgPath := configFlag(fs)
	purpose := fs.String("purpose", "", "purpose to rotate: field or token (rotating token signs everyone out)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	v, err := a.Keyring.Rotate(ctx, a.Store, keyring.Purpose(*purpose))
	if err != nil {
		return err
	}
	n, err := a.Keyring.ResealColumns(ctx, a.Store, keyring.Columns, 1<<20)
	if err != nil {
		return err
	}
	err = a.Store.W().Tx(ctx, func(tx *store.Tx) error {
		_, err := a.Audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "keyring.rotated", Target: audit.Target{Type: "keyring", ID: *purpose},
			Details: map[string]any{"version": v, "resealed": n}})
		return err
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s key rotated to v%d; %d sealed values re-sealed\n", *purpose, v, n)
	return err
}

func runKeysRetire(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keys retire", stderr)
	cfgPath := configFlag(fs)
	purpose := fs.String("purpose", "", "purpose")
	version := fs.Uint("version", 0, "decrypt-only version to retire")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openApp(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	p := keyring.Purpose(*purpose)
	if _, err := a.Keyring.ResealColumns(ctx, a.Store, keyring.Columns, 1<<20); err != nil {
		return err
	}
	inUse, err := keyring.VersionInUse(ctx, a.Store, keyring.Columns, p, uint32(*version)) //nolint:gosec // small version
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("%s v%d is still used by a sealed column; not retired", p, *version)
	}
	if err := a.Keyring.Retire(ctx, a.Store, p, uint32(*version)); err != nil { //nolint:gosec // small version
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s v%d retired\n", p, *version)
	return err
}

func runSetupToken(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("admin setup-token", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openAppOnline(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	tok, exp, err := a.Auth.IssueSetupToken(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Setup token (single use, expires %s):\n\n  %s\n\nOpen %s/setup and paste it to create the root user.\n",
		exp.UTC().Format(time.RFC3339), tok, a.Cfg.PublicOrigin())
	return err
}

func runResetPassword(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("admin reset-password", stderr)
	cfgPath := configFlag(fs)
	user := fs.String("user", "", "user ID or username")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *user == "" {
		return &exitError{code: 2, err: errors.New("--user is required")}
	}
	a, err := openAppOnline(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	u, err := a.Auth.FindUser(ctx, *user)
	if err != nil {
		return err
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	pw := base64.RawURLEncoding.EncodeToString(b)
	if err := a.Auth.BreakGlassResetPassword(ctx, u.ID, pw); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "New password for %s (shown once; change it after signing in):\n\n  %s\n\n"+
		"Local login was enabled for this user and in system settings; all of the user's sessions were signed out.\n", u.Username, pw)
	return err
}

func runAuditVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("audit verify", stderr)
	cfgPath := configFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	a, err := openAppOnline(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	res, err := audit.Verify(ctx, a.Store.R(), a.AuditAnchor(ctx))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return err
	}
	if !res.OK {
		return &exitError{code: 3, err: errors.New("audit chain verification FAILED")}
	}
	return nil
}

var (
	_ = api.Prefix
	_ = auth.RoleRoot
	_ = buildinfo.Get
)
