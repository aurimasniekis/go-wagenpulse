package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// VehicleBody creates or replaces a vehicle.
type VehicleBody struct {
	Slug              string   `json:"slug,omitempty" maxLength:"48" doc:"Default derived from display_name."`
	DisplayName       string   `json:"display_name" minLength:"1" maxLength:"100"`
	VIN               string   `json:"vin" minLength:"17" maxLength:"17" doc:"Immutable."`
	Brand             string   `json:"brand,omitempty" enum:"volkswagen,volkswagen_commercial,audi,skoda,seat,cupra,bentley,other"`
	Model             string   `json:"model,omitempty" maxLength:"100"`
	ModelYear         *int     `json:"model_year,omitempty"`
	LicensePlate      string   `json:"license_plate,omitempty" maxLength:"20"`
	Notes             string   `json:"notes,omitempty" maxLength:"2000"`
	Tags              []string `json:"tags,omitempty" maxItems:"20"`
	StaleAfterMinutes *int     `json:"stale_after_minutes,omitempty"`
}

func (b VehicleBody) input() vehicles.Input {
	return vehicles.Input{Slug: b.Slug, DisplayName: b.DisplayName, VIN: b.VIN, Brand: b.Brand, Model: b.Model, ModelYear: b.ModelYear,
		LicensePlate: b.LicensePlate, Notes: b.Notes, Tags: append([]string{}, b.Tags...), StaleAfterMinutes: b.StaleAfterMinutes}
}

// VehicleSummary adds freshness to a vehicle.
type VehicleSummary struct {
	vehicles.Vehicle
	Freshness telemetry.Freshness `json:"freshness"`
}

type vehicleOut struct {
	ETag string `header:"ETag"`
	Body vehicles.Vehicle
}

// VehiclePath addresses a vehicle.
type VehiclePath struct {
	Vehicle string `path:"vehicle" maxLength:"64" doc:"Vehicle ID or slug."`
}

func registerVehicles(api huma.API, d *Deps) {
	tags := []string{"vehicles"}
	huma.Register(api, op("list-vehicles", http.MethodGet, "/vehicles", auth.PermVehiclesRead, "List vehicles with freshness", tags...),
		func(ctx context.Context, in *struct {
			IncludeArchived bool `query:"include_archived"`
			Pagination
		}) (*struct{ Body Page[VehicleSummary] }, error) {
			after, err := in.After()
			if err != nil {
				return nil, err
			}
			f := vehicles.ListFilter{IncludeArchived: in.IncludeArchived, After: after, Limit: in.Lim()}
			if p := Principal(ctx); p.VehicleRestricted() {
				f.OnlyIDs = p.Token.VehicleIDs
			}
			vs, err := d.D.Vehicles.List(ctx, f)
			if err != nil {
				return nil, d.fail(ctx, "list-vehicles", err)
			}
			byNum := map[int64]time.Duration{}
			for _, v := range vs {
				byNum[v.Num] = d.staleAfter(ctx, v)
			}
			fresh, err := d.D.Telemetry.VehicleFreshness(ctx, d.now(), func(n int64) time.Duration {
				if s, ok := byNum[n]; ok {
					return s
				}
				return 2 * time.Hour
			})
			if err != nil {
				return nil, d.fail(ctx, "list-vehicles", err)
			}
			out := make([]VehicleSummary, 0, len(vs))
			for _, v := range vs {
				out = append(out, VehicleSummary{Vehicle: v, Freshness: fresh[v.Num]})
			}
			last := ""
			if len(vs) > 0 {
				last = vs[len(vs)-1].ID
			}
			return &struct{ Body Page[VehicleSummary] }{Body: Page[VehicleSummary]{Items: out, NextCursor: nextCursor(len(vs), in.Lim(), last)}}, nil
		})
	huma.Register(api, op("get-vehicle", http.MethodGet, "/vehicles/{vehicle}", auth.PermVehiclesRead, "Get a vehicle", tags...),
		func(ctx context.Context, in *VehiclePath) (*vehicleOut, error) {
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "get-vehicle", err)
			}
			return &vehicleOut{ETag: etag(v.Revision), Body: v}, nil
		})
	huma.Register(api, created(op("create-vehicle", http.MethodPost, "/vehicles", auth.PermVehiclesWrite, "Create a vehicle", tags...)),
		func(ctx context.Context, in *struct{ Body VehicleBody }) (*vehicleOut, error) {
			if Principal(ctx).VehicleRestricted() {
				return nil, d.fail(ctx, "create-vehicle", errVehicleScoped)
			}
			v, err := d.D.Vehicles.Create(ctx, Principal(ctx).AuditActor(), in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "create-vehicle", err)
			}
			return &vehicleOut{ETag: etag(v.Revision), Body: v}, nil
		})
	huma.Register(api, op("update-vehicle", http.MethodPut, "/vehicles/{vehicle}", auth.PermVehiclesWrite, "Replace a vehicle's configuration", tags...),
		func(ctx context.Context, in *struct {
			VehiclePath
			IfMatch
			Body VehicleBody
		}) (*vehicleOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			cur, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "update-vehicle", err)
			}
			v, err := d.D.Vehicles.Update(ctx, Principal(ctx).AuditActor(), cur.ID, rev, in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "update-vehicle", err)
			}
			return &vehicleOut{ETag: etag(v.Revision), Body: v}, nil
		})
	huma.Register(api, noContent(op("archive-vehicle", http.MethodDelete, "/vehicles/{vehicle}", auth.PermVehiclesWrite,
		"Archive a vehicle (stops collection; telemetry, events and deliveries are kept)", tags...)),
		func(ctx context.Context, in *VehiclePath) (*struct{}, error) {
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "archive-vehicle", err)
			}
			if err := d.D.Vehicles.Archive(ctx, Principal(ctx).AuditActor(), v.ID); err != nil {
				return nil, d.fail(ctx, "archive-vehicle", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, op("restore-vehicle", http.MethodPost, "/vehicles/{vehicle}/restore", auth.PermVehiclesWrite, "Restore an archived vehicle", tags...),
		func(ctx context.Context, in *VehiclePath) (*vehicleOut, error) {
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "restore-vehicle", err)
			}
			v, err = d.D.Vehicles.Restore(ctx, Principal(ctx).AuditActor(), v.ID)
			if err != nil {
				return nil, d.fail(ctx, "restore-vehicle", err)
			}
			return &vehicleOut{ETag: etag(v.Revision), Body: v}, nil
		})
	huma.Register(api, op("purge-vehicle", http.MethodPost, "/vehicles/{vehicle}/purge", auth.PermVehiclesPurge,
		"Irreversibly delete an archived vehicle's telemetry (and optionally the vehicle record)", tags...),
		func(ctx context.Context, in *struct {
			VehiclePath
			Body struct {
				Confirm       bool `json:"confirm" doc:"Must be true; purging cannot be undone."`
				DeleteVehicle bool `json:"delete_vehicle" doc:"Also delete the archived vehicle record."`
			}
		}) (*struct{ Body vehicles.PurgeResult }, error) {
			if err := requireConfirm(in.Body.Confirm, "purging telemetry cannot be undone"); err != nil {
				return nil, err
			}
			if Principal(ctx).Token != nil {
				return nil, d.fail(ctx, "purge-vehicle", errInteractivePurge)
			}
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "purge-vehicle", err)
			}
			res, err := d.D.Vehicles.Purge(ctx, Principal(ctx).AuditActor(), v.ID, in.Body.DeleteVehicle)
			if err != nil {
				return nil, d.fail(ctx, "purge-vehicle", err)
			}
			return &struct{ Body vehicles.PurgeResult }{Body: res}, nil
		})
	huma.Register(api, op("get-vehicle-state", http.MethodGet, "/vehicles/{vehicle}/state", auth.PermTelemetryRead,
		"Latest-known values with freshness, units, timestamps and quality", "telemetry"),
		func(ctx context.Context, in *struct {
			VehiclePath
			Cluster string `query:"cluster" maxLength:"100"`
		}) (*struct {
			Body struct {
				VehicleID         string                 `json:"vehicle_id"`
				StaleAfterSeconds int64                  `json:"stale_after_seconds"`
				Values            []telemetry.StateValue `json:"values"`
			}
		}, error) {
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "vehicle-state", err)
			}
			stale := d.staleAfter(ctx, v)
			vals, err := d.D.Telemetry.LatestState(ctx, v.Num, d.now(), stale, in.Cluster)
			if err != nil {
				return nil, d.fail(ctx, "vehicle-state", err)
			}
			out := &struct {
				Body struct {
					VehicleID         string                 `json:"vehicle_id"`
					StaleAfterSeconds int64                  `json:"stale_after_seconds"`
					Values            []telemetry.StateValue `json:"values"`
				}
			}{}
			out.Body.VehicleID, out.Body.StaleAfterSeconds, out.Body.Values = v.ID, int64(stale.Seconds()), vals
			return out, nil
		})
	huma.Register(api, op("get-vehicle-series", http.MethodGet, "/vehicles/{vehicle}/series", auth.PermTelemetryRead,
		"History of one signal (raw, bucketed with explicit gaps, or change points)", "telemetry"),
		func(ctx context.Context, in *struct {
			VehiclePath
			Signal    string    `query:"signal" required:"true" maxLength:"36" doc:"Signal key (UUID)."`
			From      time.Time `query:"from" doc:"Default: to minus 7 days."`
			To        time.Time `query:"to" doc:"Default: now."`
			MaxPoints int       `query:"max_points" minimum:"0" maximum:"5000" doc:"Default 1000."`
			BucketSec int       `query:"bucket_seconds" minimum:"0" maximum:"604800" doc:"Force a bucket width (≥ 60)."`
		}) (*struct{ Body telemetry.Series }, error) {
			v, err := d.vehicle(ctx, in.Vehicle)
			if err != nil {
				return nil, d.fail(ctx, "series", err)
			}
			sig, err := d.D.Telemetry.SignalByKey(ctx, in.Signal)
			if err != nil {
				return nil, d.fail(ctx, "series", err)
			}
			from, to := parseRange(in.From, in.To, 7*24*time.Hour, d.now())
			s, err := d.D.Telemetry.Series(ctx, telemetry.SeriesQuery{VehicleNum: v.Num, Signal: sig, From: from, To: to, MaxPoints: in.MaxPoints,
				Bucket: time.Duration(in.BucketSec) * time.Second})
			if err != nil {
				return nil, d.fail(ctx, "series", err)
			}
			return &struct{ Body telemetry.Series }{Body: s}, nil
		})
}

func registerSignals(api huma.API, d *Deps) {
	tags := []string{"signals"}
	huma.Register(api, op("list-signals", http.MethodGet, "/signals", auth.PermSignalsRead, "Search signals and dictionary metadata", tags...),
		func(ctx context.Context, in *struct {
			Q        string `query:"q" maxLength:"100"`
			Cluster  string `query:"cluster" maxLength:"100"`
			Origin   string `query:"origin" enum:"continuous,historical,observed,"`
			Vehicle  string `query:"vehicle" maxLength:"64" doc:"Only signals with data for this vehicle."`
			OnlySeen bool   `query:"only_seen" doc:"Only signals that ever had data."`
			Limit    int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
			Cursor   string `query:"cursor" maxLength:"64"`
		}) (*struct{ Body Page[telemetry.Signal] }, error) {
			f := telemetry.SignalFilter{Query: in.Q, Cluster: in.Cluster, Origin: in.Origin, OnlySeen: in.OnlySeen, After: in.Cursor, Limit: in.Limit}
			if in.Vehicle != "" {
				v, err := d.vehicle(ctx, in.Vehicle)
				if err != nil {
					return nil, d.fail(ctx, "list-signals", err)
				}
				f.ObservedFor = v.Num
			}
			ss, err := d.D.Telemetry.Signals(ctx, f)
			if err != nil {
				return nil, d.fail(ctx, "list-signals", err)
			}
			next := ""
			if len(ss) == f.Limit && f.Limit > 0 {
				next = ss[len(ss)-1].Key
			}
			return &struct{ Body Page[telemetry.Signal] }{Body: Page[telemetry.Signal]{Items: ss, NextCursor: next}}, nil
		})
	huma.Register(api, op("get-signal", http.MethodGet, "/signals/{key}", auth.PermSignalsRead, "Signal metadata", tags...),
		func(ctx context.Context, in *struct {
			Key string `path:"key" maxLength:"36"`
		}) (*struct{ Body telemetry.Signal }, error) {
			s, err := d.D.Telemetry.SignalByKey(ctx, in.Key)
			if err != nil {
				return nil, d.fail(ctx, "get-signal", err)
			}
			return &struct{ Body telemetry.Signal }{Body: s}, nil
		})
	huma.Register(api, op("list-signal-clusters", http.MethodGet, "/signal-clusters", auth.PermSignalsRead, "Data clusters with signal counts", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body map[string]int }, error) {
			c, err := d.D.Telemetry.Clusters(ctx)
			if err != nil {
				return nil, d.fail(ctx, "clusters", err)
			}
			return &struct{ Body map[string]int }{Body: c}, nil
		})
	huma.Register(api, op("list-dictionaries", http.MethodGet, "/dictionaries", auth.PermSignalsRead, "Loaded dictionary versions", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []dictionary.Version }, error) {
			vs, err := d.D.Dictionary.Versions(ctx)
			if err != nil {
				return nil, d.fail(ctx, "dictionaries", err)
			}
			return &struct{ Body []dictionary.Version }{Body: vs}, nil
		})
}

var (
	errVehicleScoped    = problem(http.StatusForbidden, "forbidden", "vehicle-restricted tokens cannot create vehicles", nil)
	errInteractivePurge = problem(http.StatusForbidden, "forbidden", "purging data needs an interactive sign-in", nil)
)

var _ = strings.TrimSpace
