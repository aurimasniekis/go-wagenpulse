package ingest

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Import is the API view of an import.
type Import struct {
	ID                     string     `json:"id"`
	Kind                   string     `json:"kind" enum:"upload,collection,reprocess"`
	SourceID               string     `json:"source_id,omitempty"`
	VehicleID              string     `json:"vehicle_id,omitempty"`
	State                  string     `json:"state" enum:"queued,running,succeeded,partial,failed,cancelled"`
	EventPolicy            string     `json:"event_policy" enum:"none,preview,publish"`
	Backfill               bool       `json:"backfill"`
	FilesTotal             int64      `json:"files_total"`
	FilesDone              int64      `json:"files_done"`
	DeliveriesDuplicate    int64      `json:"deliveries_duplicate"`
	DeliveriesRejected     int64      `json:"deliveries_rejected"`
	DeliveriesUnrecognized int64      `json:"deliveries_unrecognized"`
	ObservationsAccepted   int64      `json:"observations_accepted"`
	ObservationsDuplicate  int64      `json:"observations_duplicate"`
	ObservationsRejected   int64      `json:"observations_rejected"`
	Corrections            int64      `json:"corrections"`
	UnknownSignals         int64      `json:"unknown_signals"`
	LateSkipped            int64      `json:"late_skipped" doc:"Observations older than rule state that were stored but did not fire rules."`
	EventsCreated          int64      `json:"events_created"`
	EventsPreviewed        int64      `json:"events_previewed"`
	CoverageFrom           *time.Time `json:"coverage_from,omitempty"`
	CoverageTo             *time.Time `json:"coverage_to,omitempty"`
	Warnings               []string   `json:"warnings"`
	ErrorCode              string     `json:"error_code,omitempty"`
	ErrorMessage           string     `json:"error_message,omitempty"`
	CreatedBy              string     `json:"created_by,omitempty"`
	CorrelationID          string     `json:"correlation_id"`
	CreatedAt              time.Time  `json:"created_at"`
	StartedAt              *time.Time `json:"started_at,omitempty"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at"`
	vehicleNum             int64
}

const importCols = `i.id, i.kind, coalesce(i.source_id, ''), coalesce(v.id, ''), coalesce(i.vehicle_num, 0), i.state, i.event_policy, i.backfill,
	i.files_total, i.files_done, i.deliveries_duplicate, i.deliveries_rejected, i.deliveries_unrecognized, i.observations_accepted,
	i.observations_duplicate, i.observations_rejected, i.corrections, i.unknown_signals, i.late_skipped, i.events_created, i.events_previewed,
	i.coverage_from, i.coverage_to, i.warnings_json, i.error_code, i.error_message, i.created_by, i.correlation_id, i.created_at, i.started_at,
	i.finished_at, i.updated_at`

func scanImport(r interface{ Scan(...any) error }) (Import, error) {
	var im Import
	var backfill int64
	var cf, ct, started, finished sql.NullInt64
	var warnings string
	var created, updated int64
	if err := r.Scan(&im.ID, &im.Kind, &im.SourceID, &im.VehicleID, &im.vehicleNum, &im.State, &im.EventPolicy, &backfill, &im.FilesTotal,
		&im.FilesDone, &im.DeliveriesDuplicate, &im.DeliveriesRejected, &im.DeliveriesUnrecognized, &im.ObservationsAccepted,
		&im.ObservationsDuplicate, &im.ObservationsRejected, &im.Corrections, &im.UnknownSignals, &im.LateSkipped, &im.EventsCreated,
		&im.EventsPreviewed, &cf, &ct, &warnings, &im.ErrorCode, &im.ErrorMessage, &im.CreatedBy, &im.CorrelationID, &created, &started,
		&finished, &updated); err != nil {
		return im, err
	}
	im.Backfill = backfill == 1
	im.CoverageFrom, im.CoverageTo, im.StartedAt, im.FinishedAt = tptr(cf), tptr(ct), tptr(started), tptr(finished)
	im.CreatedAt, im.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	im.Warnings = []string{}
	_ = json.Unmarshal([]byte(warnings), &im.Warnings)
	return im, nil
}

func tptr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMicro(v.Int64).UTC()
	return &t
}

// Get returns an import.
func (svc *Service) Get(ctx context.Context, id string) (Import, error) {
	im, err := scanImport(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+importCols+` FROM imports i LEFT JOIN vehicles v ON v.num = i.vehicle_num
		WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return im, ErrImportNotFound
	}
	return im, err
}

func (svc *Service) getTx(ctx context.Context, tx *store.Tx, id string) (Import, error) {
	im, err := scanImport(tx.SQL.QueryRowContext(ctx, `SELECT `+importCols+` FROM imports i LEFT JOIN vehicles v ON v.num = i.vehicle_num
		WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return im, ErrImportNotFound
	}
	return im, err
}

// ImportFilter selects imports (newest first).
type ImportFilter struct {
	VehicleNum  int64
	VehicleNums []int64 // restriction for vehicle-scoped tokens
	SourceID    string
	State       string
	Kind        string
	Before      string // cursor: imports with id < Before
	Limit       int
}

// List returns imports, newest first.
func (svc *Service) List(ctx context.Context, f ImportFilter) ([]Import, error) {
	q := `SELECT ` + importCols + ` FROM imports i LEFT JOIN vehicles v ON v.num = i.vehicle_num WHERE 1 = 1`
	var args []any
	if f.Before != "" {
		q += ` AND i.id < ?`
		args = append(args, f.Before)
	}
	if f.VehicleNum > 0 {
		q += ` AND i.vehicle_num = ?`
		args = append(args, f.VehicleNum)
	}
	if len(f.VehicleNums) > 0 {
		q += ` AND i.vehicle_num IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.VehicleNums)), ",") + `)`
		for _, n := range f.VehicleNums {
			args = append(args, n)
		}
	}
	if f.SourceID != "" {
		q += ` AND i.source_id = ?`
		args = append(args, f.SourceID)
	}
	if f.State != "" {
		q += ` AND i.state = ?`
		args = append(args, f.State)
	}
	if f.Kind != "" {
		q += ` AND i.kind = ?`
		args = append(args, f.Kind)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q += ` ORDER BY i.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Import{}
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

// RawDelivery is the API view of a raw delivery.
type RawDelivery struct {
	ID               string     `json:"id"`
	SHA256           string     `json:"sha256"`
	Size             int64      `json:"size"`
	Origin           string     `json:"origin" enum:"upload,collector"`
	SourceID         string     `json:"source_id,omitempty"`
	VehicleID        string     `json:"vehicle_id,omitempty"`
	OriginalName     string     `json:"original_name,omitempty"`
	RequestType      string     `json:"request_type,omitempty"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	ReceivedAt       time.Time  `json:"received_at"`
	Format           string     `json:"format"`
	Fingerprint      string     `json:"fingerprint,omitempty"`
	Status           string     `json:"status" enum:"stored,processed,rejected,unrecognized,purged"`
	ObservationCount int64      `json:"observation_count"`
	TimeFrom         *time.Time `json:"time_from,omitempty"`
	TimeTo           *time.Time `json:"time_to,omitempty"`
	Error            string     `json:"error,omitempty"`
	BlobPresent      bool       `json:"raw_content_retained"`
	RetentionUntil   *time.Time `json:"retention_until,omitempty"`
	// Outcome and counts are set when listed within an import.
	Outcome    string `json:"outcome,omitempty"`
	Accepted   int64  `json:"accepted,omitempty"`
	Duplicates int64  `json:"duplicates,omitempty"`
	Message    string `json:"message,omitempty"`
}

const deliveryCols = `d.id, d.sha256, d.size, d.origin, coalesce(d.source_id, ''), coalesce(v.id, ''), d.original_name, coalesce(d.request_type, ''),
	d.published_at, d.received_at, d.format, d.fingerprint, d.status, d.observation_count, d.time_from, d.time_to, d.error, d.blob_present,
	d.retention_until`

func scanDelivery(r interface{ Scan(...any) error }, extra ...any) (RawDelivery, error) {
	var d RawDelivery
	var sha []byte
	var pub, from, to, ret sql.NullInt64
	var recv, present int64
	dest := []any{&d.ID, &sha, &d.Size, &d.Origin, &d.SourceID, &d.VehicleID, &d.OriginalName, &d.RequestType, &pub, &recv, &d.Format, &d.Fingerprint,
		&d.Status, &d.ObservationCount, &from, &to, &d.Error, &present, &ret}
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return d, err
	}
	d.SHA256 = hex.EncodeToString(sha)
	d.PublishedAt, d.TimeFrom, d.TimeTo, d.RetentionUntil = tptr(pub), tptr(from), tptr(to), tptr(ret)
	d.ReceivedAt = time.UnixMicro(recv).UTC()
	d.BlobPresent = present == 1
	return d, nil
}

// ImportDeliveries lists the deliveries of an import in order.
func (svc *Service) ImportDeliveries(ctx context.Context, id string) ([]RawDelivery, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT `+deliveryCols+`, i.outcome, i.accepted, i.duplicates, i.message
		FROM import_deliveries i JOIN raw_deliveries d ON d.num = i.delivery_num LEFT JOIN vehicles v ON v.num = d.vehicle_num
		WHERE i.import_id = ? ORDER BY i.position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RawDelivery{}
	for rows.Next() {
		var d RawDelivery
		var outcome, msg string
		var acc, dup int64
		d, err := scanDelivery(rows, &outcome, &acc, &dup, &msg)
		if err != nil {
			return nil, err
		}
		d.Outcome, d.Accepted, d.Duplicates, d.Message = outcome, acc, dup, msg
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryFilter selects deliveries (newest first).
type DeliveryFilter struct {
	VehicleNum int64
	Status     string
	Before     int64
	Limit      int
}

// Deliveries lists raw deliveries.
func (svc *Service) Deliveries(ctx context.Context, f DeliveryFilter) ([]RawDelivery, int64, error) {
	q := `SELECT ` + deliveryCols + `, d.num FROM raw_deliveries d LEFT JOIN vehicles v ON v.num = d.vehicle_num WHERE 1 = 1`
	var args []any
	if f.Before > 0 {
		q += ` AND d.num < ?`
		args = append(args, f.Before)
	}
	if f.VehicleNum > 0 {
		q += ` AND d.vehicle_num = ?`
		args = append(args, f.VehicleNum)
	}
	if f.Status != "" {
		q += ` AND d.status = ?`
		args = append(args, f.Status)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q += ` ORDER BY d.num DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RawDelivery{}
	var last int64
	for rows.Next() {
		var num int64
		d, err := scanDelivery(rows, &num)
		if err != nil {
			return nil, 0, err
		}
		last = num
		out = append(out, d)
	}
	if len(out) < f.Limit {
		last = 0
	}
	return out, last, rows.Err()
}
