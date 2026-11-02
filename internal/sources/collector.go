package sources

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/providers/vwdataact"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// CollectJob is the job kind of one collection run.
const CollectJob = "source.collect"

// CollectorOptions configure the collector.
type CollectorOptions struct {
	HTTPClient   *http.Client // SSRF-guarded
	BaseURL      string       // tests only; production uses vwdataact.DefaultBaseURL
	IdentityBase string       // tests only
	MinInterval  time.Duration
	MaxDownload  int64
	// MaxFilesPerRun bounds downloads per (vehicle, request type) and run.
	MaxFilesPerRun int
	Log            *slog.Logger
	Metrics        *metrics.Metrics
}

// Collector runs scheduled collection against the unofficial portal interface.
type Collector struct {
	svc    *Service
	ingest *ingest.Service
	jobs   *jobs.Runner
	opts   CollectorOptions
	log    *slog.Logger
}

// NewCollector registers the collection job.
func NewCollector(svc *Service, ing *ingest.Service, j *jobs.Runner, opts CollectorOptions) *Collector {
	if opts.Log == nil {
		opts.Log = obs.Discard()
	}
	if opts.MaxFilesPerRun <= 0 {
		opts.MaxFilesPerRun = 20
	}
	if opts.MaxDownload <= 0 {
		opts.MaxDownload = 50 << 20
	}
	c := &Collector{svc: svc, ingest: ing, jobs: j, opts: opts, log: obs.Component(opts.Log, "collector")}
	// One attempt per job: failures are recorded in source status and the scheduler decides the next run.
	j.Register(CollectJob, 1, 15*time.Minute, c.run)
	return c
}

// Schedule enqueues collection for due sources. Called periodically; the dedup key prevents overlapping runs.
func (c *Collector) Schedule(ctx context.Context) error {
	rows, err := c.svc.s.ReadDB().QueryContext(ctx, `SELECT s.id FROM sources s LEFT JOIN source_status st ON st.source_id = s.id
		WHERE s.enabled = 1 AND (st.next_run_at IS NULL OR st.next_run_at <= ?) AND (st.retry_after IS NULL OR st.retry_after <= ?)
		AND coalesce(st.state, 'configured') NOT IN ('reauthorization_required', 'disabled')`, c.svc.now().UnixMicro(), c.svc.now().UnixMicro())
	if err != nil {
		return err
	}
	var due []string
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			due = append(due, id)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	for _, id := range due {
		if err := c.enqueue(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) enqueue(ctx context.Context, id string) error {
	return c.svc.s.W().TxNamed(ctx, "sources.enqueue", func(tx *store.Tx) error {
		_, err := c.jobs.Enqueue(ctx, tx, CollectJob, map[string]string{"source_id": id}, jobs.EnqueueOptions{DedupKey: "collect:" + id})
		return err
	})
}

// CollectNow queues an immediate run (manual "collect now"), clearing a reauthorization state if credentials changed.
func (c *Collector) CollectNow(ctx context.Context, actor audit.Actor, id string) error {
	s, err := c.svc.Get(ctx, id)
	if err != nil {
		return err
	}
	if !s.Enabled {
		return apperr.New(apperr.CodeConflict, "the source is disabled")
	}
	if err := c.svc.s.W().TxNamed(ctx, "sources.collect_now", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE source_status SET next_run_at = NULL, retry_after = NULL WHERE source_id = ?`, id); err != nil {
			return err
		}
		_, err := c.svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "source.collect_requested", Target: audit.Target{Type: "source", ID: id}})
		return err
	}); err != nil {
		return err
	}
	return c.enqueue(ctx, id)
}

type runOutcome struct {
	state      string
	reason     string
	class      string
	retryAfter time.Duration
	success    bool
}

// run performs one collection for a source. Network I/O never happens inside a write transaction.
func (c *Collector) run(ctx context.Context, j jobs.Job) error {
	var p struct {
		SourceID string `json:"source_id"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent(err)
	}
	ctx = obs.WithID(ctx, obs.FieldSourceID, p.SourceID)
	src, err := c.svc.Get(ctx, p.SourceID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !src.Enabled {
		return nil
	}
	started := c.svc.now()
	out, latest := c.collect(ctx, src)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.opts.Metrics.CollectRun(src.Kind, out.state)
	return c.record(ctx, src, started, out, latest)
}

func (c *Collector) client(ctx context.Context, src Source) (*vwdataact.Client, error) {
	var userEnc, secEnc, sessEnc []byte
	var ref sql.NullString
	if err := c.svc.s.ReadDB().QueryRowContext(ctx, `SELECT username_enc, secret_enc, secret_ref, session_enc FROM sources WHERE id = ?`, src.ID).
		Scan(&userEnc, &secEnc, &ref, &sessEnc); err != nil {
		return nil, err
	}
	secret, err := c.svc.open(src.ID, "secret_enc", secEnc)
	if err != nil {
		return nil, err
	}
	if ref.Valid {
		b, err := c.svc.refs.Resolve(ref.String)
		if err != nil {
			return nil, &vwdataact.Error{Class: vwdataact.ClassAuth, Msg: "the secret reference cannot be resolved: " + err.Error()}
		}
		secret = strings.TrimSpace(string(b))
	}
	session, err := c.svc.open(src.ID, "session_enc", sessEnc)
	if err != nil {
		session = "" // a session sealed with a retired key: fall back to credentials
	}
	cfg := vwdataact.Config{BaseURL: c.opts.BaseURL, IdentityBase: c.opts.IdentityBase, Brand: src.Brand, StateKey: src.PortalBrandKey,
		Country: src.Country, Language: src.Language, HTTPClient: c.opts.HTTPClient, MinInterval: c.opts.MinInterval, MaxDownload: c.opts.MaxDownload,
		UserAgent: "WagenPulse/" + buildinfo.Get().Version + " (+https://github.com/aurimasniekis/go-wagenpulse)"}
	var login func(context.Context, *vwdataact.Client) error
	if src.AuthMode == AuthPassword {
		user, err := c.svc.open(src.ID, "username_enc", userEnc)
		if err != nil {
			return nil, err
		}
		if user == "" || secret == "" {
			return nil, &vwdataact.Error{Class: vwdataact.ClassAuth, Msg: "username and password are required"}
		}
		login = vwdataact.PasswordLogin(user, secret)
	}
	cl, err := vwdataact.New(cfg, login)
	if err != nil {
		return nil, err
	}
	switch {
	case session != "":
		_ = cl.SetCookies(session)
	case src.AuthMode == AuthSessionCookie:
		if secret == "" {
			return nil, &vwdataact.Error{Class: vwdataact.ClassAuth, Msg: "no session cookie is configured"}
		}
		if err := cl.SetCookies(secret); err != nil {
			return nil, err
		}
	default:
		if err := cl.Login(ctx); err != nil {
			return nil, err
		}
	}
	return cl, nil
}

type vehicleRef struct {
	num  int64
	id   string
	vin  string
	slug string
}

// collect lists and downloads new deliveries for every bound vehicle and request type, then creates imports.
func (c *Collector) collect(ctx context.Context, src Source) (runOutcome, *time.Time) {
	cl, err := c.client(ctx, src)
	if err != nil {
		return outcomeFor(err), nil
	}
	defer func() { c.saveSession(context.WithoutCancel(ctx), src.ID, cl.Cookies()) }()
	rows, err := c.svc.s.ReadDB().QueryContext(ctx, `SELECT v.num, v.id, v.vin, v.slug FROM source_vehicles sv JOIN vehicles v ON v.num = sv.vehicle_num
		WHERE sv.source_id = ? AND v.archived_at IS NULL ORDER BY v.id`, src.ID)
	if err != nil {
		return runOutcome{state: "error", reason: "database error", class: "internal"}, nil
	}
	var vehs []vehicleRef
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			var v vehicleRef
			if err := rows.Scan(&v.num, &v.id, &v.vin, &v.slug); err != nil {
				return err
			}
			vehs = append(vehs, v)
		}
		return rows.Err()
	}(); err != nil {
		return runOutcome{state: "error", class: "internal"}, nil
	}
	if len(vehs) == 0 {
		// Verify the session even without vehicles so the operator sees whether it works.
		if _, err := cl.Vehicles(ctx); err != nil {
			return outcomeFor(err), nil
		}
		return runOutcome{state: "authorized", reason: "no vehicles are bound to this source", success: true}, nil
	}
	types := []string{}
	if src.CollectContinuous {
		types = append(types, vwdataact.RequestContinuous)
	}
	if src.CollectHistorical {
		types = append(types, vwdataact.RequestHistorical)
	}
	var latest *time.Time
	stored := map[string][]ingest.StoredDelivery{}
	noRequest := 0
	for _, v := range vehs {
		for _, rt := range types {
			ds, newest, err := c.collectOne(ctx, src, cl, v, rt)
			if err != nil {
				if vwdataact.ClassOf(err) == vwdataact.ClassNoRequest {
					noRequest++
					continue
				}
				// Imports for what was stored so far still happen.
				c.createImports(ctx, src, stored)
				return outcomeFor(err), latest
			}
			if newest != nil && (latest == nil || newest.After(*latest)) {
				latest = newest
			}
			stored[rt] = append(stored[rt], ds...)
		}
	}
	c.createImports(ctx, src, stored)
	if noRequest == len(vehs)*len(types) {
		return runOutcome{state: "no_active_request", reason: "no active data request on the portal; create a continuous request in the portal (it renews about every 12 months)",
			class: vwdataact.ClassNoRequest, success: true}, latest
	}
	reason := ""
	if noRequest > 0 {
		reason = "some vehicles have no active data request on the portal"
	}
	return runOutcome{state: "authorized", reason: reason, success: true}, latest
}

// collectOne handles one (vehicle, request type): metadata → list → download new files.
func (c *Collector) collectOne(ctx context.Context, src Source, cl *vwdataact.Client, v vehicleRef, rt string) ([]ingest.StoredDelivery, *time.Time, error) {
	ident, err := cl.Identifier(ctx, v.vin, rt)
	if err != nil {
		return nil, nil, err
	}
	files, err := cl.List(ctx, v.vin, ident, rt)
	if err != nil {
		return nil, nil, err
	}
	var newest *time.Time
	var out []ingest.StoredDelivery
	n := 0
	for _, f := range files {
		if f.CreatedOn != nil && (newest == nil || f.CreatedOn.After(*newest)) {
			newest = f.CreatedOn
		}
		var seen int
		if err := c.svc.s.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM remote_files WHERE source_id = ? AND vin = ? AND request_type = ? AND name = ?
			AND outcome != 'failed'`, src.ID, v.vin, rt, f.Name).Scan(&seen); err != nil {
			return out, newest, err
		}
		if seen > 0 {
			continue
		}
		if strings.HasSuffix(f.Name, vwdataact.NoContentSuffix) {
			c.remember(ctx, src.ID, v.vin, rt, f, "no_content", 0)
			continue
		}
		if f.Size > c.opts.MaxDownload {
			c.remember(ctx, src.ID, v.vin, rt, f, "too_large", 0)
			continue
		}
		if n >= c.opts.MaxFilesPerRun {
			break // the rest is fetched by the next run (bounded work per run)
		}
		n++
		data, err := cl.Download(ctx, v.vin, ident, rt, f.Name)
		if err != nil {
			if vwdataact.ClassOf(err) == vwdataact.ClassTooLarge {
				c.remember(ctx, src.ID, v.vin, rt, f, "too_large", 0)
				continue
			}
			return out, newest, err
		}
		d, err := c.ingest.StoreRaw(ctx, bytes.NewReader(data), ingest.RawMeta{Origin: "collector", SourceID: src.ID, VehicleNum: v.num,
			OriginalName: f.Name, RequestType: rt, PublishedAt: f.CreatedOn, CreatedBy: src.ID, Limit: c.opts.MaxDownload})
		if err != nil {
			c.remember(ctx, src.ID, v.vin, rt, f, "failed", 0)
			return out, newest, err
		}
		outcome := "stored"
		if d.Duplicate {
			outcome = "duplicate" // the portal served bytes we already have (observed: identical content under new names, E1)
		}
		c.remember(ctx, src.ID, v.vin, rt, f, outcome, d.Num)
		out = append(out, d)
	}
	return out, newest, nil
}

func (c *Collector) remember(ctx context.Context, sourceID, vin, rt string, f vwdataact.File, outcome string, deliveryNum int64) {
	var created, dn any
	if f.CreatedOn != nil {
		created = f.CreatedOn.UnixMicro()
	}
	if deliveryNum > 0 {
		dn = deliveryNum
	}
	err := c.svc.s.W().TxNamed(ctx, "sources.remote_file", func(tx *store.Tx) error {
		_, err := tx.SQL.ExecContext(ctx, `INSERT INTO remote_files (source_id, vin, request_type, name, created_on, size, outcome, delivery_num, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (source_id, vin, request_type, name) DO UPDATE SET outcome = excluded.outcome,
			delivery_num = excluded.delivery_num, fetched_at = excluded.fetched_at`, sourceID, vin, rt, f.Name, created, f.Size, outcome, dn, tx.NowMicros())
		return err
	})
	if err != nil {
		c.log.WarnContext(ctx, "recording a remote file failed", obs.Err(err))
	}
}

// createImports creates one import per request type with the source's event policy.
func (c *Collector) createImports(ctx context.Context, src Source, stored map[string][]ingest.StoredDelivery) {
	for rt, ds := range stored {
		if len(ds) == 0 {
			continue
		}
		policy, backfill := src.LiveEventPolicy, false
		if rt == vwdataact.RequestHistorical {
			policy, backfill = src.HistoricalEventPolicy, true
		}
		if _, err := c.ingest.Create(context.WithoutCancel(ctx), ingest.CreateInput{Kind: ingest.KindCollection, SourceID: src.ID, EventPolicy: policy,
			Backfill: backfill, Deliveries: ds, Actor: audit.System}); err != nil {
			c.log.ErrorContext(ctx, "creating a collection import failed", obs.Err(err))
		}
	}
}

func (c *Collector) saveSession(ctx context.Context, id, cookies string) {
	if cookies == "" {
		return
	}
	sealed, err := c.svc.seal(id, "session_enc", cookies)
	if err != nil {
		return
	}
	_ = c.svc.s.W().TxNamed(ctx, "sources.session", func(tx *store.Tx) error {
		_, err := tx.SQL.ExecContext(ctx, `UPDATE sources SET session_enc = ? WHERE id = ?`, sealed, id)
		return err
	})
}

func outcomeFor(err error) runOutcome {
	var e *vwdataact.Error
	if !errors.As(err, &e) {
		return runOutcome{state: "error", reason: "collection failed; see server logs", class: "internal"}
	}
	switch e.Class {
	case vwdataact.ClassAuth, vwdataact.ClassChallenge:
		return runOutcome{state: "reauthorization_required", reason: e.Msg, class: e.Class}
	case vwdataact.ClassNoRequest:
		return runOutcome{state: "no_active_request", reason: e.Msg, class: e.Class, success: true}
	case vwdataact.ClassRateLimited:
		ra := e.RetryAfter
		if ra <= 0 {
			ra = 15 * time.Minute
		}
		return runOutcome{state: "rate_limited", reason: e.Msg, class: e.Class, retryAfter: ra}
	case vwdataact.ClassUnavailable:
		return runOutcome{state: "unavailable", reason: e.Msg, class: e.Class}
	default:
		return runOutcome{state: "error", reason: e.Msg, class: e.Class}
	}
}

// backoffLadder spaces retries after consecutive failures (5, 15, 30, 60 minutes; never faster than the interval).
var backoffLadder = []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

func (c *Collector) record(ctx context.Context, src Source, started time.Time, out runOutcome, latest *time.Time) error {
	interval := time.Duration(src.IntervalMinutes) * time.Minute
	return c.svc.s.W().TxNamed(ctx, "sources.record", func(tx *store.Tx) error {
		var failures int64
		_ = tx.SQL.QueryRowContext(ctx, `SELECT consecutive_failures FROM source_status WHERE source_id = ?`, src.ID).Scan(&failures)
		next := started.Add(interval)
		var retry any
		if out.success {
			failures = 0
			// Align to the portal cadence: the next delivery is expected one interval after the newest, plus a buffer.
			if latest != nil {
				if aligned := latest.Add(interval + time.Minute); aligned.After(started) && aligned.Before(next.Add(interval)) {
					next = aligned
				}
			}
		} else {
			failures++
			step := backoffLadder[min(int(failures)-1, len(backoffLadder)-1)]
			next = started.Add(max(step, interval))
			if out.retryAfter > 0 {
				next = started.Add(max(out.retryAfter, interval))
				retry = next.UnixMicro()
			}
		}
		var success, delivery any
		if out.success {
			success = tx.NowMicros()
		}
		if latest != nil {
			delivery = latest.UnixMicro()
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO source_status (source_id, state, state_reason, last_attempt_at, last_success_at, last_delivery_at,
			next_run_at, retry_after, consecutive_failures, last_error_class, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (source_id) DO UPDATE SET state = excluded.state, state_reason = excluded.state_reason, last_attempt_at = excluded.last_attempt_at,
			  last_success_at = coalesce(excluded.last_success_at, source_status.last_success_at),
			  last_delivery_at = coalesce(excluded.last_delivery_at, source_status.last_delivery_at), next_run_at = excluded.next_run_at,
			  retry_after = excluded.retry_after, consecutive_failures = excluded.consecutive_failures, last_error_class = excluded.last_error_class,
			  updated_at = excluded.updated_at`,
			src.ID, out.state, out.reason, started.UnixMicro(), success, delivery, next.UnixMicro(), retry, failures, out.class, tx.NowMicros()); err != nil {
			return err
		}
		if out.state == "reauthorization_required" {
			_, err := c.svc.audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "source.reauthorization_required",
				Target: audit.Target{Type: "source", ID: src.ID}, Outcome: audit.OutcomeFailure, ReasonCode: out.class})
			return err
		}
		return nil
	})
}

// UpdateLagMetrics exports per-source lag gauges.
func (c *Collector) UpdateLagMetrics(ctx context.Context) error {
	srcs, err := c.svc.List(ctx)
	if err != nil {
		return err
	}
	for _, s := range srcs {
		if s.Status != nil && s.Status.LastSuccessAt != nil {
			c.opts.Metrics.SetCollectLag(s.ID, c.svc.now().Sub(*s.Status.LastSuccessAt))
		}
	}
	return nil
}
