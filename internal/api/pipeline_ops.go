package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/adapters"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/routes"
	"github.com/aurimasniekis/go-wagenpulse/internal/rules"
	"github.com/aurimasniekis/go-wagenpulse/internal/sources"
)

// SourceBody creates or replaces a source. Credentials are write-only.
type SourceBody struct {
	Name                  string   `json:"name" minLength:"1" maxLength:"100"`
	Enabled               bool     `json:"enabled"`
	Brand                 string   `json:"brand" enum:"volkswagen,volkswagen_commercial,audi,skoda,seat,cupra,bentley"`
	PortalBrandKey        string   `json:"portal_brand_key,omitempty" maxLength:"64" doc:"Brand key from the portal login URL (needed for password login of some brands)."`
	Country               string   `json:"country" minLength:"2" maxLength:"2"`
	Language              string   `json:"language" minLength:"2" maxLength:"2"`
	AuthMode              string   `json:"auth_mode" enum:"session_cookie,password"`
	Username              *string  `json:"username,omitempty" maxLength:"254" doc:"Write-only account email for password login; omit to keep."`
	Secret                *string  `json:"secret,omitempty" maxLength:"16384" doc:"Write-only: the password, or the portal session cookie header value; omit to keep, empty clears."`
	SecretRef             string   `json:"secret_ref,omitempty" maxLength:"512" doc:"External reference instead of a stored secret (env:WAGENPULSE_SECRET_… or file:…). Send it on every update."`
	CollectContinuous     bool     `json:"collect_continuous"`
	CollectHistorical     bool     `json:"collect_historical"`
	IntervalMinutes       int      `json:"interval_minutes,omitempty" minimum:"0" maximum:"1440"`
	LiveEventPolicy       string   `json:"live_event_policy,omitempty" enum:"none,preview,publish,"`
	HistoricalEventPolicy string   `json:"historical_event_policy,omitempty" enum:"none,preview,publish,"`
	VehicleIDs            []string `json:"vehicle_ids,omitempty" maxItems:"100"`
}

func (b SourceBody) input() sources.Input {
	return sources.Input{Name: b.Name, Enabled: b.Enabled, Brand: b.Brand, PortalBrandKey: b.PortalBrandKey, Country: b.Country, Language: b.Language,
		AuthMode: b.AuthMode, Username: b.Username, Secret: b.Secret, SecretRef: b.SecretRef, CollectContinuous: b.CollectContinuous,
		CollectHistorical: b.CollectHistorical, IntervalMinutes: b.IntervalMinutes, LiveEventPolicy: b.LiveEventPolicy,
		HistoricalEventPolicy: b.HistoricalEventPolicy, VehicleIDs: append([]string{}, b.VehicleIDs...)}
}

type sourceOut struct {
	ETag string `header:"ETag"`
	Body sources.Source
}

// IDRef addresses a resource by ID.
type IDRef struct {
	ID string `path:"id" maxLength:"64"`
}

func registerSources(api huma.API, d *Deps) {
	tags := []string{"sources"}
	huma.Register(api, op("list-sources", http.MethodGet, "/sources", auth.PermSourcesRead, "List data sources with authorization state", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []sources.Source }, error) {
			ss, err := d.D.Sources.List(ctx)
			if err != nil {
				return nil, d.fail(ctx, "list-sources", err)
			}
			return &struct{ Body []sources.Source }{Body: ss}, nil
		})
	huma.Register(api, op("get-source", http.MethodGet, "/sources/{id}", auth.PermSourcesRead, "Get a source", tags...),
		func(ctx context.Context, in *IDRef) (*sourceOut, error) {
			s, err := d.D.Sources.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-source", err)
			}
			return &sourceOut{ETag: etag(s.Revision), Body: s}, nil
		})
	huma.Register(api, created(op("create-source", http.MethodPost, "/sources", auth.PermSourcesWrite,
		"Create a source (unofficial EU Data Act portal interface, ADR-0005)", tags...)),
		func(ctx context.Context, in *struct{ Body SourceBody }) (*sourceOut, error) {
			s, err := d.D.Sources.Create(ctx, Principal(ctx).AuditActor(), in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "create-source", err)
			}
			return &sourceOut{ETag: etag(s.Revision), Body: s}, nil
		})
	huma.Register(api, op("update-source", http.MethodPut, "/sources/{id}", auth.PermSourcesWrite, "Replace a source's configuration", tags...),
		func(ctx context.Context, in *struct {
			IDRef
			IfMatch
			Body SourceBody
		}) (*sourceOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			s, err := d.D.Sources.Update(ctx, Principal(ctx).AuditActor(), in.ID, rev, in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "update-source", err)
			}
			return &sourceOut{ETag: etag(s.Revision), Body: s}, nil
		})
	huma.Register(api, noContent(op("delete-source", http.MethodDelete, "/sources/{id}", auth.PermSourcesWrite,
		"Delete a source and its stored credentials (imported data is kept)", tags...)),
		func(ctx context.Context, in *IDRef) (*struct{}, error) {
			if err := d.D.Sources.Delete(ctx, Principal(ctx).AuditActor(), in.ID); err != nil {
				return nil, d.fail(ctx, "delete-source", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, noContent(op("collect-source-now", http.MethodPost, "/sources/{id}/collect", auth.PermSourcesWrite,
		"Run collection now (bounded by the portal's rate limits)", tags...)),
		func(ctx context.Context, in *IDRef) (*struct{}, error) {
			if err := d.D.Collector.CollectNow(ctx, Principal(ctx).AuditActor(), in.ID); err != nil {
				return nil, d.fail(ctx, "collect-now", err)
			}
			return &struct{}{}, nil
		})
}

func registerImports(api huma.API, d *Deps) {
	tags := []string{"imports"}
	huma.Register(api, op("list-imports", http.MethodGet, "/imports", auth.PermImportsRead, "Import history (newest first)", tags...),
		func(ctx context.Context, in *struct {
			Vehicle string `query:"vehicle" maxLength:"64"`
			Source  string `query:"source" maxLength:"64"`
			State   string `query:"state" enum:"queued,running,succeeded,partial,failed,cancelled,"`
			Kind    string `query:"kind" enum:"upload,collection,reprocess,"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Before  string `query:"before" maxLength:"64" doc:"Cursor: imports created before this import ID."`
		}) (*struct{ Body Page[ingest.Import] }, error) {
			f := ingest.ImportFilter{SourceID: in.Source, State: in.State, Kind: in.Kind, Before: in.Before, Limit: in.Limit}
			if in.Vehicle != "" {
				v, err := d.vehicle(ctx, in.Vehicle)
				if err != nil {
					return nil, d.fail(ctx, "list-imports", err)
				}
				f.VehicleNum = v.Num
			}
			nums, _ := d.restrictedNums(ctx)
			f.VehicleNums = nums
			is, err := d.D.Ingest.List(ctx, f)
			if err != nil {
				return nil, d.fail(ctx, "list-imports", err)
			}
			next := ""
			if len(is) == f.Limit {
				next = is[len(is)-1].ID
			}
			return &struct{ Body Page[ingest.Import] }{Body: Page[ingest.Import]{Items: is, NextCursor: next}}, nil
		})
	huma.Register(api, op("get-import", http.MethodGet, "/imports/{id}", auth.PermImportsRead, "An import with its deliveries", tags...),
		func(ctx context.Context, in *IDRef) (*struct {
			Body struct {
				Import     ingest.Import        `json:"import"`
				Deliveries []ingest.RawDelivery `json:"deliveries"`
			}
		}, error) {
			im, err := d.D.Ingest.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-import", err)
			}
			if im.VehicleID != "" && !Principal(ctx).VehicleAllowed(im.VehicleID) {
				return nil, d.fail(ctx, "get-import", ingest.ErrImportNotFound)
			}
			ds, err := d.D.Ingest.ImportDeliveries(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-import", err)
			}
			out := &struct {
				Body struct {
					Import     ingest.Import        `json:"import"`
					Deliveries []ingest.RawDelivery `json:"deliveries"`
				}
			}{}
			out.Body.Import, out.Body.Deliveries = im, ds
			return out, nil
		})
	huma.Register(api, noContent(op("cancel-import", http.MethodPost, "/imports/{id}/cancel", auth.PermImportsWrite,
		"Cancel a queued or running import (data already stored stays)", tags...)),
		func(ctx context.Context, in *IDRef) (*struct{}, error) {
			if err := d.D.Ingest.Cancel(ctx, Principal(ctx).AuditActor(), in.ID); err != nil {
				return nil, d.fail(ctx, "cancel-import", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, op("retry-import", http.MethodPost, "/imports/{id}/retry", auth.PermImportsWrite,
		"Retry rejected, unrecognized or pending deliveries of an import", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body ingest.Import }, error) {
			im, err := d.D.Ingest.Retry(ctx, Principal(ctx).AuditActor(), in.ID)
			if err != nil {
				return nil, d.fail(ctx, "retry-import", err)
			}
			return &struct{ Body ingest.Import }{Body: im}, nil
		})
	huma.Register(api, created(op("reprocess-deliveries", http.MethodPost, "/imports/reprocess", auth.PermImportsWrite,
		"Reprocess stored raw deliveries (event policy defaults to none)", tags...)),
		func(ctx context.Context, in *struct {
			Body struct {
				DeliveryIDs []string `json:"delivery_ids" minItems:"1" maxItems:"1000"`
				EventPolicy string   `json:"event_policy,omitempty" enum:"none,preview,publish,"`
			}
		}) (*struct{ Body ingest.Import }, error) {
			im, err := d.D.Ingest.Reprocess(ctx, Principal(ctx).AuditActor(), in.Body.DeliveryIDs, in.Body.EventPolicy)
			if err != nil {
				return nil, d.fail(ctx, "reprocess", err)
			}
			return &struct{ Body ingest.Import }{Body: im}, nil
		})
	huma.Register(api, op("list-raw-deliveries", http.MethodGet, "/raw-deliveries", auth.PermImportsRead, "Stored raw deliveries", tags...),
		func(ctx context.Context, in *struct {
			Vehicle string `query:"vehicle" maxLength:"64"`
			Status  string `query:"status" enum:"stored,processed,rejected,unrecognized,purged,"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Before  int64  `query:"before" minimum:"0"`
		}) (*struct {
			Body struct {
				Items      []ingest.RawDelivery `json:"items"`
				NextBefore int64                `json:"next_before,omitempty"`
			}
		}, error) {
			f := ingest.DeliveryFilter{Status: in.Status, Before: in.Before, Limit: in.Limit}
			if in.Vehicle != "" {
				v, err := d.vehicle(ctx, in.Vehicle)
				if err != nil {
					return nil, d.fail(ctx, "raw-deliveries", err)
				}
				f.VehicleNum = v.Num
			} else if Principal(ctx).VehicleRestricted() {
				return nil, d.fail(ctx, "raw-deliveries", problem(http.StatusBadRequest, "validation_failed", "vehicle-restricted tokens must pass vehicle", nil))
			}
			ds, next, err := d.D.Ingest.Deliveries(ctx, f)
			if err != nil {
				return nil, d.fail(ctx, "raw-deliveries", err)
			}
			out := &struct {
				Body struct {
					Items      []ingest.RawDelivery `json:"items"`
					NextBefore int64                `json:"next_before,omitempty"`
				}
			}{}
			out.Body.Items, out.Body.NextBefore = ds, next
			return out, nil
		})
}

// RuleBody creates or replaces a rule.
type RuleBody struct {
	Name        string           `json:"name" minLength:"1" maxLength:"100"`
	Description string           `json:"description,omitempty" maxLength:"2000"`
	Enabled     bool             `json:"enabled"`
	Definition  rules.Definition `json:"definition"`
}

type ruleOut struct {
	ETag string `header:"ETag"`
	Body rules.Rule
}

func registerRules(api huma.API, d *Deps) {
	tags := []string{"rules"}
	huma.Register(api, op("list-rules", http.MethodGet, "/rules", auth.PermRulesRead, "List event rules", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []rules.Rule }, error) {
			rs, err := d.D.Rules.List(ctx)
			if err != nil {
				return nil, d.fail(ctx, "list-rules", err)
			}
			return &struct{ Body []rules.Rule }{Body: rs}, nil
		})
	huma.Register(api, op("get-rule", http.MethodGet, "/rules/{id}", auth.PermRulesRead, "Get a rule with execution statistics", tags...),
		func(ctx context.Context, in *IDRef) (*ruleOut, error) {
			r, err := d.D.Rules.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-rule", err)
			}
			return &ruleOut{ETag: etag(r.ETagRev), Body: r}, nil
		})
	huma.Register(api, op("list-rule-revisions", http.MethodGet, "/rules/{id}/revisions", auth.PermRulesRead, "Immutable revisions of a rule", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body []map[string]any }, error) {
			rs, err := d.D.Rules.Revisions(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "rule-revisions", err)
			}
			return &struct{ Body []map[string]any }{Body: rs}, nil
		})
	huma.Register(api, created(op("create-rule", http.MethodPost, "/rules", auth.PermRulesWrite, "Create a rule", tags...)),
		func(ctx context.Context, in *struct{ Body RuleBody }) (*ruleOut, error) {
			r, err := d.D.Rules.Create(ctx, Principal(ctx).AuditActor(), rules.Input{Name: in.Body.Name, Description: in.Body.Description,
				Enabled: in.Body.Enabled, Definition: in.Body.Definition})
			if err != nil {
				return nil, d.fail(ctx, "create-rule", err)
			}
			return &ruleOut{ETag: etag(r.ETagRev), Body: r}, nil
		})
	huma.Register(api, op("update-rule", http.MethodPut, "/rules/{id}", auth.PermRulesWrite,
		"Replace a rule (a changed definition becomes a new revision and resets rule state)", tags...),
		func(ctx context.Context, in *struct {
			IDRef
			IfMatch
			Body RuleBody
		}) (*ruleOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			r, err := d.D.Rules.Update(ctx, Principal(ctx).AuditActor(), in.ID, rev, rules.Input{Name: in.Body.Name, Description: in.Body.Description,
				Enabled: in.Body.Enabled, Definition: in.Body.Definition})
			if err != nil {
				return nil, d.fail(ctx, "update-rule", err)
			}
			return &ruleOut{ETag: etag(r.ETagRev), Body: r}, nil
		})
	huma.Register(api, noContent(op("delete-rule", http.MethodDelete, "/rules/{id}", auth.PermRulesWrite,
		"Delete a rule (its events and revisions stay)", tags...)),
		func(ctx context.Context, in *IDRef) (*struct{}, error) {
			if err := d.D.Rules.Delete(ctx, Principal(ctx).AuditActor(), in.ID); err != nil {
				return nil, d.fail(ctx, "delete-rule", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, op("preview-rule", http.MethodPost, "/rules/preview", auth.PermRulesRead,
		"Evaluate a definition against stored history without side effects", tags...),
		func(ctx context.Context, in *struct {
			Body struct {
				Definition rules.Definition `json:"definition"`
				VehicleID  string           `json:"vehicle_id" maxLength:"64"`
				From       time.Time        `json:"from"`
				To         time.Time        `json:"to"`
			}
		}) (*struct{ Body rules.PreviewResult }, error) {
			v, err := d.vehicle(ctx, in.Body.VehicleID)
			if err != nil {
				return nil, d.fail(ctx, "preview-rule", err)
			}
			res, err := d.D.Rules.Preview(ctx, rules.PreviewRequest{Definition: in.Body.Definition, VehicleID: v.ID, From: in.Body.From, To: in.Body.To})
			if err != nil {
				return nil, d.fail(ctx, "preview-rule", err)
			}
			return &struct{ Body rules.PreviewResult }{Body: res}, nil
		})
}

// AdapterBody creates or replaces an adapter. NATS credentials are write-only.
type AdapterBody struct {
	Name     string               `json:"name" minLength:"1" maxLength:"64"`
	Enabled  bool                 `json:"enabled"`
	Config   adapters.PulseConfig `json:"config"`
	Creds    *string              `json:"creds,omitempty" maxLength:"16384" doc:"Write-only Pulse client .creds content; omit to keep."`
	CredsRef string               `json:"creds_ref,omitempty" maxLength:"512" doc:"External reference instead (env:WAGENPULSE_SECRET_… or file:…); send it on every update."`
}

func (b AdapterBody) input() adapters.Input {
	return adapters.Input{Name: b.Name, Enabled: b.Enabled, Config: b.Config, Creds: b.Creds, CredsRef: b.CredsRef}
}

type adapterOut struct {
	ETag string `header:"ETag"`
	Body adapters.Adapter
}

func registerAdapters(api huma.API, d *Deps) {
	tags := []string{"adapters"}
	huma.Register(api, op("list-adapters", http.MethodGet, "/adapters", auth.PermAdaptersRead, "List adapter instances with health and queues", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []adapters.Adapter }, error) {
			as, err := d.D.Adapters.List(ctx)
			if err != nil {
				return nil, d.fail(ctx, "list-adapters", err)
			}
			return &struct{ Body []adapters.Adapter }{Body: as}, nil
		})
	huma.Register(api, op("get-adapter", http.MethodGet, "/adapters/{id}", auth.PermAdaptersRead, "Get an adapter", tags...),
		func(ctx context.Context, in *IDRef) (*adapterOut, error) {
			a, err := d.D.Adapters.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-adapter", err)
			}
			return &adapterOut{ETag: etag(a.Revision), Body: a}, nil
		})
	huma.Register(api, created(op("create-adapter", http.MethodPost, "/adapters", auth.PermAdaptersWrite, "Create a Pulse/NATS adapter", tags...)),
		func(ctx context.Context, in *struct{ Body AdapterBody }) (*adapterOut, error) {
			a, err := d.D.Adapters.Create(ctx, Principal(ctx).AuditActor(), in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "create-adapter", err)
			}
			return &adapterOut{ETag: etag(a.Revision), Body: a}, nil
		})
	huma.Register(api, op("update-adapter", http.MethodPut, "/adapters/{id}", auth.PermAdaptersWrite, "Replace an adapter's configuration", tags...),
		func(ctx context.Context, in *struct {
			IDRef
			IfMatch
			Body AdapterBody
		}) (*adapterOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			a, err := d.D.Adapters.Update(ctx, Principal(ctx).AuditActor(), in.ID, rev, in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "update-adapter", err)
			}
			return &adapterOut{ETag: etag(a.Revision), Body: a}, nil
		})
	huma.Register(api, noContent(op("delete-adapter", http.MethodDelete, "/adapters/{id}", auth.PermAdaptersWrite,
		"Delete an adapter (refused while deliveries are pending unless abandon_pending=true)", tags...)),
		func(ctx context.Context, in *struct {
			IDRef
			AbandonPending bool `query:"abandon_pending"`
		}) (*struct{}, error) {
			if err := d.D.Adapters.Delete(ctx, Principal(ctx).AuditActor(), in.ID, in.AbandonPending); err != nil {
				return nil, d.fail(ctx, "delete-adapter", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, op("test-adapter", http.MethodPost, "/adapters/{id}/test", auth.PermAdaptersWrite,
		"Publish one adapter.test event and wait for the JetStream acknowledgement", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body adapters.TestResult }, error) {
			res, err := d.D.Dispatch.Test(ctx, Principal(ctx).AuditActor(), in.ID)
			if err != nil {
				return nil, d.fail(ctx, "test-adapter", err)
			}
			return &struct{ Body adapters.TestResult }{Body: res}, nil
		})
}

// RouteBody creates or replaces a route.
type RouteBody struct {
	Name            string   `json:"name" minLength:"1" maxLength:"100"`
	Enabled         bool     `json:"enabled"`
	AdapterID       string   `json:"adapter_id" maxLength:"64"`
	EventTypes      []string `json:"event_types,omitempty" maxItems:"10"`
	RuleIDs         []string `json:"rule_ids,omitempty" maxItems:"200"`
	VehicleIDs      []string `json:"vehicle_ids,omitempty" maxItems:"200"`
	IncludeBackfill bool     `json:"include_backfill"`
}

func (b RouteBody) input() routes.Input {
	return routes.Input{Name: b.Name, Enabled: b.Enabled, AdapterID: b.AdapterID, EventTypes: append([]string{}, b.EventTypes...),
		RuleIDs: append([]string{}, b.RuleIDs...), VehicleIDs: append([]string{}, b.VehicleIDs...), IncludeBackfill: b.IncludeBackfill}
}

type routeOut struct {
	ETag string `header:"ETag"`
	Body routes.Route
}

func registerRoutes(api huma.API, d *Deps) {
	tags := []string{"routes"}
	huma.Register(api, op("list-routes", http.MethodGet, "/routes", auth.PermRoutesRead, "List routes", tags...),
		func(ctx context.Context, in *struct {
			Adapter string `query:"adapter" maxLength:"64"`
		}) (*struct{ Body []routes.Route }, error) {
			rs, err := d.D.Routes.List(ctx, in.Adapter)
			if err != nil {
				return nil, d.fail(ctx, "list-routes", err)
			}
			return &struct{ Body []routes.Route }{Body: rs}, nil
		})
	huma.Register(api, op("get-route", http.MethodGet, "/routes/{id}", auth.PermRoutesRead, "Get a route", tags...),
		func(ctx context.Context, in *IDRef) (*routeOut, error) {
			r, err := d.D.Routes.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-route", err)
			}
			return &routeOut{ETag: etag(r.Revision), Body: r}, nil
		})
	huma.Register(api, created(op("create-route", http.MethodPost, "/routes", auth.PermRoutesWrite, "Create a route", tags...)),
		func(ctx context.Context, in *struct{ Body RouteBody }) (*routeOut, error) {
			r, err := d.D.Routes.Create(ctx, Principal(ctx).AuditActor(), in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "create-route", err)
			}
			return &routeOut{ETag: etag(r.Revision), Body: r}, nil
		})
	huma.Register(api, op("update-route", http.MethodPut, "/routes/{id}", auth.PermRoutesWrite, "Replace a route", tags...),
		func(ctx context.Context, in *struct {
			IDRef
			IfMatch
			Body RouteBody
		}) (*routeOut, error) {
			rev, err := in.Revision()
			if err != nil {
				return nil, err
			}
			r, err := d.D.Routes.Update(ctx, Principal(ctx).AuditActor(), in.ID, rev, in.Body.input())
			if err != nil {
				return nil, d.fail(ctx, "update-route", err)
			}
			return &routeOut{ETag: etag(r.Revision), Body: r}, nil
		})
	huma.Register(api, noContent(op("delete-route", http.MethodDelete, "/routes/{id}", auth.PermRoutesWrite,
		"Delete a route (existing deliveries are unaffected)", tags...)),
		func(ctx context.Context, in *IDRef) (*struct{}, error) {
			if err := d.D.Routes.Delete(ctx, Principal(ctx).AuditActor(), in.ID); err != nil {
				return nil, d.fail(ctx, "delete-route", err)
			}
			return &struct{}{}, nil
		})
}

func registerEvents(api huma.API, d *Deps) {
	tags := []string{"events"}
	huma.Register(api, op("list-events", http.MethodGet, "/events", auth.PermEventsRead, "Search generated events (newest first)", tags...),
		func(ctx context.Context, in *struct {
			Vehicle string    `query:"vehicle" maxLength:"64"`
			Rule    string    `query:"rule" maxLength:"64"`
			Type    string    `query:"type" maxLength:"64"`
			Import  string    `query:"import" maxLength:"64"`
			From    time.Time `query:"from"`
			To      time.Time `query:"to"`
			Limit   int       `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Before  int64     `query:"before" minimum:"0"`
		}) (*struct {
			Body struct {
				Items      []events.Event `json:"items"`
				NextBefore int64          `json:"next_before,omitempty"`
			}
		}, error) {
			f := events.EventFilter{RuleID: in.Rule, Type: in.Type, ImportID: in.Import, From: in.From, To: in.To, Before: in.Before, Limit: in.Limit}
			if in.Vehicle != "" {
				v, err := d.vehicle(ctx, in.Vehicle)
				if err != nil {
					return nil, d.fail(ctx, "list-events", err)
				}
				f.VehicleNum = v.Num
			}
			nums, _ := d.restrictedNums(ctx)
			f.VehicleNums = nums
			es, next, err := d.D.Events.Events(ctx, f)
			if err != nil {
				return nil, d.fail(ctx, "list-events", err)
			}
			out := &struct {
				Body struct {
					Items      []events.Event `json:"items"`
					NextBefore int64          `json:"next_before,omitempty"`
				}
			}{}
			out.Body.Items, out.Body.NextBefore = es, next
			return out, nil
		})
	huma.Register(api, op("get-event", http.MethodGet, "/events/{id}", auth.PermEventsRead, "An event with its stored payload (VIN never included)", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body events.Event }, error) {
			e, _, err := d.D.Events.Get(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-event", err)
			}
			if e.VehicleID != "" && !Principal(ctx).VehicleAllowed(e.VehicleID) {
				return nil, d.fail(ctx, "get-event", events.ErrNotFound)
			}
			return &struct{ Body events.Event }{Body: e}, nil
		})
	huma.Register(api, op("list-deliveries", http.MethodGet, "/deliveries", auth.PermEventsRead, "Deliveries per destination (newest first)", tags...),
		func(ctx context.Context, in *struct {
			Adapter string `query:"adapter" maxLength:"64"`
			State   string `query:"state" enum:"pending,processing,succeeded,retrying,dead,"`
			Event   string `query:"event" maxLength:"64"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
			Before  int64  `query:"before" minimum:"0"`
		}) (*struct {
			Body struct {
				Items      []events.Delivery `json:"items"`
				NextBefore int64             `json:"next_before,omitempty"`
			}
		}, error) {
			nums, _ := d.restrictedNums(ctx)
			ds, next, err := d.D.Events.Deliveries(ctx, events.DeliveryFilter{AdapterID: in.Adapter, State: in.State, EventID: in.Event, VehicleNums: nums,
				Before: in.Before, Limit: in.Limit})
			if err != nil {
				return nil, d.fail(ctx, "list-deliveries", err)
			}
			out := &struct {
				Body struct {
					Items      []events.Delivery `json:"items"`
					NextBefore int64             `json:"next_before,omitempty"`
				}
			}{}
			out.Body.Items, out.Body.NextBefore = ds, next
			return out, nil
		})
	huma.Register(api, op("get-delivery", http.MethodGet, "/deliveries/{id}", auth.PermEventsRead, "A delivery with its attempt history", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body events.Delivery }, error) {
			dl, err := d.D.Events.Delivery(ctx, in.ID)
			if err != nil {
				return nil, d.fail(ctx, "get-delivery", err)
			}
			if dl.VehicleID != "" && !Principal(ctx).VehicleAllowed(dl.VehicleID) {
				return nil, d.fail(ctx, "get-delivery", events.ErrNotFound)
			}
			return &struct{ Body events.Delivery }{Body: dl}, nil
		})
	huma.Register(api, op("retry-delivery", http.MethodPost, "/deliveries/{id}/retry", auth.PermDeliveriesRetry,
		"Retry a dead or retrying delivery now (same Nats-Msg-Id)", tags...),
		func(ctx context.Context, in *IDRef) (*struct{ Body events.Delivery }, error) {
			dl, err := d.D.Events.Retry(ctx, Principal(ctx).AuditActor(), in.ID)
			if err != nil {
				return nil, d.fail(ctx, "retry-delivery", err)
			}
			return &struct{ Body events.Delivery }{Body: dl}, nil
		})
	huma.Register(api, op("replay-events", http.MethodPost, "/replays", auth.PermDeliveriesReplay,
		"Preview (confirm=false) or replay events to one adapter; replays are published again with new message IDs", tags...),
		func(ctx context.Context, in *struct {
			Body struct {
				AdapterID  string    `json:"adapter_id" maxLength:"64"`
				From       time.Time `json:"from"`
				To         time.Time `json:"to"`
				Types      []string  `json:"types,omitempty" maxItems:"10"`
				VehicleIDs []string  `json:"vehicle_ids,omitempty" maxItems:"100"`
				RuleID     string    `json:"rule_id,omitempty" maxLength:"64"`
				Confirm    bool      `json:"confirm"`
			}
		}) (*struct{ Body events.ReplayResult }, error) {
			req := events.ReplayRequest{AdapterID: in.Body.AdapterID, From: in.Body.From, To: in.Body.To, Types: in.Body.Types, RuleID: in.Body.RuleID,
				Confirm: in.Body.Confirm}
			for _, id := range in.Body.VehicleIDs {
				v, err := d.vehicle(ctx, id)
				if err != nil {
					return nil, d.fail(ctx, "replay", err)
				}
				req.VehicleNums = append(req.VehicleNums, v.Num)
			}
			if Principal(ctx).VehicleRestricted() && len(req.VehicleNums) == 0 {
				req.VehicleNums, _ = d.restrictedNums(ctx)
			}
			res, err := d.D.Events.Replay(ctx, Principal(ctx).AuditActor(), req)
			if err != nil {
				return nil, d.fail(ctx, "replay", err)
			}
			return &struct{ Body events.ReplayResult }{Body: res}, nil
		})
}

// Overview is the fleet dashboard summary.
type Overview struct {
	Vehicles       int                         `json:"vehicles"`
	FreshVehicles  int                         `json:"fresh_vehicles"`
	StaleVehicles  int                         `json:"stale_vehicles"`
	NoDataVehicles int                         `json:"vehicles_without_data"`
	Sources        map[string]int              `json:"sources_by_state"`
	Imports24h     map[string]int              `json:"imports_24h_by_state"`
	Events24h      int64                       `json:"events_24h"`
	Deliveries     map[string]map[string]int64 `json:"deliveries_by_adapter"`
	Adapters       map[string]int              `json:"adapters_by_state"`
	Observations24 int64                       `json:"observations_24h"`
}

func registerOverview(api huma.API, d *Deps) {
	huma.Register(api, op("get-overview", http.MethodGet, "/overview", auth.PermVehiclesRead, "Fleet, ingestion and delivery health summary", "overview"),
		func(ctx context.Context, _ *struct{}) (*struct{ Body Overview }, error) {
			o := Overview{Sources: map[string]int{}, Imports24h: map[string]int{}, Adapters: map[string]int{}}
			now := d.now()
			vs, err := d.D.Vehicles.List(ctx, listForOverview(ctx))
			if err != nil {
				return nil, d.fail(ctx, "overview", err)
			}
			staleBy := map[int64]time.Duration{}
			for _, v := range vs {
				staleBy[v.Num] = d.staleAfter(ctx, v)
			}
			fresh, err := d.D.Telemetry.VehicleFreshness(ctx, now, func(n int64) time.Duration { return staleBy[n] })
			if err != nil {
				return nil, d.fail(ctx, "overview", err)
			}
			o.Vehicles = len(vs)
			for _, v := range vs {
				f, ok := fresh[v.Num]
				switch {
				case !ok || f.LastObservedAt == nil:
					o.NoDataVehicles++
				case now.Sub(*f.LastObservedAt) > staleBy[v.Num]:
					o.StaleVehicles++
				default:
					o.FreshVehicles++
				}
			}
			if !Principal(ctx).VehicleRestricted() {
				ss, err := d.D.Sources.List(ctx)
				if err != nil {
					return nil, d.fail(ctx, "overview", err)
				}
				for _, s := range ss {
					o.Sources[s.Status.State]++
				}
				as, err := d.D.Adapters.List(ctx)
				if err != nil {
					return nil, d.fail(ctx, "overview", err)
				}
				for _, a := range as {
					o.Adapters[a.Status.State]++
				}
				if o.Deliveries, err = d.D.Events.QueueStats(ctx); err != nil {
					return nil, d.fail(ctx, "overview", err)
				}
			}
			rows, err := d.Store.ReadDB().QueryContext(ctx, `SELECT state, count(*) FROM imports WHERE created_at > ? GROUP BY state`, now.Add(-24*time.Hour).UnixMicro())
			if err == nil {
				if err := func() error {
					defer rows.Close()
					for rows.Next() {
						var s string
						var n int
						if rows.Scan(&s, &n) == nil {
							o.Imports24h[s] = n
						}
					}
					return rows.Err()
				}(); err != nil {
					return nil, d.fail(ctx, "overview", err)
				}
			}
			_ = d.Store.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE created_at > ?`, now.Add(-24*time.Hour).UnixMicro()).Scan(&o.Events24h)
			_ = d.Store.ReadDB().QueryRowContext(ctx, `SELECT coalesce(sum(observations_accepted), 0) FROM imports WHERE created_at > ?`,
				now.Add(-24*time.Hour).UnixMicro()).Scan(&o.Observations24)
			if o.Deliveries == nil {
				o.Deliveries = map[string]map[string]int64{}
			}
			return &struct{ Body Overview }{Body: o}, nil
		})
}
