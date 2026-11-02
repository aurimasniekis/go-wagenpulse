package api

import (
	"context"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/adapters"
	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/routes"
	"github.com/aurimasniekis/go-wagenpulse/internal/rules"
	"github.com/aurimasniekis/go-wagenpulse/internal/sources"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// Domain bundles the domain services the API exposes.
type Domain struct {
	Vehicles   *vehicles.Service
	Telemetry  *telemetry.Reader
	Dictionary *dictionary.Service
	Sources    *sources.Service
	Collector  *sources.Collector
	Ingest     *ingest.Service
	Rules      *rules.Service
	Events     *events.Service
	Routes     *routes.Service
	Adapters   *adapters.Service
	Dispatch   *adapters.Manager
	// Purge runs retention now (set by the app).
	RunRetention func(ctx context.Context) (any, error)
	// Backup creates a backup now (set by the app).
	Backup func(ctx context.Context) (any, error)
}

// vehicle resolves a vehicle by ID or slug and enforces API-token vehicle restrictions.
func (d *Deps) vehicle(ctx context.Context, idOrSlug string) (vehicles.Vehicle, error) {
	v, err := d.D.Vehicles.Get(ctx, idOrSlug)
	if err != nil {
		return v, err
	}
	if !Principal(ctx).VehicleAllowed(v.ID) {
		return v, vehicles.ErrNotFound
	}
	return v, nil
}

// restrictedNums returns the vehicle numbers an API token is restricted to (nil = unrestricted).
func (d *Deps) restrictedNums(ctx context.Context) ([]int64, error) {
	p := Principal(ctx)
	if !p.VehicleRestricted() {
		return nil, nil
	}
	out := []int64{}
	for _, id := range p.Token.VehicleIDs {
		if v, err := d.D.Vehicles.Get(ctx, id); err == nil {
			out = append(out, v.Num)
		}
	}
	if len(out) == 0 {
		out = []int64{-1}
	}
	return out, nil
}

// staleAfter returns the effective stale threshold of a vehicle.
func (d *Deps) staleAfter(ctx context.Context, v vehicles.Vehicle) time.Duration {
	if v.StaleAfterMinutes != nil {
		return time.Duration(*v.StaleAfterMinutes) * time.Minute
	}
	sys, err := d.Settings.System(ctx)
	if err != nil {
		return 2 * time.Hour
	}
	return time.Duration(sys.Value.StaleAfterMinutes) * time.Minute
}

func parseRange(from, to time.Time, defaultSpan time.Duration, now time.Time) (time.Time, time.Time) {
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-defaultSpan)
	}
	return from, to
}

func requireConfirm(ok bool, what string) error {
	if !ok {
		return problem(http.StatusBadRequest, apperr.CodeInvalid, "confirm=true is required: "+what, nil)
	}
	return nil
}

var _ = auth.RoleRoot

func listForOverview(ctx context.Context) vehicles.ListFilter {
	f := vehicles.ListFilter{Limit: 1000}
	if p := Principal(ctx); p.VehicleRestricted() {
		f.OnlyIDs = p.Token.VehicleIDs
	}
	return f
}
