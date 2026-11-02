package app

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/adapters"
	"github.com/aurimasniekis/go-wagenpulse/internal/api"
	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/routes"
	"github.com/aurimasniekis/go-wagenpulse/internal/rules"
	"github.com/aurimasniekis/go-wagenpulse/internal/sources"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// Domain is the API's domain service bundle.
type Domain = api.Domain

// initDomain builds the telemetry, ingestion and event services.
func (a *App) initDomain(ctx context.Context) error {
	cfg := a.Cfg
	runner := jobs.NewRunner(a.Store, jobs.Options{Concurrency: cfg.Workers.Jobs, Logger: a.Log, Clock: a.Clock, Metrics: a.Metrics})
	blobs, err := ingest.NewBlobs(filepath.Join(a.Store.Dir(), "blobs"))
	if err != nil {
		return err
	}
	dict := dictionary.New(a.Store, a.Audit)
	if err := dict.LoadBundled(ctx); err != nil {
		return fmt.Errorf("loading bundled dictionaries: %w", err)
	}
	ing := ingest.New(a.Store, blobs, a.Audit, runner, a.Settings, ingest.Options{BatchSize: cfg.Ingest.BatchSize, Clock: a.Clock, Log: a.Log,
		Metrics: a.Metrics, Limits: ingest.Limits{MaxExtractedBytes: cfg.Ingest.MaxExtractedBytes, MaxEntries: cfg.Ingest.MaxEntries,
			MaxRatio: cfg.Ingest.MaxRatio, MaxEntryBytes: min(cfg.Ingest.MaxExtractedBytes, 64<<20)}})
	engine := rules.NewEngine(a.Store, a.Metrics)
	ing.SetSink(engine)
	srcs := sources.New(a.Store, a.Keyring, a.Audit, a.Refs, a.Clock.Now)
	collector := sources.NewCollector(srcs, ing, runner, sources.CollectorOptions{HTTPClient: a.Guard.Client(90 * time.Second),
		MaxDownload: cfg.Ingest.MaxUploadBytes, Log: a.Log, Metrics: a.Metrics})
	adapts := adapters.NewService(a.Store, a.Keyring, a.Audit, a.Refs)
	adapts.Production = cfg.IsProduction()
	mgr := adapters.NewManager(adapts, adapters.ManagerOptions{Guard: a.Guard, InstallationID: a.InstallationID, Log: a.Log, Metrics: a.Metrics,
		Concurrency: cfg.Workers.DeliveryPerAdapter})
	evs := events.NewService(a.Store, a.Audit)
	evs.Wake = mgr.Wake
	engine.OnEvents = mgr.Wake
	srcs.Wake = func() { _ = collector.Schedule(context.Background()) } //nolint:contextcheck // runs after the request that changed the source
	a.Domain = &Domain{Vehicles: vehicles.New(a.Store, a.Audit), Telemetry: telemetry.NewReader(a.Store), Dictionary: dict, Sources: srcs,
		Collector: collector, Ingest: ing, Rules: rules.NewService(a.Store, a.Audit, a.Clock.Now), Events: evs, Routes: routes.NewService(a.Store, a.Audit),
		Adapters: adapts, Dispatch: mgr}
	a.Domain.RunRetention = func(ctx context.Context) (any, error) { return a.runRetention(ctx) }
	a.workers = append(a.workers,
		runner.Run,
		mgr.Run,
		func(ctx context.Context) {
			periodic(ctx, a.component("collector"), "collector_schedule", 30*time.Second, collector.Schedule)
		},
		func(ctx context.Context) {
			periodic(ctx, a.component("rules"), "stale_sweep", time.Minute, func(ctx context.Context) error {
				_, err := engine.Sweep(ctx, a.Clock.Now())
				return err
			})
		},
		func(ctx context.Context) {
			periodic(ctx, a.component("metrics"), "queue_metrics", 30*time.Second, func(ctx context.Context) error {
				if err := mgr.UpdateQueueMetrics(ctx); err != nil {
					return err
				}
				if err := a.updateJobMetrics(ctx); err != nil {
					return err
				}
				return collector.UpdateLagMetrics(ctx)
			})
		},
		func(ctx context.Context) {
			periodic(ctx, a.component("retention"), "retention", 6*time.Hour, func(ctx context.Context) error {
				_, err := a.runRetention(ctx)
				return err
			})
		},
	)
	return nil
}

func (a *App) updateJobMetrics(ctx context.Context) error {
	rows, err := a.Store.R().CountJobsByState(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		a.Metrics.SetJobQueue(r.Kind, r.State, r.N)
	}
	return nil
}

// domainRegistrations returns API registrations implemented by the app.
func (a *App) domainRegistrations() []func(huma.API, *api.Deps) {
	return []func(huma.API, *api.Deps){registerOperational}
}

// registerOperational adds operations implemented by the app (retention runs).
func registerOperational(hapi huma.API, d *api.Deps) {
	huma.Register(hapi, huma.Operation{OperationID: "run-retention", Method: http.MethodPost, Path: "/settings/retention/run",
		Summary: "Apply retention now (deletes data older than the retention settings)", Tags: []string{"settings"},
		Security: []map[string][]string{{"session": {}}, {"apiToken": {}}}, Metadata: map[string]any{"x-permission": string(auth.PermSettingsWrite)},
		Description: "Requires permission `settings.write`."},
		func(ctx context.Context, in *struct {
			Body struct {
				Confirm bool `json:"confirm" doc:"Must be true: data past retention is deleted irreversibly."`
			}
		}) (*struct{ Body RetentionResult }, error) {
			if !in.Body.Confirm {
				return nil, huma.Error400BadRequest("confirm=true is required: retention deletes data irreversibly")
			}
			res, err := d.D.RunRetention(ctx)
			if err != nil {
				return nil, d.Fail(ctx, "retention", err)
			}
			rr, _ := res.(RetentionResult)
			return &struct{ Body RetentionResult }{Body: rr}, nil
		})
}

// registerUploads mounts streaming endpoints that are not Huma operations: multipart delivery uploads, dictionary
// imports and CSV export.
func (a *App) registerUploads(mux *http.ServeMux, deps *api.Deps) {
	mux.HandleFunc("POST "+api.Prefix+"/uploads/deliveries", func(w http.ResponseWriter, r *http.Request) { a.uploadDeliveries(w, r, deps) })
	mux.HandleFunc("POST "+api.Prefix+"/uploads/dictionary", func(w http.ResponseWriter, r *http.Request) { a.uploadDictionary(w, r, deps) })
	mux.HandleFunc("GET "+api.Prefix+"/exports/observations.csv", func(w http.ResponseWriter, r *http.Request) { a.exportCSV(w, r, deps) })
}

// uploadDeliveries accepts multipart files (field "files", repeatable) and optional fields vehicle_id and
// event_policy (sent before the files); it stores each file as a raw delivery and creates one import (202).
func (a *App) uploadDeliveries(w http.ResponseWriter, r *http.Request, deps *api.Deps) {
	p, perr := deps.AuthenticateHTTP(r, auth.PermImportsWrite)
	if perr != nil {
		api.WriteProblem(w, perr)
		return
	}
	ctx := audit.WithMeta(r.Context(), audit.Meta{SourceIP: deps.ClientIPString(r), RequestID: deps.RequestID(r)})
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		api.WriteProblem(w, problemf(http.StatusUnsupportedMediaType, "unsupported_media_type", "send multipart/form-data with one or more files"))
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	vehicleRef, policy := "", ""
	var stored []ingest.StoredDelivery
	var vehicleNum int64
	files := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			api.WriteProblem(w, problemf(http.StatusBadRequest, "malformed_upload", "the multipart body is malformed or larger than the upload limit"))
			return
		}
		switch part.FormName() {
		case "vehicle_id":
			b, _ := io.ReadAll(io.LimitReader(part, 128))
			vehicleRef = strings.TrimSpace(string(b))
		case "event_policy":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			policy = strings.TrimSpace(string(b))
		case "files":
			if files >= 200 {
				api.WriteProblem(w, problemf(http.StatusBadRequest, "too_many_files", "at most 200 files per upload"))
				return
			}
			files++
			if vehicleRef != "" && vehicleNum == 0 {
				v, err := a.Domain.Vehicles.Get(ctx, vehicleRef)
				if err != nil || !p.VehicleAllowed(v.ID) || v.Archived {
					api.WriteProblem(w, problemf(http.StatusNotFound, "not_found", "vehicle not found or archived"))
					return
				}
				vehicleNum = v.Num
			}
			if p.VehicleRestricted() && vehicleNum == 0 {
				api.WriteProblem(w, problemf(http.StatusBadRequest, "validation_failed", "vehicle-restricted tokens must send vehicle_id before the files"))
				return
			}
			d, err := a.Domain.Ingest.StoreRaw(ctx, part, ingest.RawMeta{Origin: "upload", VehicleNum: vehicleNum, OriginalName: part.FileName(),
				CreatedBy: p.AuditActor().ID, Limit: a.Cfg.Ingest.MaxUploadBytes})
			if err != nil {
				deps.FailHTTP(w, r, "upload", err)
				return
			}
			stored = append(stored, d)
		}
		_ = part.Close()
	}
	if len(stored) == 0 {
		api.WriteProblem(w, problemf(http.StatusBadRequest, "validation_failed", "no files were uploaded (field name: files)"))
		return
	}
	if policy == "" {
		sys, err := a.Settings.System(ctx)
		if err != nil {
			deps.FailHTTP(w, r, "upload", err)
			return
		}
		policy = sys.Value.UploadEventPolicy
	}
	imp, err := a.Domain.Ingest.Create(ctx, ingest.CreateInput{Kind: ingest.KindUpload, VehicleNum: vehicleNum, EventPolicy: policy, Backfill: true,
		Deliveries: stored, Actor: p.AuditActor()})
	if err != nil {
		deps.FailHTTP(w, r, "upload", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", api.Prefix+"/imports/"+imp.ID)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(imp)
}

// uploadDictionary imports a dictionary JSON document produced by tools/dictgen (request body, ≤ 16 MiB).
func (a *App) uploadDictionary(w http.ResponseWriter, r *http.Request, deps *api.Deps) {
	p, perr := deps.AuthenticateHTTP(r, auth.PermDictionaryWrite)
	if perr != nil {
		api.WriteProblem(w, perr)
		return
	}
	ctx := audit.WithMeta(r.Context(), audit.Meta{SourceIP: deps.ClientIPString(r), RequestID: deps.RequestID(r)})
	doc, err := dictionary.Parse(r.Body)
	if err != nil {
		deps.FailHTTP(w, r, "dictionary", err)
		return
	}
	v, created, err := a.Domain.Dictionary.Import(ctx, p.AuditActor(), doc)
	if err != nil {
		deps.FailHTTP(w, r, "dictionary", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// exportCSV streams observations as CSV (bounded: 366 days, 100k rows).
// Query: vehicle (repeatable), signal (repeatable), from, to (RFC 3339).
func (a *App) exportCSV(w http.ResponseWriter, r *http.Request, deps *api.Deps) {
	p, perr := deps.AuthenticateHTTP(r, auth.PermTelemetryExport)
	if perr != nil {
		api.WriteProblem(w, perr)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	from, err1 := time.Parse(time.RFC3339, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339, q.Get("to"))
	if err1 != nil || err2 != nil {
		api.WriteProblem(w, problemf(http.StatusUnprocessableEntity, "validation_failed", "from and to must be RFC 3339 timestamps"))
		return
	}
	var vnums, snums []int64
	for _, ref := range q["vehicle"] {
		v, err := a.Domain.Vehicles.Get(ctx, ref)
		if err != nil || !p.VehicleAllowed(v.ID) {
			api.WriteProblem(w, problemf(http.StatusNotFound, "not_found", "vehicle not found"))
			return
		}
		vnums = append(vnums, v.Num)
	}
	for _, key := range q["signal"] {
		s, err := a.Domain.Telemetry.SignalByKey(ctx, key)
		if err != nil {
			api.WriteProblem(w, problemf(http.StatusNotFound, "not_found", "signal not found"))
			return
		}
		snums = append(snums, s.Num)
	}
	if len(vnums) > 50 || len(snums) > 200 {
		api.WriteProblem(w, problemf(http.StatusUnprocessableEntity, "validation_failed", "at most 50 vehicles and 200 signals per export"))
		return
	}
	if len(vnums) == 0 || len(snums) == 0 || !to.After(from) || to.Sub(from) > telemetry.MaxRange {
		api.WriteProblem(w, problemf(http.StatusUnprocessableEntity, "validation_failed",
			"select at least one vehicle and one signal and a positive range of at most 366 days"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="wagenpulse-observations.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"vehicle_id", "signal_key", "signal_name", "observed_at", "received_at", "value", "text", "quality", "unit", "time_basis"})
	_, truncated, err := a.Domain.Telemetry.Export(ctx, vnums, snums, from, to, func(e telemetry.ExportRow) error {
		num, text := "", ""
		if e.Num != nil {
			num = strconv.FormatFloat(*e.Num, 'f', -1, 64)
		}
		if e.Text != nil {
			text = *e.Text
		}
		return cw.Write([]string{e.VehicleID, e.SignalKey, csvSafe(e.SignalName), e.ObservedAt.Format(time.RFC3339Nano), e.ReceivedAt.Format(time.RFC3339Nano),
			num, csvSafe(text), e.Quality, e.Unit, e.TimeBasis})
	})
	if err != nil {
		var ae *apperr.Error
		if !errors.As(err, &ae) {
			a.component("export").WarnContext(ctx, "export failed mid-stream", obs.Err(err))
		}
	}
	if truncated {
		_ = cw.Write([]string{"# truncated at 100000 rows; narrow the range"})
	}
	cw.Flush()
}

// csvSafe neutralizes spreadsheet formula injection in untrusted text cells.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func problemf(status int, code, detail string) *api.Problem {
	return &api.Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

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

// OpenAPIDocument returns the full API document (the openapi command, drift checks).
func OpenAPIDocument() ([]byte, error) {
	a := &App{}
	return api.OpenAPI(api.APIVersion, a.domainRegistrations()...)
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
