// Package routes binds events to adapter instances (ADR-0015): a route selects events by type, rule and vehicle and
// sends them to one adapter. An event matching several routes to the same adapter is delivered once.
package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Route is the API view.
type Route struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	AdapterID       string    `json:"adapter_id"`
	EventTypes      []string  `json:"event_types" doc:"Event types to deliver (empty = all)."`
	RuleIDs         []string  `json:"rule_ids" doc:"Rules whose events are delivered (empty = all rules)."`
	VehicleIDs      []string  `json:"vehicle_ids" doc:"Vehicles whose events are delivered (empty = all)."`
	IncludeBackfill bool      `json:"include_backfill" doc:"Also deliver events created from historical imports and reprocessing (default false, ADR-0017)."`
	Revision        int64     `json:"revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Input creates or replaces a route.
type Input struct {
	Name            string
	Enabled         bool
	AdapterID       string
	EventTypes      []string
	RuleIDs         []string
	VehicleIDs      []string
	IncludeBackfill bool
}

// Service manages routes.
type Service struct {
	s     *store.Store
	audit *audit.Writer
}

// NewService returns a service.
func NewService(s *store.Store, a *audit.Writer) *Service { return &Service{s: s, audit: a} }

func sortedUnique(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (in *Input) validate() error {
	v := &apperr.Validation{}
	in.Name = strings.TrimSpace(in.Name)
	if l := len([]rune(in.Name)); l == 0 || l > 100 {
		v.Add("name", "must be 1..100 characters")
	}
	if !ids.Valid(ids.Adapter, in.AdapterID) {
		v.Add("adapter_id", "must be an adapter id")
	}
	in.EventTypes, in.RuleIDs, in.VehicleIDs = sortedUnique(in.EventTypes), sortedUnique(in.RuleIDs), sortedUnique(in.VehicleIDs)
	for _, t := range in.EventTypes {
		if !events.ValidType(t) {
			v.Add("event_types", "unknown event type %q", t)
		}
	}
	for _, r := range in.RuleIDs {
		if !ids.Valid(ids.Rule, r) {
			v.Add("rule_ids", "invalid rule id %q", r)
		}
	}
	for _, id := range in.VehicleIDs {
		if !ids.Valid(ids.Vehicle, id) {
			v.Add("vehicle_ids", "invalid vehicle id %q", id)
		}
	}
	if len(in.RuleIDs) > 200 || len(in.VehicleIDs) > 200 {
		v.Add("rule_ids", "at most 200 rules and 200 vehicles per route")
	}
	return v.Err()
}

// ErrNotFound is returned for unknown routes.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "route not found")

// Get returns a route.
func (svc *Service) Get(ctx context.Context, id string) (Route, error) {
	var r Route
	var enabled, inc int
	var types string
	var created, updated int64
	err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT id, name, enabled, adapter_id, event_types_json, include_backfill, revision, created_at, updated_at
		FROM routes WHERE id = ?`, id).Scan(&r.ID, &r.Name, &enabled, &r.AdapterID, &types, &inc, &r.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Enabled, r.IncludeBackfill = enabled == 1, inc == 1
	r.CreatedAt, r.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	r.EventTypes = []string{}
	_ = json.Unmarshal([]byte(types), &r.EventTypes)
	if r.RuleIDs, err = svc.strings(ctx, `SELECT rule_id FROM route_rules WHERE route_id = ? ORDER BY rule_id`, id); err != nil {
		return r, err
	}
	r.VehicleIDs, err = svc.strings(ctx, `SELECT v.id FROM route_vehicles rv JOIN vehicles v ON v.num = rv.vehicle_num WHERE rv.route_id = ? ORDER BY v.id`, id)
	return r, err
}

func (svc *Service) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// List returns routes, optionally for one adapter.
func (svc *Service) List(ctx context.Context, adapterID string) ([]Route, error) {
	q := `SELECT id FROM routes`
	var args []any
	if adapterID != "" {
		q += ` WHERE adapter_id = ?`
		args = append(args, adapterID)
	}
	idsList, err := svc.strings(ctx, q+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(idsList))
	for _, id := range idsList {
		r, err := svc.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (svc *Service) write(ctx context.Context, tx *store.Tx, id string, in Input) error {
	var n int
	if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM adapters WHERE id = ?`, in.AdapterID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return apperr.New(apperr.CodeInvalid, "adapter_id: unknown adapter %s", in.AdapterID)
	}
	for _, q := range []string{`DELETE FROM route_rules WHERE route_id = ?`, `DELETE FROM route_vehicles WHERE route_id = ?`} {
		if _, err := tx.SQL.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	for _, r := range in.RuleIDs {
		if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE id = ? AND deleted_at IS NULL`, r).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeInvalid, "rule_ids: unknown rule %s", r)
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO route_rules (route_id, rule_id) VALUES (?, ?)`, id, r); err != nil {
			return err
		}
	}
	for _, v := range in.VehicleIDs {
		var num int64
		if err := tx.SQL.QueryRowContext(ctx, `SELECT num FROM vehicles WHERE id = ?`, v).Scan(&num); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.New(apperr.CodeInvalid, "vehicle_ids: unknown vehicle %s", v)
			}
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO route_vehicles (route_id, vehicle_num) VALUES (?, ?)`, id, num); err != nil {
			return err
		}
	}
	return nil
}

// Create adds a route.
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in Input) (Route, error) {
	if err := in.validate(); err != nil {
		return Route{}, err
	}
	id := ids.New(ids.Route)
	types, _ := json.Marshal(in.EventTypes)
	err := svc.s.W().TxNamed(ctx, "routes.create", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO routes (id, name, enabled, adapter_id, event_types_json, include_backfill, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, b2i(in.Enabled), in.AdapterID, string(types), b2i(in.IncludeBackfill), tx.NowMicros(), tx.NowMicros()); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return apperr.New(apperr.CodeInvalid, "adapter_id: unknown adapter %s", in.AdapterID)
			}
			return err
		}
		if err := svc.write(ctx, tx, id, in); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "route.created", Target: audit.Target{Type: "route", ID: id},
			Details: map[string]any{"adapter_id": in.AdapterID, "include_backfill": in.IncludeBackfill, "enabled": in.Enabled}})
		return err
	})
	if err != nil {
		return Route{}, err
	}
	return svc.Get(ctx, id)
}

// Update replaces a route.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, id string, ifRevision int64, in Input) (Route, error) {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return Route{}, err
	}
	if err := in.validate(); err != nil {
		return Route{}, err
	}
	types, _ := json.Marshal(in.EventTypes)
	err = svc.s.W().TxNamed(ctx, "routes.update", func(tx *store.Tx) error {
		rev := cur.Revision
		if ifRevision != 0 {
			rev = ifRevision
		}
		r, err := tx.SQL.ExecContext(ctx, `UPDATE routes SET name = ?, enabled = ?, adapter_id = ?, event_types_json = ?, include_backfill = ?,
			revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, in.Name, b2i(in.Enabled), in.AdapterID, string(types),
			b2i(in.IncludeBackfill), tx.NowMicros(), id, rev)
		if err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return apperr.New(apperr.CodeInvalid, "adapter_id: unknown adapter %s", in.AdapterID)
			}
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return apperr.New(apperr.CodePrecondition, "the route was changed by someone else; reload and retry")
		}
		if err := svc.write(ctx, tx, id, in); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "route.updated", Target: audit.Target{Type: "route", ID: id},
			Details: map[string]any{"adapter_id": in.AdapterID, "include_backfill": in.IncludeBackfill, "enabled": in.Enabled}})
		return err
	})
	if err != nil {
		return Route{}, err
	}
	return svc.Get(ctx, id)
}

// Delete removes a route; existing deliveries are unaffected.
func (svc *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	if _, err := svc.Get(ctx, id); err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "routes.delete", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "route.deleted", Target: audit.Target{Type: "route", ID: id}})
		return err
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
