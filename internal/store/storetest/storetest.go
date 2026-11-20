// Adapted from HallPass internal/store/storetest/storetest.go (Apache-2.0).

// Package storetest opens throwaway stores for tests and enforces the short-transaction rule (ADR-0002).
package storetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// MaxTxDuration is the longest a write transaction may take in tests (ADR-0002).
// WAGENPULSE_TEST_TX_MAX overrides it for slow CI runners; the default matches the requirement.
var MaxTxDuration = func() time.Duration {
	if v, err := time.ParseDuration(os.Getenv("WAGENPULSE_TEST_TX_MAX")); err == nil && v > 0 {
		return v
	}
	return 100 * time.Millisecond
}()

// MaxCommitDuration is the allowance for COMMIT (fsync under synchronous=FULL) of one write transaction in tests.
// ADR-0002's budget bounds the work done while holding the write lock, which the code controls; fsync latency on
// a shared CI runner is environmental (a 705ms commit of a five-row transaction was seen with the body at ~15ms),
// so it gets its own, much larger ceiling that still catches a transaction that writes pathologically much.
const MaxCommitDuration = 3 * time.Second

// Option customizes Open.
type Option func(*store.Options)

// WithClock sets the store clock.
func WithClock(c clock.Clock) Option { return func(o *store.Options) { o.Clock = c } }

// WithDir uses dir as the data directory instead of a fresh temp dir.
func WithDir(dir string) Option { return func(o *store.Options) { o.DataDir = dir } }

// WithBusyTimeout sets SQLite's busy_timeout, for tests where several stores contend for the write lock under -race.
func WithBusyTimeout(d time.Duration) Option { return func(o *store.Options) { o.BusyTimeout = d } }

// Open opens a store in a new temporary data directory and closes it at test cleanup. For every successful write
// transaction the callback body (the work done while holding the write lock) is checked against MaxTxDuration and
// COMMIT against MaxCommitDuration; exceeding either fails the test.
func Open(t testing.TB, opts ...Option) *store.Store {
	t.Helper()
	var mu sync.Mutex
	o := store.Options{
		DataDir: filepath.Join(t.TempDir(), "data"),
		TxPhaseObserver: func(name string, body, commit time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			if body > MaxTxDuration {
				t.Errorf("write transaction %q took %s (> %s, ADR-0002)", name, body, MaxTxDuration)
			}
			if commit > MaxCommitDuration {
				t.Errorf("write transaction %q commit took %s (> %s, ADR-0002)", name, commit, MaxCommitDuration)
			}
		},
	}
	for _, opt := range opts {
		opt(&o)
	}
	s, err := store.Open(context.Background(), o)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
