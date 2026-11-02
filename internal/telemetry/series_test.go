package telemetry

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Rollup-backed buckets must equal aggregates computed from raw observations, and empty buckets must be gaps.
func TestRollupBucketsMatchRawAggregates(t *testing.T) {
	ctx := context.Background()
	s, err := openBench(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, sn, err := seed(ctx, s, 1)
	if err != nil {
		t.Fatal(err)
	}
	end := benchEnd
	start := end.Add(-48 * time.Hour)
	for at := start; at.Before(end); at = at.Add(benchStep) {
		if at.After(start.Add(20*time.Hour)) && at.Before(start.Add(26*time.Hour)) {
			continue // a six-hour hole: must come back as gaps, never interpolated
		}
		if _, err := writeDelivery(ctx, s, v[0], delivery(sn, at, 0)); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(s)
	sig, err := r.SignalByKey(ctx, "00000000-0000-3000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []time.Duration{time.Hour, 3 * time.Hour} {
		out, err := r.Series(ctx, SeriesQuery{VehicleNum: v[0], Signal: sig, From: start, To: end, Bucket: bucket, MaxPoints: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if out.Source != "rollups" || out.Aggregation != "bucket" || len(out.Points) != int(48*time.Hour/bucket) {
			t.Fatalf("bucket %s: source=%s agg=%s points=%d", bucket, out.Source, out.Aggregation, len(out.Points))
		}
		if out.Gaps == 0 {
			t.Fatalf("bucket %s: expected gaps for the missing hours", bucket)
		}
		for _, p := range out.Points {
			var n int
			var mn, mx, sum float64
			err := s.ReadDB().QueryRowContext(ctx, `SELECT count(*), coalesce(min(num),0), coalesce(max(num),0), coalesce(sum(num),0)
				FROM observations WHERE vehicle_num = ? AND signal_num = ? AND observed_at >= ? AND observed_at < ?`,
				v[0], sig.Num, p.T.UnixMicro(), p.T.Add(bucket).UnixMicro()).Scan(&n, &mn, &mx, &sum)
			if err != nil {
				t.Fatal(err)
			}
			if int64(n) != p.Count {
				t.Fatalf("bucket %s at %s: count %d, raw %d", bucket, p.T, p.Count, n)
			}
			if n == 0 {
				if p.Value != nil {
					t.Fatalf("empty bucket at %s has a value (interpolated?)", p.T)
				}
				continue
			}
			if math.Abs(*p.Value-sum/float64(n)) > 1e-9 || *p.Min != mn || *p.Max != mx {
				t.Fatalf("bucket %s at %s: got avg=%v min=%v max=%v, raw avg=%v min=%v max=%v", bucket, p.T, *p.Value, *p.Min, *p.Max, sum/float64(n), mn, mx)
			}
		}
	}
}

// Every API path that returns signal metadata carries the presentation fields (label, enum).
func TestLatestStateCarriesPresentation(t *testing.T) {
	ctx := context.Background()
	s, err := openBench(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, _, err := seed(ctx, s, 1)
	if err != nil {
		t.Fatal(err)
	}
	var lockNum int64
	if err := s.W().Tx(ctx, func(tx *store.Tx) error {
		res, err := tx.SQL.ExecContext(ctx, `INSERT INTO signals (key, name, description, value_kind, origin, updated_at)
			VALUES ('00000000-0000-3000-8000-0000000000aa', 'locked_state_tailgate', 'Indicates if the tailgate is locked (2) or unlocked (3)', 'number', 'continuous', 1)`)
		if err != nil {
			return err
		}
		lockNum, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	two := 2.0
	if _, err := writeDelivery(ctx, s, v[0], []Observation{{SignalNum: lockNum, ObservedAt: benchEnd, TimeBasis: TimeObserved, Value: Value{Num: &two, Quality: QualityOK}}}); err != nil {
		t.Fatal(err)
	}
	st, err := NewReader(s).LatestState(ctx, v[0], benchEnd, time.Hour, "")
	if err != nil || len(st) != 1 {
		t.Fatalf("state %v %v", st, err)
	}
	if st[0].Signal.Label != "Tailgate lock" || st[0].Signal.Enum["2"] != "locked" {
		t.Fatalf("presentation missing: label=%q enum=%v", st[0].Signal.Label, st[0].Signal.Enum)
	}
}
