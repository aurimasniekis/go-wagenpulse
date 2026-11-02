package dictionary

import (
	"context"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
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
