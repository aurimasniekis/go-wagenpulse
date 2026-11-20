package dictionary

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
)

func TestBundledLoadIdempotent(t *testing.T) {
	docs, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Kind != "continuous" || len(docs[0].Entries) != 1141 || docs[1].Kind != "historical" || len(docs[1].Entries) != 5140 {
		t.Fatalf("bundled: %d docs", len(docs))
	}
	s := storetest.Open(t)
	svc := New(s, audit.NewWriter(keyringtest.Load(t, s)))
	ctx := context.Background()
	if err := svc.LoadBundled(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.LoadBundled(ctx); err != nil {
		t.Fatal(err)
	}
	vs, err := svc.Versions(ctx)
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions %v %v", vs, err)
	}
	var n int
	if err := s.ReadDB().QueryRow(`SELECT count(*) FROM signals`).Scan(&n); err != nil || n != 1141+5140 {
		t.Fatalf("signals %d %v", n, err)
	}
	var unit, kind string
	if err := s.ReadDB().QueryRow(`SELECT unit, value_kind FROM signals WHERE key = 'cf28f7d9-6201-30b8-82e5-a461968d30dc'`).Scan(&unit, &kind); err != nil ||
		unit != "min" || kind != "number" {
		t.Fatalf("remaining_charging_time: %q %q %v", unit, kind, err)
	}
}

func TestParseRejectsBadDocuments(t *testing.T) {
	for _, bad := range []string{`{}`, `{"format":"wagenpulse.dictionary.v1","kind":"x"}`, `not json`,
		`{"format":"wagenpulse.dictionary.v1","kind":"continuous","document_version":"1","document_date":"2026-01-01","entries":[{"key":"NOT-A-UUID","name":"x"}]}`,
		`{"format":"wagenpulse.dictionary.v1","kind":"continuous","document_version":"1","document_date":"2026-01-01","entries":[],"extra":1}`} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestKindsAndUnits(t *testing.T) {
	kinds := map[string]ValueKind{"int": KindNumber, "Unsigned Integer": KindNumber, "bigint": KindNumber, "float": KindNumber,
		"boolean": KindBoolean, "bool": KindBoolean, "enum": KindEnum, "STRING": KindString, "character varying(2)": KindString,
		"TIMESTAMP": KindTimestamp, "timestamp without time zone": KindTimestamp, "struct": KindJSON, "": KindUnknown, "'-": KindUnknown}
	for in, want := range kinds {
		if got := KindOf(in); got != want {
			t.Errorf("KindOf(%q) = %s want %s", in, got, want)
		}
	}
	units := map[string]string{"Minutes": "min", "(°C)": "°C", "KWh": "kWh", "'-": "", "10kPA / Bar / PSI/ kPA": "10kPA / Bar / PSI/ kPA", "dK": "dK"}
	for in, want := range units {
		if got := NormalizeUnit(in); got != want {
			t.Errorf("NormalizeUnit(%q) = %q want %q", in, got, want)
		}
	}
}

// ADR-0027: two processes (a starting server and an attached command) loading the bundled dictionaries at the same
// time must not collide; each document is stored once.
func TestConcurrentLoadAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	// Two loaders contend for the write lock; SQLite's busy handler is unfair, so allow far more than the 5 s default under -race.
	owner := storetest.Open(t, storetest.WithBusyTimeout(time.Minute))
	kr := keyringtest.Load(t, owner)
	att, err := store.Open(ctx, store.Options{DataDir: owner.Dir(), AllowAttach: true, BusyTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = att.Close() }()
	svcs := []*Service{New(owner, audit.NewWriter(kr)), New(att, audit.NewWriter(kr))}
	errs := make(chan error, len(svcs))
	for _, svc := range svcs {
		go func() { errs <- svc.LoadBundled(ctx) }()
	}
	for range svcs {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent load: %v", err)
		}
	}
	var n int
	if err := owner.ReadDB().QueryRow(`SELECT count(*) FROM dictionary_versions`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("versions=%d err=%v", n, err)
	}
}

// An interrupted load (error after the first batch) leaves a partial version; the next load resumes it.
func TestInterruptedLoadIsResumed(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	svc := New(s, audit.NewWriter(keyringtest.Load(t, s)))
	docs, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	boom := errors.New("injected interruption")
	svc.afterBatch = func(batch int) error {
		if batch == 0 {
			return boom
		}
		return nil
	}
	if _, _, err := svc.load(ctx, d, "bundled", audit.System); !errors.Is(err, boom) {
		t.Fatalf("load err = %v, want injected", err)
	}
	var stored int
	if err := s.ReadDB().QueryRow(`SELECT count(*) FROM dictionary_entries`).Scan(&stored); err != nil || stored != batchSize {
		t.Fatalf("partial entries = %d (%v), want %d", stored, err, batchSize)
	}

	svc.afterBatch = nil
	v, created, err := svc.load(ctx, d, "bundled", audit.System)
	if err != nil || created {
		t.Fatalf("resume: created=%v err=%v", created, err)
	}
	if v.EntryCount != int64(len(d.Entries)) {
		t.Fatalf("entry_count = %d, want %d", v.EntryCount, len(d.Entries))
	}
	var n, sigs, versions int
	if err := s.ReadDB().QueryRow(`SELECT count(*) FROM dictionary_entries WHERE version_id = ?`, v.ID).Scan(&n); err != nil || int64(n) != v.EntryCount {
		t.Fatalf("stored entries = %d (%v), want %d", n, err, v.EntryCount)
	}
	if err := s.ReadDB().QueryRow(`SELECT count(*) FROM signals`).Scan(&sigs); err != nil || sigs != len(d.Entries) {
		t.Fatalf("signals = %d (%v), want %d", sigs, err, len(d.Entries))
	}
	if err := s.ReadDB().QueryRow(`SELECT count(*) FROM dictionary_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("versions = %d (%v)", versions, err)
	}
}

// A document sharing a stored version's source hash but not its content must not be merged into it.
func TestSameHashDifferentContentIsRejected(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	svc := New(s, audit.NewWriter(keyringtest.Load(t, s)))
	docs, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	other := *d
	other.Entries = append(append([]Entry{}, d.Entries...), Entry{Key: "11111111-2222-3333-4444-555555555555", Name: "extra"})

	count := func(id string) (n int) {
		if err := s.ReadDB().QueryRow(`SELECT count(*) FROM dictionary_entries WHERE version_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Complete version: a different document is a no-op and changes nothing.
	v, _, err := svc.load(ctx, d, "bundled", audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.load(ctx, &other, "imported", audit.System); err != nil {
		t.Fatalf("complete version: %v", err)
	}
	if n := count(v.ID); int64(n) != v.EntryCount || n != len(d.Entries) {
		t.Fatalf("complete version changed: %d entries", n)
	}

	// Partial version: a different document is a conflict and touches nothing.
	if err := s.W().Exec(ctx, `DELETE FROM dictionary_versions`); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected interruption")
	svc.afterBatch = func(int) error { return boom }
	if _, _, err := svc.load(ctx, d, "bundled", audit.System); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	svc.afterBatch = nil
	var pid string
	if err := s.ReadDB().QueryRow(`SELECT id FROM dictionary_versions`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	before := count(pid)
	if _, _, err := svc.load(ctx, &other, "imported", audit.System); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("partial version: err = %v, want conflict", err)
	}
	if count(pid) != before {
		t.Fatal("partial version was modified")
	}
}
