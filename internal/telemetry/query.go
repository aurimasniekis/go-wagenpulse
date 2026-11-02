package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Query limits (docs/STORAGE.md).
const (
	MaxRange     = 366 * 24 * time.Hour
	MaxPoints    = 5000
	MaxExportRow = 100000
)

// Reader answers telemetry queries from the reader pool.
type Reader struct {
	s *store.Store
}

// NewReader returns a reader.
func NewReader(s *store.Store) *Reader { return &Reader{s: s} }

// Signal is signal metadata as returned by the API.
type Signal struct {
	Key          string     `json:"key"`
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Unit         string     `json:"unit,omitempty"`
	UnitRaw      string     `json:"unit_raw,omitempty"`
	DataType     string     `json:"data_type,omitempty"`
	ValueKind    string     `json:"value_kind"`
	Clusters     []string   `json:"clusters"`
	Origin       string     `json:"origin" enum:"continuous,historical,observed"`
	ObservedName string     `json:"observed_name,omitempty" doc:"dataFieldName seen in deliveries when it differs from the dictionary name."`
	FirstSeenAt  *time.Time `json:"first_seen_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	Num          int64      `json:"-"`
}

const signalCols = `num, key, name, description, unit, unit_raw, data_type_raw, value_kind, clusters_json, origin, observed_name, first_seen_at, last_seen_at`

func scanSignal(r interface{ Scan(...any) error }) (Signal, error) {
	var s Signal
	var cl string
	var first, last sql.NullInt64
	if err := r.Scan(&s.Num, &s.Key, &s.Name, &s.Description, &s.Unit, &s.UnitRaw, &s.DataType, &s.ValueKind, &cl, &s.Origin, &s.ObservedName,
		&first, &last); err != nil {
		return s, err
	}
	s.Clusters = []string{}
	_ = json.Unmarshal([]byte(cl), &s.Clusters)
	if first.Valid {
		t := time.UnixMicro(first.Int64).UTC()
		s.FirstSeenAt = &t
	}
	if last.Valid {
		t := time.UnixMicro(last.Int64).UTC()
		s.LastSeenAt = &t
	}
	return s, nil
}

// ErrSignalNotFound is returned for unknown signal keys.
var ErrSignalNotFound = apperr.New(apperr.CodeNotFound, "signal not found")

// SignalByKey returns a signal.
func (r *Reader) SignalByKey(ctx context.Context, key string) (Signal, error) {
	s, err := scanSignal(r.s.ReadDB().QueryRowContext(ctx, `SELECT `+signalCols+` FROM signals WHERE key = ?`, strings.ToLower(key)))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrSignalNotFound
	}
	return s, err
}

// SignalFilter selects signals.
type SignalFilter struct {
	Query       string // substring of name, key or description
	Cluster     string
	Origin      string
	ObservedFor int64 // vehicle num: only signals with data for this vehicle (0 = any)
	OnlySeen    bool  // only signals with any observation
	After       string
	Limit       int
}

// Signals lists signals ordered by key.
func (r *Reader) Signals(ctx context.Context, f SignalFilter) ([]Signal, error) {
	q := `SELECT ` + signalCols + ` FROM signals WHERE key > ?`
	args := []any{f.After}
	if f.Query != "" {
		like := "%" + strings.NewReplacer("%", `\%`, "_", `\_`).Replace(strings.ToLower(f.Query)) + "%"
		q += ` AND (lower(name) LIKE ? ESCAPE '\' OR key LIKE ? ESCAPE '\' OR lower(description) LIKE ? ESCAPE '\' OR lower(observed_name) LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like)
	}
	if f.Cluster != "" {
		q += ` AND EXISTS (SELECT 1 FROM json_each(clusters_json) WHERE value = ?)`
		args = append(args, f.Cluster)
	}
	if f.Origin != "" {
		q += ` AND origin = ?`
		args = append(args, f.Origin)
	}
	if f.OnlySeen {
		q += ` AND last_seen_at IS NOT NULL`
	}
	if f.ObservedFor > 0 {
		q += ` AND num IN (SELECT signal_num FROM latest_state WHERE vehicle_num = ?)`
		args = append(args, f.ObservedFor)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += ` ORDER BY key LIMIT ?`
	args = append(args, f.Limit)
	rows, err := r.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signal{}
	for rows.Next() {
		s, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Clusters lists the distinct data clusters with signal counts.
func (r *Reader) Clusters(ctx context.Context) (map[string]int, error) {
	rows, err := r.s.ReadDB().QueryContext(ctx, `SELECT j.value, count(*) FROM signals, json_each(signals.clusters_json) j GROUP BY j.value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// StateValue is a latest-known value with its signal.
type StateValue struct {
	Signal     Signal    `json:"signal"`
	ObservedAt time.Time `json:"observed_at"`
	ReceivedAt time.Time `json:"received_at"`
	TimeBasis  string    `json:"time_basis" enum:"observed,delivery"`
	Num        *float64  `json:"num,omitempty"`
	Text       *string   `json:"text,omitempty"`
	Quality    string    `json:"quality" enum:"ok,null,empty,unset,invalid"`
	Unit       string    `json:"unit,omitempty"`
	Stale      bool      `json:"stale"`
	AgeSeconds int64     `json:"age_seconds"`
}

// LatestState returns the latest values of a vehicle; staleAfter marks values older than it as stale.
func (r *Reader) LatestState(ctx context.Context, vehicleNum int64, now time.Time, staleAfter time.Duration, cluster string) ([]StateValue, error) {
	q := `SELECT ` + prefixed("s.", signalCols) + `, l.observed_at, l.received_at, l.time_basis, l.num, l.text, l.quality, l.unit
		FROM latest_state l JOIN signals s ON s.num = l.signal_num WHERE l.vehicle_num = ?`
	args := []any{vehicleNum}
	if cluster != "" {
		q += ` AND EXISTS (SELECT 1 FROM json_each(s.clusters_json) WHERE value = ?)`
		args = append(args, cluster)
	}
	q += ` ORDER BY s.name, s.key`
	rows, err := r.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateValue{}
	for rows.Next() {
		var v StateValue
		var cl string
		var first, last sql.NullInt64
		var at, recv int64
		var num sql.NullFloat64
		var text sql.NullString
		if err := rows.Scan(&v.Signal.Num, &v.Signal.Key, &v.Signal.Name, &v.Signal.Description, &v.Signal.Unit, &v.Signal.UnitRaw, &v.Signal.DataType,
			&v.Signal.ValueKind, &cl, &v.Signal.Origin, &v.Signal.ObservedName, &first, &last, &at, &recv, &v.TimeBasis, &num, &text, &v.Quality, &v.Unit); err != nil {
			return nil, err
		}
		v.Signal.Clusters = []string{}
		_ = json.Unmarshal([]byte(cl), &v.Signal.Clusters)
		v.ObservedAt, v.ReceivedAt = time.UnixMicro(at).UTC(), time.UnixMicro(recv).UTC()
		if num.Valid {
			v.Num = &num.Float64
		}
		if text.Valid {
			v.Text = &text.String
		}
		v.AgeSeconds = int64(now.Sub(v.ObservedAt).Seconds())
		v.Stale = staleAfter > 0 && now.Sub(v.ObservedAt) > staleAfter
		out = append(out, v)
	}
	return out, rows.Err()
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// Freshness summarizes a vehicle's data recency.
type Freshness struct {
	Signals        int        `json:"signals"`
	StaleSignals   int        `json:"stale_signals"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	LastReceivedAt *time.Time `json:"last_received_at,omitempty"`
}

// VehicleFreshness returns per-vehicle freshness for many vehicles at once.
func (r *Reader) VehicleFreshness(ctx context.Context, now time.Time, staleAfter func(vehicleNum int64) time.Duration) (map[int64]Freshness, error) {
	rows, err := r.s.ReadDB().QueryContext(ctx, `SELECT vehicle_num, observed_at, received_at FROM latest_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Freshness{}
	for rows.Next() {
		var v, at, recv int64
		if err := rows.Scan(&v, &at, &recv); err != nil {
			return nil, err
		}
		f := out[v]
		f.Signals++
		t := time.UnixMicro(at).UTC()
		rt := time.UnixMicro(recv).UTC()
		if now.Sub(t) > staleAfter(v) {
			f.StaleSignals++
		}
		if f.LastObservedAt == nil || t.After(*f.LastObservedAt) {
			f.LastObservedAt = &t
		}
		if f.LastReceivedAt == nil || rt.After(*f.LastReceivedAt) {
			f.LastReceivedAt = &rt
		}
		out[v] = f
	}
	return out, rows.Err()
}

// Point is one series point. Numeric aggregates are set for bucketed series; gaps are buckets with Count 0 and
// nil values (never interpolated).
type Point struct {
	T       time.Time `json:"t"`
	Value   *float64  `json:"value"`
	Min     *float64  `json:"min,omitempty"`
	Max     *float64  `json:"max,omitempty"`
	Count   int64     `json:"count"`
	Text    *string   `json:"text,omitempty"`
	Quality string    `json:"quality,omitempty"`
}

// Series is a time series response.
type Series struct {
	SignalKey   string    `json:"signal_key"`
	Unit        string    `json:"unit,omitempty"`
	ValueKind   string    `json:"value_kind"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Aggregation string    `json:"aggregation" enum:"raw,bucket,changes"`
	BucketSecs  int64     `json:"bucket_seconds,omitempty"`
	Points      []Point   `json:"points"`
	Truncated   bool      `json:"truncated"`
	Source      string    `json:"source" enum:"observations,rollups"`
	Gaps        int       `json:"gaps"`
}

// SeriesQuery asks for a series.
type SeriesQuery struct {
	VehicleNum int64
	Signal     Signal
	From, To   time.Time
	MaxPoints  int
	Bucket     time.Duration // 0 = automatic
}

// Validate checks the time range and limits.
func (q *SeriesQuery) Validate() error {
	if !q.To.After(q.From) {
		return apperr.New(apperr.CodeInvalid, "to must be after from")
	}
	if q.To.Sub(q.From) > MaxRange {
		return apperr.New(apperr.CodeInvalid, "the time range must be at most %d days", int(MaxRange.Hours()/24))
	}
	if q.MaxPoints <= 0 {
		q.MaxPoints = 1000
	}
	if q.MaxPoints > MaxPoints {
		return apperr.New(apperr.CodeInvalid, "max_points must be at most %d", MaxPoints)
	}
	if q.Bucket != 0 && q.Bucket < time.Minute {
		return apperr.New(apperr.CodeInvalid, "bucket must be at least 60 seconds")
	}
	return nil
}

// Series returns a vehicle signal's history. Numeric signals are returned raw when they fit in max_points,
// otherwise bucketed (from hourly rollups when buckets are ≥ 1 h); empty buckets are explicit gaps. Non-numeric
// signals return change points only.
func (r *Reader) Series(ctx context.Context, q SeriesQuery) (Series, error) {
	if err := q.Validate(); err != nil {
		return Series{}, err
	}
	out := Series{SignalKey: q.Signal.Key, Unit: q.Signal.Unit, ValueKind: q.Signal.ValueKind, From: q.From.UTC(), To: q.To.UTC(), Points: []Point{},
		Source: "observations"}
	from, to := q.From.UnixMicro(), q.To.UnixMicro()
	numeric := q.Signal.ValueKind == "number" || q.Signal.ValueKind == "boolean" || q.Signal.ValueKind == "unknown"
	var count int
	if err := r.s.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM observations WHERE vehicle_num = ? AND signal_num = ? AND observed_at >= ? AND observed_at < ?`,
		q.VehicleNum, q.Signal.Num, from, to).Scan(&count); err != nil {
		return out, err
	}
	if !numeric {
		out.Aggregation = "changes"
		return out, r.changes(ctx, q, &out)
	}
	if q.Bucket == 0 && count <= q.MaxPoints {
		out.Aggregation = "raw"
		return out, r.raw(ctx, q, &out)
	}
	bucket := q.Bucket
	if bucket == 0 {
		bucket = q.To.Sub(q.From) / time.Duration(q.MaxPoints)
		bucket = roundBucket(bucket)
	}
	if int64(q.To.Sub(q.From)/bucket) > int64(q.MaxPoints) {
		return out, apperr.New(apperr.CodeInvalid, "bucket too small for the range (more than %d points)", q.MaxPoints)
	}
	out.Aggregation, out.BucketSecs = "bucket", int64(bucket.Seconds())
	return out, r.buckets(ctx, q, bucket, &out)
}

func roundBucket(d time.Duration) time.Duration {
	for _, s := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
		12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		if d <= s {
			return s
		}
	}
	return 7 * 24 * time.Hour
}

func (r *Reader) raw(ctx context.Context, q SeriesQuery, out *Series) error {
	rows, err := r.s.ReadDB().QueryContext(ctx, `SELECT observed_at, num, text, quality FROM observations WHERE vehicle_num = ? AND signal_num = ?
		AND observed_at >= ? AND observed_at < ? ORDER BY observed_at, received_at LIMIT ?`, q.VehicleNum, q.Signal.Num, q.From.UnixMicro(),
		q.To.UnixMicro(), q.MaxPoints+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at int64
		var num sql.NullFloat64
		var text sql.NullString
		var p Point
		if err := rows.Scan(&at, &num, &text, &p.Quality); err != nil {
			return err
		}
		if len(out.Points) == q.MaxPoints {
			out.Truncated = true
			break
		}
		p.T, p.Count = time.UnixMicro(at).UTC(), 1
		if num.Valid && p.Quality == QualityOK {
			v := num.Float64
			p.Value = &v
		}
		if text.Valid {
			s := text.String
			p.Text = &s
		}
		// Corrections at the same timestamp: the later receipt replaces the earlier point.
		if n := len(out.Points); n > 0 && out.Points[n-1].T.Equal(p.T) {
			out.Points[n-1] = p
			continue
		}
		out.Points = append(out.Points, p)
	}
	return rows.Err()
}

func (r *Reader) changes(ctx context.Context, q SeriesQuery, out *Series) error {
	rows, err := r.s.ReadDB().QueryContext(ctx, `SELECT observed_at, num, text, quality FROM observations WHERE vehicle_num = ? AND signal_num = ?
		AND observed_at >= ? AND observed_at < ? ORDER BY observed_at, received_at`, q.VehicleNum, q.Signal.Num, q.From.UnixMicro(), q.To.UnixMicro())
	if err != nil {
		return err
	}
	defer rows.Close()
	var last string
	for rows.Next() {
		var at int64
		var num sql.NullFloat64
		var text sql.NullString
		var quality string
		if err := rows.Scan(&at, &num, &text, &quality); err != nil {
			return err
		}
		key := quality + "|" + text.String
		if num.Valid {
			key += fmt.Sprintf("|%g", num.Float64)
		}
		if len(out.Points) > 0 && key == last {
			continue
		}
		last = key
		if len(out.Points) == q.MaxPoints {
			out.Truncated = true
			break
		}
		p := Point{T: time.UnixMicro(at).UTC(), Count: 1, Quality: quality}
		if num.Valid {
			v := num.Float64
			p.Value = &v
		}
		if text.Valid {
			s := text.String
			p.Text = &s
		}
		out.Points = append(out.Points, p)
	}
	return rows.Err()
}

func (r *Reader) buckets(ctx context.Context, q SeriesQuery, bucket time.Duration, out *Series) error {
	bw := bucket.Microseconds()
	from := q.From.UnixMicro() - ((q.From.UnixMicro()%bw)+bw)%bw
	to := q.To.UnixMicro()
	var rows *sql.Rows
	var err error
	if bucket >= time.Hour {
		out.Source = "rollups"
		rows, err = r.s.ReadDB().QueryContext(ctx, `SELECT (bucket_start - ?) / ? AS b, sum(n), min(min), max(max), sum(sum),
			(SELECT last_num FROM observation_rollups r2 WHERE r2.vehicle_num = r.vehicle_num AND r2.signal_num = r.signal_num
			   AND (r2.bucket_start - ?) / ? = (r.bucket_start - ?) / ? ORDER BY r2.last_at DESC LIMIT 1)
			FROM observation_rollups r WHERE vehicle_num = ? AND signal_num = ? AND bucket_start >= ? AND bucket_start < ?
			GROUP BY b ORDER BY b`, from, bw, from, bw, from, bw, q.VehicleNum, q.Signal.Num, from, to)
	} else {
		rows, err = r.s.ReadDB().QueryContext(ctx, `SELECT (observed_at - ?) / ? AS b, count(*), min(num), max(num), sum(num), NULL
			FROM observations WHERE vehicle_num = ? AND signal_num = ? AND observed_at >= ? AND observed_at < ? AND quality = 'ok'
			  AND num IS NOT NULL GROUP BY b ORDER BY b`, from, bw, q.VehicleNum, q.Signal.Num, from, to)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	got := map[int64]Point{}
	for rows.Next() {
		var b, n int64
		var mn, mx, sum, last sql.NullFloat64
		if err := rows.Scan(&b, &n, &mn, &mx, &sum, &last); err != nil {
			return err
		}
		p := Point{Count: n}
		if n > 0 && sum.Valid {
			avg := sum.Float64 / float64(n)
			p.Value = &avg
			lo, hi := mn.Float64, mx.Float64
			p.Min, p.Max = &lo, &hi
		}
		got[b] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, t := int64(0), from; t < to; i, t = i+1, t+bw {
		p, ok := got[i]
		p.T = time.UnixMicro(t).UTC()
		if !ok {
			out.Gaps++
		}
		out.Points = append(out.Points, p)
	}
	return nil
}

// ExportRow is one exported observation.
type ExportRow struct {
	VehicleID  string
	SignalKey  string
	SignalName string
	ObservedAt time.Time
	ReceivedAt time.Time
	Num        *float64
	Text       *string
	Quality    string
	Unit       string
	TimeBasis  string
}

// Export streams observations for vehicles/signals in a range (bounded by MaxExportRow).
func (r *Reader) Export(ctx context.Context, vehicleNums []int64, signalNums []int64, from, to time.Time, fn func(ExportRow) error) (int, bool, error) {
	if !to.After(from) || to.Sub(from) > MaxRange {
		return 0, false, apperr.New(apperr.CodeInvalid, "the time range must be positive and at most 366 days")
	}
	if len(vehicleNums) == 0 || len(signalNums) == 0 {
		return 0, false, apperr.New(apperr.CodeInvalid, "select at least one vehicle and one signal")
	}
	q := `SELECT v.id, s.key, s.name, o.observed_at, o.received_at, o.num, o.text, o.quality, o.unit, o.time_basis
		FROM observations o JOIN vehicles v ON v.num = o.vehicle_num JOIN signals s ON s.num = o.signal_num
		WHERE o.vehicle_num IN (` + placeholders(len(vehicleNums)) + `) AND o.signal_num IN (` + placeholders(len(signalNums)) + `)
		AND o.observed_at >= ? AND o.observed_at < ? ORDER BY o.observed_at, v.id, s.key LIMIT ?`
	args := make([]any, 0, len(vehicleNums)+len(signalNums)+3)
	for _, n := range vehicleNums {
		args = append(args, n)
	}
	for _, n := range signalNums {
		args = append(args, n)
	}
	args = append(args, from.UnixMicro(), to.UnixMicro(), MaxExportRow+1)
	rows, err := r.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		if n == MaxExportRow {
			return n, true, nil
		}
		var e ExportRow
		var at, recv int64
		var num sql.NullFloat64
		var text sql.NullString
		if err := rows.Scan(&e.VehicleID, &e.SignalKey, &e.SignalName, &at, &recv, &num, &text, &e.Quality, &e.Unit, &e.TimeBasis); err != nil {
			return n, false, err
		}
		e.ObservedAt, e.ReceivedAt = time.UnixMicro(at).UTC(), time.UnixMicro(recv).UTC()
		if num.Valid {
			e.Num = &num.Float64
		}
		if text.Valid {
			e.Text = &text.String
		}
		if err := fn(e); err != nil {
			return n, false, err
		}
		n++
	}
	return n, false, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
