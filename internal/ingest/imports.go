package ingest

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// JobKind is the job that processes an import.
const JobKind = "import.process"

// Import kinds.
const (
	KindUpload     = "upload"
	KindCollection = "collection"
	KindReprocess  = "reprocess"
)

// EvalInput describes newly accepted observations for rule evaluation (ADR-0015, ADR-0017).
type EvalInput struct {
	VehicleNum    int64
	VehicleID     string
	VehicleSlug   string
	Accepted      []telemetry.Accepted
	Policy        string // none | preview | publish
	Backfill      bool
	ImportID      string
	DeliveryID    string
	DeliverySHA   string
	SourceID      string
	CorrelationID string
}

// EvalResult reports rule outcomes.
type EvalResult struct {
	Created     int
	Previewed   int
	LateSkipped int
}

// EventSink evaluates rules inside the ingest transaction (implemented by the rules package).
type EventSink interface {
	Evaluate(ctx context.Context, tx *store.Tx, in EvalInput) (EvalResult, error)
}

// Options configure the service.
type Options struct {
	Limits    Limits
	BatchSize int
	Clock     clock.Clock
	Log       *slog.Logger
	Metrics   *metrics.Metrics
}

// Service manages raw deliveries and imports.
type Service struct {
	s        *store.Store
	blobs    *Blobs
	audit    *audit.Writer
	jobs     *jobs.Runner
	settings *settings.Service
	writer   telemetry.Writer
	sink     EventSink
	opts     Options
	log      *slog.Logger
}

// New builds the service and registers the import job.
func New(s *store.Store, b *Blobs, a *audit.Writer, j *jobs.Runner, st *settings.Service, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Log == nil {
		opts.Log = obs.Discard()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 2000
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits
	}
	svc := &Service{s: s, blobs: b, audit: a, jobs: j, settings: st, opts: opts, log: obs.Component(opts.Log, "ingest")}
	j.Register(JobKind, 5, 30*time.Minute, svc.runJob)
	return svc
}

// SetSink installs the rule evaluator.
func (svc *Service) SetSink(s EventSink) { svc.sink = s }

// Blobs returns the blob store.
func (svc *Service) Blobs() *Blobs { return svc.blobs }

// RawMeta describes a raw delivery being stored.
type RawMeta struct {
	Origin       string // upload | collector
	SourceID     string
	VehicleNum   int64
	OriginalName string
	RequestType  string
	PublishedAt  *time.Time
	CreatedBy    string
	Limit        int64
}

// StoredDelivery is the result of StoreRaw.
type StoredDelivery struct {
	Num       int64
	ID        string
	Duplicate bool
	SHA256    string
	Size      int64
}

// StoreRaw writes raw bytes to the content-addressed store and records a delivery (deduplicated by SHA-256).
func (svc *Service) StoreRaw(ctx context.Context, r io.Reader, m RawMeta) (StoredDelivery, error) {
	sum, size, err := svc.blobs.Put(r, m.Limit)
	if errors.Is(err, ErrTooLarge) {
		return StoredDelivery{}, apperr.New(apperr.CodeInvalid, "the file is larger than the upload limit (%d MiB)", m.Limit>>20)
	}
	if err != nil {
		return StoredDelivery{}, err
	}
	ret, err := svc.settings.Retention(ctx)
	if err != nil {
		return StoredDelivery{}, err
	}
	out := StoredDelivery{SHA256: hex.EncodeToString(sum), Size: size}
	err = svc.s.W().TxNamed(ctx, "ingest.store_raw", func(tx *store.Tx) error {
		var num int64
		var id string
		var present int64
		err := tx.SQL.QueryRowContext(ctx, `SELECT num, id, blob_present FROM raw_deliveries WHERE sha256 = ?`, sum).Scan(&num, &id, &present)
		if err == nil {
			out.Num, out.ID, out.Duplicate = num, id, true
			if present == 0 {
				// The blob had been removed by retention; it is present again now.
				_, err = tx.SQL.ExecContext(ctx, `UPDATE raw_deliveries SET blob_present = 1, retention_until = ? WHERE num = ?`,
					retentionUntil(tx.Now, ret.Value.RawDeliveriesDays), num)
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out.ID = ids.New(ids.Delivery)
		var pub any
		if m.PublishedAt != nil {
			pub = m.PublishedAt.UnixMicro()
		}
		var src, veh, rt any
		if m.SourceID != "" {
			src = m.SourceID
		}
		if m.VehicleNum > 0 {
			veh = m.VehicleNum
		}
		if m.RequestType != "" {
			rt = m.RequestType
		}
		r, err := tx.SQL.ExecContext(ctx, `INSERT INTO raw_deliveries (id, sha256, size, origin, source_id, vehicle_num, original_name, request_type,
			published_at, received_at, status, retention_until, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'stored', ?, ?)`,
			out.ID, sum, size, m.Origin, src, veh, SanitizeName(m.OriginalName), rt, pub, tx.NowMicros(),
			retentionUntil(tx.Now, ret.Value.RawDeliveriesDays), m.CreatedBy)
		if err != nil {
			return err
		}
		out.Num, err = r.LastInsertId()
		return err
	})
	return out, err
}

func retentionUntil(now time.Time, days int) any {
	if days <= 0 {
		return nil
	}
	return now.Add(time.Duration(days) * 24 * time.Hour).UnixMicro()
}

// CreateInput creates an import over stored deliveries.
type CreateInput struct {
	Kind          string
	SourceID      string
	VehicleNum    int64
	EventPolicy   string
	Backfill      bool
	Deliveries    []StoredDelivery
	Actor         audit.Actor
	CorrelationID string
}

// Create records an import and enqueues its processing job. Deliveries already processed by an earlier import are
// still listed (as duplicates) so the import history shows them; reprocessing is an explicit kind.
func (svc *Service) Create(ctx context.Context, in CreateInput) (Import, error) {
	if !settings.ValidEventPolicy(in.EventPolicy) {
		return Import{}, apperr.New(apperr.CodeInvalid, "event_policy must be none, preview or publish")
	}
	id := ids.New(ids.Import)
	if in.CorrelationID == "" {
		in.CorrelationID = ids.New(ids.Correlation)
	}
	err := svc.s.W().TxNamed(ctx, "ingest.create_import", func(tx *store.Tx) error {
		var src, veh any
		if in.SourceID != "" {
			src = in.SourceID
		}
		if in.VehicleNum > 0 {
			veh = in.VehicleNum
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO imports (id, kind, source_id, vehicle_num, state, event_policy, backfill, files_total,
			created_by, correlation_id, created_at, updated_at) VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?)`, id, in.Kind, src, veh,
			in.EventPolicy, b2i(in.Backfill), len(in.Deliveries), in.Actor.ID, in.CorrelationID, tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
		for i, d := range in.Deliveries {
			outcome := "pending"
			if d.Duplicate && in.Kind != KindReprocess {
				var processed int
				if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM raw_deliveries WHERE num = ? AND status IN ('processed')`, d.Num).Scan(&processed); err != nil {
					return err
				}
				if processed > 0 {
					outcome = "duplicate"
				}
			}
			if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO import_deliveries (import_id, delivery_num, position, outcome) VALUES (?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, id, d.Num, i, outcome); err != nil {
				return err
			}
		}
		if _, err := svc.jobs.Enqueue(ctx, tx, JobKind, map[string]string{"import_id": id}, jobs.EnqueueOptions{DedupKey: "import:" + id,
			CorrelationID: in.CorrelationID}); err != nil {
			return err
		}
		if in.Kind == KindUpload || in.Kind == KindReprocess {
			_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: in.Actor, Action: "import.created", Target: audit.Target{Type: "import", ID: id},
				Details: map[string]any{"kind": in.Kind, "files": len(in.Deliveries), "event_policy": in.EventPolicy}})
			return err
		}
		return nil
	})
	if err != nil {
		return Import{}, err
	}
	return svc.Get(ctx, id)
}

type jobPayload struct {
	ImportID string `json:"import_id"`
}

// runJob processes an import's pending deliveries. It is restart-safe: deliveries are checkpointed individually and
// observation writes are idempotent, so a crash mid-delivery re-processes it without creating duplicates.
func (svc *Service) runJob(ctx context.Context, j jobs.Job) error {
	var p jobPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent(err)
	}
	ctx = obs.WithID(ctx, obs.FieldImportID, p.ImportID)
	imp, err := svc.Get(ctx, p.ImportID)
	if err != nil {
		return jobs.Permanent(err)
	}
	if imp.State == "cancelled" || imp.State == "succeeded" || imp.State == "failed" || imp.State == "partial" {
		return nil
	}
	if err := svc.s.W().TxNamed(ctx, "ingest.import_start", func(tx *store.Tx) error {
		_, err := tx.SQL.ExecContext(ctx, `UPDATE imports SET state = 'running', started_at = coalesce(started_at, ?), updated_at = ? WHERE id = ?
			AND state IN ('queued', 'running')`, tx.NowMicros(), tx.NowMicros(), imp.ID)
		return err
	}); err != nil {
		return err
	}
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT d.num, d.id, d.sha256, d.size, d.origin, coalesce(d.source_id, ''), coalesce(d.vehicle_num, 0),
		d.published_at, d.received_at FROM import_deliveries i JOIN raw_deliveries d ON d.num = i.delivery_num
		WHERE i.import_id = ? AND i.outcome = 'pending' ORDER BY i.position`, imp.ID)
	if err != nil {
		return err
	}
	var pending []deliveryRow
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			var d deliveryRow
			var pub sql.NullInt64
			if err := rows.Scan(&d.num, &d.id, &d.sha, &d.size, &d.origin, &d.sourceID, &d.vehicleNum, &pub, &d.receivedAt); err != nil {
				return err
			}
			if pub.Valid {
				t := time.UnixMicro(pub.Int64).UTC()
				d.publishedAt = &t
			}
			pending = append(pending, d)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	for _, d := range pending {
		if cur, err := svc.Get(ctx, imp.ID); err == nil && cur.State == "cancelled" {
			return nil
		}
		if err := svc.processDelivery(ctx, imp, d); err != nil {
			if ctx.Err() != nil {
				return err
			}
			// Unexpected (storage) errors: retry the job; rejected content was recorded and does not error.
			svc.log.ErrorContext(ctx, "processing a delivery failed", "delivery", d.id, obs.Err(err))
			return err
		}
	}
	return svc.finish(ctx, imp.ID)
}

type deliveryRow struct {
	num         int64
	id          string
	sha         []byte
	size        int64
	origin      string
	sourceID    string
	vehicleNum  int64
	publishedAt *time.Time
	receivedAt  int64
}

// counts accumulates per-delivery outcomes.
type counts struct {
	accepted, duplicates, corrections, unknown, late, created, previewed int
	from, to                                                             time.Time
}

func (svc *Service) processDelivery(ctx context.Context, imp Import, d deliveryRow) error {
	start := time.Now()
	f, err := svc.blobs.Open(d.sha)
	if err != nil {
		return svc.markDelivery(ctx, imp, d, "rejected", 0, &RejectError{Code: "blob_missing", Detail: "the raw content is no longer stored (retention)"}, nil, counts{})
	}
	docs, rerr := ReadDelivery(f, d.size, svc.opts.Limits)
	_ = f.Close()
	if rerr != nil {
		var re *RejectError
		if errors.As(rerr, &re) {
			svc.opts.Metrics.IngestDelivery(d.origin, "rejected", time.Since(start))
			return svc.markDelivery(ctx, imp, d, "rejected", 0, re, nil, counts{})
		}
		return rerr
	}
	var c counts
	var parsedAny bool
	var format, fp, summary string
	// The import's explicit target wins over whatever an earlier import recorded for identical content.
	vehicleNum := imp.vehicleNum
	if vehicleNum == 0 {
		vehicleNum = d.vehicleNum
	}
	var warnings []string
	for _, doc := range docs {
		p, err := Parse(doc)
		if err != nil {
			var re *RejectError
			if errors.As(err, &re) {
				return svc.markDelivery(ctx, imp, d, "rejected", vehicleNum, re, nil, c)
			}
			return err
		}
		fp, summary = p.Fingerprint, p.Summary
		warnings = append(warnings, p.Warnings...)
		if p.Format != FormatContinuousV1 {
			format = p.Format
			continue
		}
		format, parsedAny = p.Format, true
		veh, rej, err := svc.resolveVehicle(ctx, vehicleNum, p.VIN)
		if err != nil {
			return err
		}
		if rej != nil {
			return svc.markDelivery(ctx, imp, d, "rejected", vehicleNum, rej, warnings, c)
		}
		vehicleNum = veh.Num
		fallback := time.UnixMicro(d.receivedAt).UTC()
		if p.CapturedAt != nil {
			fallback = *p.CapturedAt
		} else if d.publishedAt != nil {
			fallback = *d.publishedAt
		}
		if err := svc.writeRecords(ctx, imp, d, veh, p.Records, fallback, &c); err != nil {
			return err
		}
	}
	if !parsedAny {
		svc.opts.Metrics.IngestDelivery(d.origin, "unrecognized", time.Since(start))
		return svc.markDelivery(ctx, imp, d, "unrecognized", vehicleNum, &RejectError{Code: "unrecognized_format",
			Detail: "the structure is not a verified format (fingerprint " + fp + "); it is stored and can be reprocessed when a parser exists. Structure: " + summary},
			warnings, c)
	}
	svc.opts.Metrics.IngestDelivery(d.origin, "processed", time.Since(start))
	svc.opts.Metrics.IngestObservations("accepted", c.accepted)
	svc.opts.Metrics.IngestObservations("duplicate", c.duplicates)
	svc.opts.Metrics.IngestObservations("correction", c.corrections)
	_ = format
	return svc.markDelivery(ctx, imp, d, "processed", vehicleNum, nil, warnings, c)
}

// resolveVehicle matches the delivery's VIN to the target vehicle (or finds it by VIN).
func (svc *Service) resolveVehicle(ctx context.Context, vehicleNum int64, vin string) (vehicles.Vehicle, *RejectError, error) {
	if vehicleNum > 0 {
		var v vehicles.Vehicle
		var archived sql.NullInt64
		err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT num, id, slug, vin, archived_at FROM vehicles WHERE num = ?`, vehicleNum).
			Scan(&v.Num, &v.ID, &v.Slug, &v.VIN, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return v, &RejectError{Code: "vehicle_missing", Detail: "the target vehicle no longer exists"}, nil
		}
		if err != nil {
			return v, nil, err
		}
		if archived.Valid {
			return v, &RejectError{Code: "vehicle_archived", Detail: "the target vehicle is archived"}, nil
		}
		if vin != "" && !strings.EqualFold(vin, v.VIN) {
			return v, &RejectError{Code: "vin_mismatch", Detail: "the delivery belongs to a different vehicle (VIN …" + lastN(vin, 6) + ")"}, nil
		}
		return v, nil, nil
	}
	if vin == "" {
		return vehicles.Vehicle{}, &RejectError{Code: "vehicle_unknown", Detail: "the delivery has no VIN; choose the vehicle when uploading"}, nil
	}
	v, err := vehicles.ByVIN(ctx, svc.s.ReadDB(), vin)
	if errors.Is(err, vehicles.ErrNotFound) {
		return v, &RejectError{Code: "vehicle_unknown", Detail: "no active vehicle has VIN …" + lastN(vin, 6) + "; add the vehicle first"}, nil
	}
	return v, nil, err
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// writeRecords normalizes and writes records in transactions of at most BatchSize observations, evaluating rules in
// the same transaction.
func (svc *Service) writeRecords(ctx context.Context, imp Import, d deliveryRow, veh vehicles.Vehicle, recs []Record, fallback time.Time, c *counts) error {
	cache := newSignalCache()
	for start := 0; start < len(recs); start += svc.opts.BatchSize {
		end := min(start+svc.opts.BatchSize, len(recs))
		batch := recs[start:end]
		var res telemetry.WriteResult
		var ev EvalResult
		var created int
		err := svc.s.W().TxNamed(ctx, "ingest.write_batch", func(tx *store.Tx) error {
			before := cache.created
			obsBatch := make([]telemetry.Observation, 0, len(batch))
			for _, r := range batch {
				m, err := cache.resolve(ctx, tx, r)
				if err != nil {
					return err
				}
				o := telemetry.Observation{SignalNum: m.num, TimeBasis: telemetry.TimeObserved, Value: normalize(r, m)}
				if r.Timestamp != nil {
					o.ObservedAt = *r.Timestamp
				} else {
					o.ObservedAt, o.TimeBasis = fallback, telemetry.TimeDelivery
				}
				obsBatch = append(obsBatch, o)
			}
			var err error
			if res, err = svc.writer.Write(ctx, tx, veh.Num, d.num, time.UnixMicro(d.receivedAt), obsBatch); err != nil {
				return err
			}
			created = cache.created - before
			if svc.sink != nil && imp.EventPolicy != "none" && len(res.Accepted) > 0 {
				if ev, err = svc.sink.Evaluate(ctx, tx, EvalInput{VehicleNum: veh.Num, VehicleID: veh.ID, VehicleSlug: veh.Slug, Accepted: res.Accepted,
					Policy: imp.EventPolicy, Backfill: imp.Backfill, ImportID: imp.ID, DeliveryID: d.id, DeliverySHA: hex.EncodeToString(d.sha),
					SourceID: d.sourceID, CorrelationID: imp.CorrelationID}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			cache = newSignalCache() // the transaction rolled back: cached inserts are gone
			return err
		}
		c.accepted += len(res.Accepted)
		c.duplicates += res.Duplicates
		c.corrections += res.Corrections
		c.unknown += created
		c.created += ev.Created
		c.previewed += ev.Previewed
		c.late += ev.LateSkipped
		if !res.From.IsZero() && (c.from.IsZero() || res.From.Before(c.from)) {
			c.from = res.From
		}
		if res.To.After(c.to) {
			c.to = res.To
		}
	}
	return nil
}

// markDelivery checkpoints one delivery's outcome and updates the import's counters.
func (svc *Service) markDelivery(ctx context.Context, imp Import, d deliveryRow, outcome string, vehicleNum int64, rej *RejectError, warnings []string, c counts) error {
	msg := ""
	status := map[string]string{"processed": "processed", "rejected": "rejected", "unrecognized": "unrecognized"}[outcome]
	if rej != nil {
		msg = rej.Code + ": " + rej.Detail
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
	}
	return svc.s.W().TxNamed(ctx, "ingest.checkpoint", func(tx *store.Tx) error {
		var veh any
		if vehicleNum > 0 && outcome != "rejected" {
			veh = vehicleNum
		}
		var from, to any
		if !c.from.IsZero() {
			from, to = c.from.UnixMicro(), c.to.UnixMicro()
		}
		format := FormatContinuousV1
		switch outcome {
		case "unrecognized":
			format = FormatUnrecognized
		case "rejected":
			format = "rejected"
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE raw_deliveries SET status = ?, format = ?, error = ?, vehicle_num = coalesce(?, vehicle_num),
			observation_count = observation_count + ?, time_from = coalesce(min(coalesce(time_from, ?), ?), ?), time_to = coalesce(max(coalesce(time_to, ?), ?), ?)
			WHERE num = ?`, status, format, msg, veh, c.accepted, from, from, from, to, to, to, d.num); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE import_deliveries SET outcome = ?, accepted = ?, duplicates = ?, message = ? WHERE import_id = ? AND delivery_num = ?`,
			outcome, c.accepted, c.duplicates, msg, imp.ID, d.num); err != nil {
			return err
		}
		var rejected, unrec int
		switch outcome {
		case "rejected":
			rejected = 1
		case "unrecognized":
			unrec = 1
		}
		var wj any
		if len(warnings) > 0 || msg != "" {
			ws := append([]string{}, warnings...)
			if msg != "" {
				ws = append(ws, msg)
			}
			b, _ := json.Marshal(ws)
			wj = string(b)
		}
		_, err := tx.SQL.ExecContext(ctx, `UPDATE imports SET files_done = files_done + 1, deliveries_rejected = deliveries_rejected + ?,
			deliveries_unrecognized = deliveries_unrecognized + ?, observations_accepted = observations_accepted + ?,
			observations_duplicate = observations_duplicate + ?, corrections = corrections + ?, unknown_signals = unknown_signals + ?,
			late_skipped = late_skipped + ?, events_created = events_created + ?, events_previewed = events_previewed + ?,
			coverage_from = CASE WHEN ? IS NULL THEN coverage_from ELSE min(coalesce(coverage_from, ?), ?) END,
			coverage_to = CASE WHEN ? IS NULL THEN coverage_to ELSE max(coalesce(coverage_to, ?), ?) END,
			vehicle_num = coalesce(vehicle_num, ?),
			warnings_json = CASE WHEN ? IS NULL THEN warnings_json ELSE (SELECT json_group_array(value) FROM (SELECT value FROM json_each(warnings_json)
			  UNION SELECT value FROM json_each(?) LIMIT 50)) END,
			updated_at = ? WHERE id = ?`,
			rejected, unrec, c.accepted, c.duplicates, c.corrections, c.unknown, c.late, c.created, c.previewed,
			from, from, from, to, to, to, veh, wj, wj, tx.NowMicros(), imp.ID)
		return err
	})
}

// finish sets the final state from the per-delivery outcomes.
func (svc *Service) finish(ctx context.Context, id string) error {
	return svc.s.W().TxNamed(ctx, "ingest.import_finish", func(tx *store.Tx) error {
		var total, processed, dup, bad, files int
		if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(outcome = 'processed'), 0), coalesce(sum(outcome = 'duplicate'), 0),
			coalesce(sum(outcome IN ('rejected', 'unrecognized')), 0), (SELECT files_total FROM imports WHERE id = ?)
			FROM import_deliveries WHERE import_id = ?`, id, id).Scan(&total, &processed, &dup, &bad, &files); err != nil {
			return err
		}
		// Identical files repeated within one import share a delivery row; they count as duplicates too.
		dup += max(0, files-total)
		state := "succeeded"
		switch {
		case total > 0 && bad == total:
			state = "failed"
		case bad > 0:
			state = "partial"
		}
		_, err := tx.SQL.ExecContext(ctx, `UPDATE imports SET state = ?, deliveries_duplicate = ?, files_done = files_total, finished_at = ?, updated_at = ?
			WHERE id = ? AND state IN ('queued', 'running')`, state, dup, tx.NowMicros(), tx.NowMicros(), id)
		return err
	})
}

// Cancel stops a queued or running import between deliveries/batches. Data already written stays (idempotent).
func (svc *Service) Cancel(ctx context.Context, actor audit.Actor, id string) error {
	return svc.s.W().TxNamed(ctx, "ingest.import_cancel", func(tx *store.Tx) error {
		r, err := tx.SQL.ExecContext(ctx, `UPDATE imports SET state = 'cancelled', finished_at = ?, updated_at = ? WHERE id = ? AND state IN ('queued', 'running')`,
			tx.NowMicros(), tx.NowMicros(), id)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			if _, err := svc.getTx(ctx, tx, id); err != nil {
				return err
			}
			return apperr.New(apperr.CodeConflict, "only queued or running imports can be cancelled")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "import.cancelled", Target: audit.Target{Type: "import", ID: id}})
		return err
	})
}

// Retry re-queues the rejected/unrecognized/pending deliveries of a finished import (for example after a parser
// update or a transient failure). Processed deliveries are not touched.
func (svc *Service) Retry(ctx context.Context, actor audit.Actor, id string) (Import, error) {
	err := svc.s.W().TxNamed(ctx, "ingest.import_retry", func(tx *store.Tx) error {
		imp, err := svc.getTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if imp.State == "queued" || imp.State == "running" {
			return apperr.New(apperr.CodeConflict, "the import is still in progress")
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE import_deliveries SET outcome = 'pending', message = '' WHERE import_id = ?
			AND outcome IN ('pending', 'rejected', 'unrecognized')`, id); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE imports SET state = 'queued', finished_at = NULL, error_code = '', error_message = '',
			deliveries_rejected = 0, deliveries_unrecognized = 0, files_done = (SELECT count(*) FROM import_deliveries WHERE import_id = ? AND outcome != 'pending'),
			updated_at = ? WHERE id = ?`, id, tx.NowMicros(), id); err != nil {
			return err
		}
		if _, err := svc.jobs.Enqueue(ctx, tx, JobKind, map[string]string{"import_id": id}, jobs.EnqueueOptions{DedupKey: "import:" + id,
			CorrelationID: imp.CorrelationID}); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "import.retried", Target: audit.Target{Type: "import", ID: id}})
		return err
	})
	if err != nil {
		return Import{}, err
	}
	return svc.Get(ctx, id)
}

// Reprocess creates a reprocess import over existing deliveries (by ID). Observations are idempotent; the event
// policy defaults to none so reprocessing never floods destinations (ADR-0017).
func (svc *Service) Reprocess(ctx context.Context, actor audit.Actor, deliveryIDs []string, policy string) (Import, error) {
	if len(deliveryIDs) == 0 || len(deliveryIDs) > 1000 {
		return Import{}, apperr.New(apperr.CodeInvalid, "select 1..1000 deliveries")
	}
	if policy == "" {
		policy = settings.EventPolicyNone
	}
	var ds []StoredDelivery
	for _, id := range deliveryIDs {
		var d StoredDelivery
		var present int
		err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT num, id, blob_present FROM raw_deliveries WHERE id = ?`, id).Scan(&d.Num, &d.ID, &present)
		if errors.Is(err, sql.ErrNoRows) {
			return Import{}, apperr.New(apperr.CodeNotFound, "delivery %s not found", id)
		}
		if err != nil {
			return Import{}, err
		}
		if present == 0 {
			return Import{}, apperr.New(apperr.CodeConflict, "delivery %s: raw content was removed by retention", id)
		}
		d.Duplicate = true
		ds = append(ds, d)
	}
	return svc.Create(ctx, CreateInput{Kind: KindReprocess, EventPolicy: policy, Backfill: true, Deliveries: ds, Actor: actor})
}

// ErrImportNotFound is returned for unknown imports.
var ErrImportNotFound = apperr.New(apperr.CodeNotFound, "import not found")

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// String for debugging.
func (d deliveryRow) String() string { return fmt.Sprintf("delivery %s", d.id) }
