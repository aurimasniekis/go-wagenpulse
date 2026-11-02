package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Benchmarks for docs/STORAGE.md ("Performance validation"). Data is synthetic: one vehicle-year at the portal's
// 15-minute cadence for benchSignals numeric signals, written through Writer so rollups are maintained exactly as in
// production. `make bench` runs these.

const (
	benchSignals = 16
	benchStep    = 15 * time.Minute
)

var (
	benchOnce  sync.Once
	benchStore *store.Store
	benchErr   error
	benchEnd   = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	benchDir   string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if benchStore != nil {
		_ = benchStore.Close()
	}
	if benchDir != "" {
		_ = os.RemoveAll(benchDir)
	}
	os.Exit(code)
}

func openBench(dir string) (*store.Store, error) {
	return store.Open(context.Background(), store.Options{DataDir: filepath.Join(dir, "data")})
}

// seed creates vehicles and numeric signals and returns their numbers.
func seed(ctx context.Context, s *store.Store, vehicles int) ([]int64, []int64, error) {
	var vnums, snums []int64
	err := s.W().Tx(ctx, func(tx *store.Tx) error {
		for i := range vehicles {
			res, err := tx.SQL.ExecContext(ctx, `INSERT INTO vehicles (id, slug, display_name, vin, brand, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'volkswagen', 1, 1)`, fmt.Sprintf("veh_bench%02d", i), fmt.Sprintf("bench-%d", i),
				fmt.Sprintf("Bench %d", i), fmt.Sprintf("XXXZZZ00ZXX%06d", i))
			if err != nil {
				return err
			}
			n, _ := res.LastInsertId()
			vnums = append(vnums, n)
		}
		for i := range benchSignals {
			res, err := tx.SQL.ExecContext(ctx, `INSERT INTO signals (key, name, unit, value_kind, origin, updated_at)
				VALUES (?, ?, 'km', 'number', 'observed', 1)`, fmt.Sprintf("00000000-0000-3000-8000-%012d", i), fmt.Sprintf("bench_%d", i))
			if err != nil {
				return err
			}
			n, _ := res.LastInsertId()
			snums = append(snums, n)
		}
		return nil
	})
	return vnums, snums, err
}

// delivery builds one portal delivery: every signal sampled at three capture times (as in the real samples).
func delivery(snums []int64, at time.Time, salt int) []Observation {
	obs := make([]Observation, 0, len(snums)*3)
	for k := range 3 {
		t := at.Add(time.Duration(k) * 5 * time.Minute)
		for i, sn := range snums {
			v := float64((int(t.Unix()/60)+i*7+salt)%500) / 4
			obs = append(obs, Observation{SignalNum: sn, ObservedAt: t, TimeBasis: TimeObserved, Value: Value{Num: &v, Quality: QualityOK, Unit: "km"}})
		}
	}
	return obs
}

func writeDelivery(ctx context.Context, s *store.Store, vehicle int64, obs []Observation) (WriteResult, error) {
	var res WriteResult
	err := s.W().Tx(ctx, func(tx *store.Tx) error {
		var err error
		res, err = Writer{}.Write(ctx, tx, vehicle, 0, time.Now(), obs)
		return err
	})
	return res, err
}

// dataset returns a store holding one year of 15-minute deliveries for one vehicle (built once per run).
func dataset(b *testing.B) (*store.Store, int64, []int64) {
	b.Helper()
	ctx := context.Background()
	var vnum int64
	var snums []int64
	benchOnce.Do(func() {
		benchDir, benchErr = os.MkdirTemp("", "wagenpulse-bench-*")
		if benchErr != nil {
			return
		}
		if benchStore, benchErr = openBench(benchDir); benchErr != nil {
			return
		}
		v, sn, err := seed(ctx, benchStore, 1)
		if err != nil {
			benchErr = err
			return
		}
		start := time.Now()
		rows := 0
		for t := benchEnd.Add(-365 * 24 * time.Hour); t.Before(benchEnd); t = t.Add(benchStep) {
			res, err := writeDelivery(ctx, benchStore, v[0], delivery(sn, t, 0))
			if err != nil {
				benchErr = err
				return
			}
			rows += len(res.Accepted)
		}
		b.Logf("dataset: %d observations (1 vehicle × %d signals × 1 year @15 min, 3 samples per delivery) written in %s",
			rows, benchSignals, time.Since(start).Round(time.Millisecond))
	})
	if benchErr != nil {
		b.Fatal(benchErr)
	}
	_ = benchStore.ReadDB().QueryRowContext(ctx, `SELECT num FROM vehicles WHERE id = 'veh_bench00'`).Scan(&vnum)
	rows, err := benchStore.ReadDB().QueryContext(ctx, `SELECT num FROM signals WHERE name LIKE 'bench_%' ORDER BY num`)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		snums = append(snums, n)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	return benchStore, vnum, snums
}

// BenchmarkIngestDelivery: one 48-value delivery per op into a fresh store (new observations, latest state, rollups).
func BenchmarkIngestDelivery(b *testing.B) {
	ctx := context.Background()
	s, err := openBench(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, sn, err := seed(ctx, s, 1)
	if err != nil {
		b.Fatal(err)
	}
	t := benchEnd
	b.ResetTimer()
	for b.Loop() {
		t = t.Add(benchStep)
		if _, err := writeDelivery(ctx, s, v[0], delivery(sn, t, 0)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*benchSignals*3)/b.Elapsed().Seconds(), "obs/s")
}

// BenchmarkIngestDuplicateDelivery: re-importing an identical delivery (the dedupe path).
func BenchmarkIngestDuplicateDelivery(b *testing.B) {
	ctx := context.Background()
	s, err := openBench(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, sn, err := seed(ctx, s, 1)
	if err != nil {
		b.Fatal(err)
	}
	obs := delivery(sn, benchEnd, 0)
	if _, err := writeDelivery(ctx, s, v[0], obs); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		res, err := writeDelivery(ctx, s, v[0], delivery(sn, benchEnd, 0))
		if err != nil || len(res.Accepted) != 0 {
			b.Fatalf("accepted=%d err=%v", len(res.Accepted), err)
		}
	}
	b.ReportMetric(float64(b.N*benchSignals*3)/b.Elapsed().Seconds(), "obs/s")
}

func benchSeries(b *testing.B, span time.Duration, maxPoints int) {
	s, v, sn := dataset(b)
	r := NewReader(s)
	ctx := context.Background()
	sig, err := r.SignalByKey(ctx, "00000000-0000-3000-8000-000000000003")
	if err != nil || sig.Num != sn[3] {
		b.Fatalf("signal: %v", err)
	}
	q := SeriesQuery{VehicleNum: v, Signal: sig, From: benchEnd.Add(-span), To: benchEnd, MaxPoints: maxPoints}
	var points int
	var source string
	b.ResetTimer()
	for b.Loop() {
		out, err := r.Series(ctx, q)
		if err != nil {
			b.Fatal(err)
		}
		points, source = len(out.Points), out.Source
	}
	b.ReportMetric(float64(points), "points")
	b.Logf("%s: %d points from %s (%s)", span, points, source, b.Name())
}

func BenchmarkSeries24h(b *testing.B)  { benchSeries(b, 24*time.Hour, 1000) }
func BenchmarkSeries7d(b *testing.B)   { benchSeries(b, 7*24*time.Hour, 1000) }
func BenchmarkSeries30d(b *testing.B)  { benchSeries(b, 30*24*time.Hour, 1000) }
func BenchmarkSeries365d(b *testing.B) { benchSeries(b, 365*24*time.Hour, 1000) }

func BenchmarkLatestState(b *testing.B) {
	s, v, _ := dataset(b)
	r := NewReader(s)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		st, err := r.LatestState(ctx, v, benchEnd, 2*time.Hour, "")
		if err != nil || len(st) != benchSignals {
			b.Fatalf("state=%d err=%v", len(st), err)
		}
	}
}

// BenchmarkExport30d streams a 30-day CSV-equivalent export of four signals (~34k rows).
func BenchmarkExport30d(b *testing.B) {
	s, v, sn := dataset(b)
	r := NewReader(s)
	ctx := context.Background()
	var n int
	b.ResetTimer()
	for b.Loop() {
		var err error
		n, _, err = r.Export(ctx, []int64{v}, sn[:4], benchEnd.Add(-30*24*time.Hour), benchEnd, func(ExportRow) error { return nil })
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(n), "rows")
	b.ReportMetric(float64(n*b.N)/b.Elapsed().Seconds(), "rows/s")
}
