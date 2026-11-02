// Package telemetry stores and queries typed observations (ADR-0008), maintains latest-known state with late-arrival
// protection (ADR-0009) and hourly rollups for long-range charts (docs/STORAGE.md).
package telemetry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"hash/fnv"
	"math"
	"slices"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Qualities of an observation value.
const (
	QualityOK      = "ok"
	QualityNull    = "null"    // the source sent JSON null
	QualityEmpty   = "empty"   // the source sent an empty string
	QualityUnset   = "unset"   // the source sent a documented "not available" sentinel (2^32-1, 2^31-1, -2^31)
	QualityInvalid = "invalid" // the value does not match the signal's type; raw kept
)

// Time bases of an observation's timestamp.
const (
	TimeObserved = "observed" // the source's per-value capture time
	TimeDelivery = "delivery" // no per-value time; the delivery's capture/publication time was used
)

// Value is a typed value.
type Value struct {
	Num     *float64
	Text    *string
	Quality string
	Unit    string
	Raw     *string // kept only when normalization changed or rejected the source text
}

// Observation is one normalized value of a signal at a time.
type Observation struct {
	SignalNum  int64
	ObservedAt time.Time
	TimeBasis  string
	Value
}

// Hash identifies a value for deduplication: same (vehicle, signal, time, hash) means the same observation.
func (v Value) Hash() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(v.Quality))
	h.Write([]byte{0})
	if v.Num != nil {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(*v.Num))
		h.Write([]byte{'n'})
		h.Write(b[:])
	}
	if v.Text != nil {
		h.Write([]byte{'t'})
		_, _ = h.Write([]byte(*v.Text))
	}
	h.Write([]byte{0})
	_, _ = h.Write([]byte(v.Unit))
	return int64(h.Sum64()) //nolint:gosec // bit pattern reinterpretation
}

// State is a latest-known value.
type State struct {
	ObservedAt time.Time
	ReceivedAt time.Time
	Value
}

// Accepted is a newly stored observation, with the latest state before it was applied.
type Accepted struct {
	Observation
	Previous   *State // latest state before this observation (nil when none)
	BecameLast bool   // whether it became the latest state
	Correction bool   // another value existed at the same observed time
}

// WriteResult summarizes a batch.
type WriteResult struct {
	Accepted    []Accepted
	Duplicates  int
	Corrections int
	From, To    time.Time
}

// Writer writes observation batches inside a caller's transaction.
type Writer struct{}

// Write stores a batch for one vehicle and delivery inside tx: inserts observations (ignoring exact duplicates),
// updates latest state only for newer observations (or same-time corrections received later), maintains signal
// first/last seen and recomputes touched hourly rollup buckets. Observations are processed in observed-time order.
func (Writer) Write(ctx context.Context, tx *store.Tx, vehicleNum, deliveryNum int64, receivedAt time.Time, obs []Observation) (WriteResult, error) {
	var res WriteResult
	if len(obs) == 0 {
		return res, nil
	}
	ins, err := tx.SQL.PrepareContext(ctx, `INSERT INTO observations (vehicle_num, signal_num, observed_at, value_hash, received_at, num, text,
		quality, unit, raw, delivery_num, time_basis) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return res, err
	}
	defer ins.Close()
	exists, err := tx.SQL.PrepareContext(ctx, `SELECT 1 FROM observations WHERE vehicle_num = ? AND signal_num = ? AND observed_at = ? LIMIT 1`)
	if err != nil {
		return res, err
	}
	defer exists.Close()
	getState, err := tx.SQL.PrepareContext(ctx, `SELECT observed_at, received_at, num, text, quality, unit FROM latest_state
		WHERE vehicle_num = ? AND signal_num = ?`)
	if err != nil {
		return res, err
	}
	defer getState.Close()
	upState, err := tx.SQL.PrepareContext(ctx, `INSERT INTO latest_state (vehicle_num, signal_num, observed_at, received_at, num, text, quality,
		unit, delivery_num, time_basis) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vehicle_num, signal_num) DO UPDATE SET observed_at = excluded.observed_at, received_at = excluded.received_at,
		  num = excluded.num, text = excluded.text, quality = excluded.quality, unit = excluded.unit, delivery_num = excluded.delivery_num,
		  time_basis = excluded.time_basis
		WHERE excluded.observed_at > latest_state.observed_at
		   OR (excluded.observed_at = latest_state.observed_at AND excluded.received_at >= latest_state.received_at)`)
	if err != nil {
		return res, err
	}
	defer upState.Close()
	sortByTime(obs)
	type bucketKey struct{ signal, start int64 }
	buckets := map[bucketKey]struct{}{}
	seen := map[int64][2]int64{}
	recv := receivedAt.UnixMicro()
	for _, o := range obs {
		at := o.ObservedAt.UnixMicro()
		hash := o.Hash()
		var one int
		correction := false
		switch err := exists.QueryRowContext(ctx, vehicleNum, o.SignalNum, at).Scan(&one); {
		case err == nil:
			correction = true
		case err != sql.ErrNoRows:
			return res, err
		}
		r, err := ins.ExecContext(ctx, vehicleNum, o.SignalNum, at, hash, recv, o.Num, o.Text, o.Quality, o.Unit, o.Raw, deliveryNum, o.TimeBasis)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			res.Duplicates++
			continue
		}
		var prev *State
		var pAt, pRecv int64
		var pNum sql.NullFloat64
		var pText sql.NullString
		var pQual, pUnit string
		switch err := getState.QueryRowContext(ctx, vehicleNum, o.SignalNum).Scan(&pAt, &pRecv, &pNum, &pText, &pQual, &pUnit); {
		case err == nil:
			prev = &State{ObservedAt: time.UnixMicro(pAt).UTC(), ReceivedAt: time.UnixMicro(pRecv).UTC(), Value: Value{Quality: pQual, Unit: pUnit}}
			if pNum.Valid {
				prev.Num = &pNum.Float64
			}
			if pText.Valid {
				prev.Text = &pText.String
			}
		case err != sql.ErrNoRows:
			return res, err
		}
		u, err := upState.ExecContext(ctx, vehicleNum, o.SignalNum, at, recv, o.Num, o.Text, o.Quality, o.Unit, deliveryNum, o.TimeBasis)
		if err != nil {
			return res, err
		}
		became, _ := u.RowsAffected()
		if correction {
			res.Corrections++
		}
		res.Accepted = append(res.Accepted, Accepted{Observation: o, Previous: prev, BecameLast: became == 1, Correction: correction})
		if res.From.IsZero() || o.ObservedAt.Before(res.From) {
			res.From = o.ObservedAt
		}
		if o.ObservedAt.After(res.To) {
			res.To = o.ObservedAt
		}
		s := seen[o.SignalNum]
		if s[0] == 0 || at < s[0] {
			s[0] = at
		}
		if at > s[1] {
			s[1] = at
		}
		seen[o.SignalNum] = s
		if o.Num != nil && o.Quality == QualityOK {
			buckets[bucketKey{o.SignalNum, BucketStart(at)}] = struct{}{}
		}
	}
	for sig, span := range seen {
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE signals SET first_seen_at = min(coalesce(first_seen_at, ?), ?),
			last_seen_at = max(coalesce(last_seen_at, ?), ?) WHERE num = ?`, span[0], span[0], span[1], span[1], sig); err != nil {
			return res, err
		}
	}
	for b := range buckets {
		if err := recomputeBucket(ctx, tx, vehicleNum, b.signal, b.start); err != nil {
			return res, err
		}
	}
	return res, nil
}

// BucketMicros is the rollup bucket width.
const BucketMicros = int64(time.Hour / time.Microsecond)

// BucketStart returns the hourly bucket containing t (Unix µs).
func BucketStart(t int64) int64 { return t - ((t%BucketMicros)+BucketMicros)%BucketMicros }

// recomputeBucket rebuilds one hourly rollup from observations; for corrected timestamps only the latest receipt
// counts.
func recomputeBucket(ctx context.Context, tx *store.Tx, vehicleNum, signalNum, start int64) error {
	_, err := tx.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO observation_rollups (vehicle_num, signal_num, bucket_start, n, min, max, sum,
		last_num, last_at)
		SELECT ?, ?, ?, count(*), min(num), max(num), sum(num),
		  (SELECT num FROM observations l WHERE l.vehicle_num = ? AND l.signal_num = ? AND l.observed_at >= ? AND l.observed_at < ?
		     AND l.quality = 'ok' AND l.num IS NOT NULL ORDER BY l.observed_at DESC, l.received_at DESC LIMIT 1),
		  max(observed_at)
		FROM observations o
		WHERE o.vehicle_num = ? AND o.signal_num = ? AND o.observed_at >= ? AND o.observed_at < ? AND o.quality = 'ok' AND o.num IS NOT NULL
		  AND o.received_at = (SELECT max(o2.received_at) FROM observations o2 WHERE o2.vehicle_num = o.vehicle_num
		    AND o2.signal_num = o.signal_num AND o2.observed_at = o.observed_at)
		HAVING count(*) > 0`,
		vehicleNum, signalNum, start, vehicleNum, signalNum, start, start+BucketMicros, vehicleNum, signalNum, start, start+BucketMicros)
	return err
}

func sortByTime(obs []Observation) {
	// Stable: insertion order within equal times is preserved so deliveries replay deterministically.
	slices.SortStableFunc(obs, func(a, b Observation) int { return a.ObservedAt.Compare(b.ObservedAt) })
}
