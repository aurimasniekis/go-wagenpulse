package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/api"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// Domain bundles the telemetry, ingestion and event services (wired by initDomain).
type Domain struct{}

func (a *App) initDomain(_ context.Context) error {
	a.Domain = &Domain{}
	return nil
}

// domainRegistrations returns the API registrations of domain services.
func (a *App) domainRegistrations() []func(huma.API, *api.Deps) { return nil }

// registerUploads mounts streaming upload endpoints that are not Huma operations.
func (a *App) registerUploads(_ *http.ServeMux, _ *api.Deps) {}

// backgroundWorkers returns every long-running worker.
func (a *App) backgroundWorkers() []func(context.Context) {
	ws := []func(context.Context){
		func(ctx context.Context) {
			periodic(ctx, a.component("auth"), "token_usage_flush", time.Minute, func(ctx context.Context) error {
				return a.Auth.FlushTokenUsage(ctx)
			})
		},
		func(ctx context.Context) {
			periodic(ctx, a.component("maintenance"), "maintenance", 5*time.Minute, a.maintain)
		},
	}
	return append(ws, a.workers...)
}

// maintain purges expired authentication state and idempotency records.
func (a *App) maintain(ctx context.Context) error {
	now := a.Clock.Now()
	return a.Store.W().TxNamed(ctx, "maintenance.purge", func(tx *store.Tx) error {
		n := now.UnixMicro()
		day := now.Add(-24 * time.Hour).UnixMicro()
		if _, err := tx.Q.PurgeSessions(ctx, db.PurgeSessionsParams{AbsoluteExpiresAt: n, RevokedAt: sql.NullInt64{Int64: day, Valid: true}, IdleExpiresAt: n}); err != nil {
			return err
		}
		if _, err := tx.Q.PurgeChallenges(ctx, n); err != nil {
			return err
		}
		if _, err := tx.Q.PurgeOIDCStates(ctx, n); err != nil {
			return err
		}
		if _, err := tx.Q.PurgeThrottle(ctx, day); err != nil {
			return err
		}
		_, err := tx.Q.PurgeIdempotencyKeys(ctx, n)
		return err
	})
}

// periodic runs fn every interval until ctx is canceled; panics and errors are logged.
func periodic(ctx context.Context, log interface {
	WarnContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
}, name string, interval time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	run := func() {
		defer func() {
			if p := recover(); p != nil {
				log.ErrorContext(ctx, "periodic task panicked", "task", name, "panic", p)
			}
		}()
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "periodic task failed", "task", name, obs.Err(err))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// OpenAPIDocument returns the full API document including domain operations (the openapi command, drift checks).
func OpenAPIDocument() ([]byte, error) {
	a := &App{Domain: &Domain{}}
	return api.OpenAPI(buildinfoVersion(), a.domainRegistrations()...)
}

// AuditAnchor returns the audit chain anchor left by retention purges (nil when nothing was purged).
func (a *App) AuditAnchor(ctx context.Context) *audit.Anchor {
	row, err := a.Store.R().GetSetting(ctx, audit.AnchorSettingKey)
	if err != nil {
		return nil
	}
	var an audit.Anchor
	if json.Unmarshal([]byte(row.Value), &an) != nil {
		return nil
	}
	return &an
}

// buildinfoVersion is the API document version: the API version, not the binary version, so the committed document
// does not change on every build.
func buildinfoVersion() string { return api.APIVersion }
