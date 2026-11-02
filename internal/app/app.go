// Package app assembles WagenPulse: configuration, storage, keys, services, background workers and the HTTP
// listeners. One process owns a data directory (ADR-0001).
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/config"
	"github.com/aurimasniekis/go-wagenpulse/internal/httpx"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/netguard"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// App is a running instance's object graph.
type App struct {
	Cfg            *config.Config
	Log            *slog.Logger
	Clock          clock.Clock
	Metrics        *metrics.Metrics
	Store          *store.Store
	Keyring        *keyring.Keyring
	Audit          *audit.Writer
	Settings       *settings.Service
	Auth           *auth.Service
	OIDC           *oidc.Service
	Guard          *netguard.Guard
	Refs           *secretref.Resolver
	ClientIP       *httpx.ClientIPResolver
	InstallationID string
	StartedAt      time.Time

	Domain *Domain

	workers []func(context.Context)
}

// Options adjust Open (tests).
type Options struct {
	Clock   clock.Clock
	Environ []string
	// HashParams overrides Argon2id parameters (tests only).
	HashParams *auth.Params
	// Attach lets an administrative command use the data directory while a server runs (store.Options.AllowAttach).
	Attach bool
}

// Open opens the data directory (taking its lock), loads the keyring and builds every service.
func Open(ctx context.Context, cfg *config.Config, log *slog.Logger, opts Options) (*App, error) {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	a := &App{Cfg: cfg, Log: log, Clock: opts.Clock, Metrics: metrics.New(), StartedAt: opts.Clock.Now()}
	info := buildinfo.Get()
	a.Metrics.SetBuildInfo(info.Version, info.GoVersion)
	s, err := store.Open(ctx, store.Options{DataDir: cfg.DataDir, AllowNetworkFS: cfg.Storage.AllowNetworkFS,
		PreMigrationSnapshots: cfg.Storage.PreMigrationSnapshots, Logger: log, Clock: opts.Clock, Metrics: a.Metrics,
		AllowAttach: opts.Attach})
	if err != nil {
		return nil, err
	}
	a.Store = s
	if err := a.init(ctx, opts); err != nil {
		_ = a.Close()
		return nil, err
	}
	return a, nil
}

// EnsureInstallation creates the installation singleton on first start and returns its ID.
func EnsureInstallation(ctx context.Context, s *store.Store) (string, error) {
	var id string
	err := s.W().TxNamed(ctx, "installation.ensure", func(tx *store.Tx) error {
		row, err := tx.Q.GetInstallation(ctx)
		if err == nil {
			id = row.InstallationID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id = ids.New(ids.Installation)
		return tx.Q.InsertInstallation(ctx, db.InsertInstallationParams{InstallationID: id, CreatedAt: tx.NowMicros(), UpdatedAt: tx.NowMicros()})
	})
	return id, err
}

func (a *App) init(ctx context.Context, opts Options) error {
	cfg := a.Cfg
	var err error
	if a.InstallationID, err = EnsureInstallation(ctx, a.Store); err != nil {
		return err
	}
	kek, err := keyring.LoadKEK(keyring.Source{Kind: cfg.KEK.Source, File: cfg.KEK.File, Production: cfg.IsProduction(),
		DataDir: cfg.DataDir, Environ: opts.Environ})
	if err != nil {
		return err
	}
	if a.Keyring, err = keyring.Load(ctx, a.Store, kek); err != nil {
		return err
	}
	a.Audit = audit.NewWriter(a.Keyring)
	a.Settings = settings.New(a.Store, a.Audit, cfg.Session.MaxIdle.D(), cfg.Session.MaxAbsolute.D())
	params := auth.DefaultParams
	if opts.HashParams != nil {
		params = *opts.HashParams
	}
	a.Auth = auth.New(a.Store, a.Keyring, a.Audit, a.Settings, auth.Options{Clock: a.Clock, Log: a.Log, Metrics: a.Metrics, Params: params,
		SetupTTL: cfg.Setup.TokenTTL.D(), FreeFailures: cfg.Login.FreeFailures, MaxDelay: cfg.Login.MaxDelay.D(), Decay: cfg.Login.Decay.D()})
	if a.Guard, err = NewGuard(cfg); err != nil {
		return err
	}
	a.Refs = &secretref.Resolver{EnvPrefix: config.SecretEnvPrefix, FileDirs: cfg.Secrets.FileDirs, Getenv: envLookup(opts.Environ)}
	a.OIDC = oidc.New(a.Store, a.Keyring, a.Audit, a.Auth, oidc.NewUpstream(a.Guard.Client(30*time.Second)), a.Refs, cfg.PublicOrigin(),
		a.Log, a.Clock.Now)
	a.OIDC.AllowHTTPIssuers = !cfg.IsProduction()
	if a.ClientIP, err = httpx.NewClientIPResolver(cfg.Proxy.TrustedProxies, nil); err != nil {
		return err
	}
	if err := a.initDomain(ctx); err != nil {
		return err
	}
	if !cfg.IsProduction() {
		a.Log.Warn("running in development mode: a development KEK and http://localhost are allowed; never use this mode in production")
	}
	return nil
}

// NewGuard builds the outbound connection guard (ADR-0019).
func NewGuard(cfg *config.Config) (*netguard.Guard, error) {
	g, err := netguard.NewWithOptions(netguard.Options{AllowCIDRs: cfg.Outbound.AllowCIDRs, DenyCIDRs: cfg.Outbound.DenyCIDRs,
		AllowPrivate: cfg.Outbound.AllowPrivate})
	if err != nil {
		return nil, err
	}
	if cfg.Outbound.CAFile != "" {
		pem, err := os.ReadFile(cfg.Outbound.CAFile)
		if err != nil {
			return nil, fmt.Errorf("outbound.ca_file: %w", err)
		}
		if err := g.AddRootCAs(pem); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func envLookup(environ []string) func(string) string {
	m := map[string]string{}
	for _, kv := range environ {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return func(k string) string { return m[k] }
}

// Ready reports whether the instance can serve: storage healthy and keyring loaded. Upstream outages (VW portal,
// NATS) never make the process unready (ADR-0012 of operations).
func (a *App) Ready(ctx context.Context) error {
	if a.Keyring == nil {
		return errors.New("keyring not loaded")
	}
	st := a.Store.Health().Status()
	if st.State != store.HealthOK {
		return fmt.Errorf("storage %s: %s", st.State, st.Reason)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := a.Store.R().GetInstallation(cctx)
	return err
}

// Close releases the data directory.
func (a *App) Close() error {
	if a.Store != nil {
		return a.Store.Close()
	}
	return nil
}

// component returns a component logger.
func (a *App) component(name string) *slog.Logger { return obs.Component(a.Log, name) }
