package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// Synthetic fixture: keys and VINs are invented; the structure mirrors the verified continuous format (E1).
const (
	testVIN    = "WVWZZZTEST0000001"
	otherVIN   = "WVWZZZTEST0000002"
	keySOC     = "11111111-1111-3111-8111-111111111111" // number, %
	keyRange   = "22222222-2222-3222-8222-222222222222" // number, km
	keyLocked  = "33333333-3333-3333-8333-333333333333" // boolean
	keyState   = "44444444-4444-3444-8444-444444444444" // enum
	keyUnknown = "55555555-5555-3555-8555-555555555555" // not in the dictionary
)

type env struct {
	s   *store.Store
	svc *Service
	veh vehicles.Vehicle
	vs  *vehicles.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	s := storetest.Open(t)
	aw := audit.NewWriter(keyringtest.Load(t, s))
	dict := dictionary.New(s, aw)
	doc := &dictionary.Document{Format: dictionary.Format, Kind: "continuous", Title: "synthetic", DocumentVersion: "1", DocumentDate: "2026-01-01",
		SourceSHA256: "synthetic", Entries: []dictionary.Entry{
			{Key: keySOC, Name: "battery_state_report.soc", Unit: "%", DataType: "int"},
			{Key: keyRange, Name: "cruising_range_combined", Unit: "km", DataType: "int"},
			{Key: keyLocked, Name: "locked_state", DataType: "boolean"},
			{Key: keyState, Name: "charging_state", DataType: "enum"},
		}}
	if _, _, err := dict.Import(ctx, audit.System, doc); err != nil {
		t.Fatal(err)
	}
	blobs, err := NewBlobs(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(s, jobs.Options{})
	st := settings.New(s, aw, 24*time.Hour, 48*time.Hour)
	svc := New(s, blobs, aw, runner, st, Options{BatchSize: 3})
	vs := vehicles.New(s, aw)
	veh, err := vs.Create(ctx, audit.System, vehicles.Input{DisplayName: "Test Car", VIN: testVIN, Brand: "cupra"})
	if err != nil {
		t.Fatal(err)
	}
	return &env{s: s, svc: svc, veh: veh, vs: vs}
}

type rec struct {
	key, name string
	value     any // string, nil, or omitted when absentValue
	ts        string
	absent    bool
}

func delivery(vin string, recs ...rec) []byte {
	data := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		m := map[string]any{"key": r.key, "dataFieldName": r.name}
		if !r.absent {
			m["value"] = r.value
		}
		if r.ts != "" {
			m["timestampUtc"] = r.ts
		}
		data = append(data, m)
	}
	b, _ := json.Marshal(map[string]any{"vin": vin, "user_id": "synthetic-user", "Data": data})
	return b
}

func zipOf(files map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// upload stores files and runs the import job synchronously.
func (e *env) upload(t *testing.T, vehicleNum int64, policy string, files ...[]byte) Import {
	t.Helper()
	ctx := context.Background()
	var ds []StoredDelivery
	for i, f := range files {
		d, err := e.svc.StoreRaw(ctx, bytes.NewReader(f), RawMeta{Origin: "upload", VehicleNum: vehicleNum, OriginalName: fmt.Sprintf("f%d.zip", i), Limit: 10 << 20})
		if err != nil {
			t.Fatal(err)
		}
		ds = append(ds, d)
	}
	imp, err := e.svc.Create(ctx, CreateInput{Kind: KindUpload, VehicleNum: vehicleNum, EventPolicy: policy, Backfill: true, Deliveries: ds, Actor: audit.System})
	if err != nil {
		t.Fatal(err)
	}
	e.run(t, imp.ID)
	out, err := e.svc.Get(ctx, imp.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) run(t *testing.T, id string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"import_id": id})
	if err := e.svc.runJob(context.Background(), jobs.Job{ID: "job", Kind: JobKind, Attempt: 1, MaxAttempts: 5, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) latest(t *testing.T, key string) (num *float64, text *string, observedAt time.Time) {
	t.Helper()
	var at int64
	err := e.s.ReadDB().QueryRow(`SELECT l.num, l.text, l.observed_at FROM latest_state l JOIN signals s ON s.num = l.signal_num
		WHERE l.vehicle_num = ? AND s.key = ?`, e.veh.Num, key).Scan(&num, &text, &at)
	if err != nil {
		t.Fatalf("latest %s: %v", key, err)
	}
	return num, text, time.UnixMicro(at).UTC()
}

func base() []rec {
	return []rec{
		{key: keySOC, name: "battery_state_report.soc", value: "69", ts: "2026-11-01T06:00:00.000Z"},
		{key: keyRange, name: "cruising_range_combined", value: "312", ts: "2026-11-01T06:00:00.000Z"},
		{key: keyLocked, name: "locked_state", value: "true", ts: "2026-11-01T06:01:00.000Z"},
		{key: keyState, name: "charging_state", value: "CHARGE_STATE_READY", ts: "2026-11-01T06:01:00.000Z"},
		{key: keyUnknown, name: "mystery_field", value: "4294967295", ts: "2026-11-01T06:02:00.000Z"},
		{key: keySOC + "x", name: "broken", value: "1"}, // invalid key: skipped with a warning
		{key: keyRange, name: "cruising_range_combined", value: nil, ts: "2026-11-01T05:00:00.000Z"},
	}
}

func TestImportDedupOverlapAndOrder(t *testing.T) {
	e := newEnv(t)
	d1 := zipOf(map[string][]byte{testVIN + "_20261101060500.json": delivery(testVIN, base()...)})
	imp := e.upload(t, 0, "none", d1)
	if imp.State != "succeeded" || imp.ObservationsAccepted != 6 || imp.UnknownSignals != 1 || imp.VehicleID != e.veh.ID {
		t.Fatalf("first import %+v", imp)
	}
	if len(imp.Warnings) == 0 {
		t.Fatal("expected a warning for the invalid key")
	}
	num, _, at := e.latest(t, keySOC)
	if *num != 69 || !at.Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("soc %v at %v", *num, at)
	}
	var quality string
	_ = e.s.ReadDB().QueryRow(`SELECT l.quality FROM latest_state l JOIN signals s ON s.num = l.signal_num WHERE s.key = ?`, keyUnknown).Scan(&quality)
	if quality != "unset" {
		t.Fatalf("sentinel quality %q", quality)
	}
	// Byte-identical re-upload is a duplicate delivery.
	imp2 := e.upload(t, e.veh.Num, "none", d1)
	if imp2.DeliveriesDuplicate != 1 || imp2.ObservationsAccepted != 0 {
		t.Fatalf("duplicate upload %+v", imp2)
	}
	// Overlapping newer delivery: unchanged values at the same time are duplicates; new values are accepted.
	newer := append(base()[:4:4], rec{key: keySOC, name: "battery_state_report.soc", value: "70", ts: "2026-11-01T06:15:00.000Z"})
	imp3 := e.upload(t, 0, "none", zipOf(map[string][]byte{testVIN + "_20261101062000.json": delivery(testVIN, newer...)}))
	if imp3.ObservationsAccepted != 1 || imp3.ObservationsDuplicate != 4 {
		t.Fatalf("overlap %+v", imp3)
	}
	// Late (older) delivery must not replace newer latest state.
	older := []rec{{key: keySOC, name: "battery_state_report.soc", value: "50", ts: "2026-10-31T06:00:00.000Z"}}
	imp4 := e.upload(t, 0, "none", zipOf(map[string][]byte{testVIN + "_20261031060500.json": delivery(testVIN, older...)}))
	if imp4.ObservationsAccepted != 1 {
		t.Fatalf("late %+v", imp4)
	}
	if num, _, _ := e.latest(t, keySOC); *num != 70 {
		t.Fatalf("late data replaced latest state: %v", *num)
	}
	// Same timestamp, different value: a correction that wins in latest state.
	corr := []rec{{key: keySOC, name: "battery_state_report.soc", value: "71", ts: "2026-11-01T06:15:00.000Z"}}
	imp5 := e.upload(t, 0, "none", delivery(testVIN, corr...))
	if imp5.Corrections != 1 {
		t.Fatalf("correction %+v", imp5)
	}
	if num, _, _ := e.latest(t, keySOC); *num != 71 {
		t.Fatalf("correction not applied: %v", *num)
	}
	// Re-running a finished import is a no-op (idempotent jobs).
	e.run(t, imp5.ID)
}

func TestRejections(t *testing.T) {
	e := newEnv(t)
	slip := zipOf(map[string][]byte{"../../etc/x.json": delivery(testVIN, base()...)})
	nested := zipOf(map[string][]byte{"inner.zip": zipOf(map[string][]byte{"a.json": []byte("{}")})})
	cases := map[string]struct {
		data    []byte
		vehicle int64
		code    string
	}{
		"path traversal": {slip, 0, "unsafe_entry"},
		"nested archive": {nested, 0, "nested_archive"},
		"not json":       {[]byte("hello world"), 0, "unsupported_content"},
		"broken json":    {[]byte(`{"vin": "x", "Data": [`), 0, "malformed_json"},
		"vin mismatch":   {delivery(otherVIN, base()...), e.veh.Num, "vin_mismatch"},
		"unknown vin":    {delivery(otherVIN, base()[:2]...), 0, "vehicle_unknown"},
		"empty zip":      {zipOf(map[string][]byte{"readme.txt": []byte("x")}), 0, "no_documents"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			imp := e.upload(t, c.vehicle, "none", c.data)
			if imp.State != "failed" || imp.DeliveriesRejected != 1 {
				t.Fatalf("%+v", imp)
			}
			ds, _ := e.svc.ImportDeliveries(context.Background(), imp.ID)
			if len(ds) != 1 || !strings.HasPrefix(ds[0].Message, c.code) || ds[0].Status != "rejected" {
				t.Fatalf("deliveries %+v", ds)
			}
		})
	}
}

func TestZipBomb(t *testing.T) {
	big := bytes.Repeat([]byte(" "), 8<<20)
	data := zipOf(map[string][]byte{"a.json": append(append([]byte("{"), big...), '}')})
	f := filepath.Join(t.TempDir(), "bomb.zip")
	if err := os.WriteFile(f, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h, _ := os.Open(f)
	defer h.Close()
	_, err := ReadDelivery(h, int64(len(data)), Limits{MaxExtractedBytes: 4 << 20, MaxEntries: 10, MaxRatio: 100, MaxEntryBytes: 64 << 20})
	if err == nil || !strings.Contains(err.Error(), "compression_ratio") && !strings.Contains(err.Error(), "too_large") {
		t.Fatalf("bomb accepted: %v", err)
	}
}

func TestUnrecognizedThenReprocess(t *testing.T) {
	e := newEnv(t)
	hist := []byte(`{"VTS":{"vehicles":[{"registrationCountry":"DE"}]},"RS":{"actionSessions":[]}}`)
	imp := e.upload(t, e.veh.Num, "none", hist)
	if imp.DeliveriesUnrecognized != 1 || imp.State != "failed" {
		t.Fatalf("%+v", imp)
	}
	ds, _ := e.svc.ImportDeliveries(context.Background(), imp.ID)
	if ds[0].Status != "unrecognized" || !strings.Contains(ds[0].Message, "fingerprint") || strings.Contains(ds[0].Message, "DE") {
		t.Fatalf("delivery %+v", ds[0])
	}
	re, err := e.svc.Reprocess(context.Background(), audit.System, []string{ds[0].ID}, "")
	if err != nil || re.EventPolicy != "none" || re.Kind != KindReprocess {
		t.Fatalf("reprocess %+v %v", re, err)
	}
	e.run(t, re.ID)
}

func TestCancelAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d, err := e.svc.StoreRaw(ctx, bytes.NewReader(delivery(testVIN, base()...)), RawMeta{Origin: "upload", Limit: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	imp, err := e.svc.Create(ctx, CreateInput{Kind: KindUpload, EventPolicy: "none", Deliveries: []StoredDelivery{d}, Actor: audit.System})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(ctx, audit.System, imp.ID); err != nil {
		t.Fatal(err)
	}
	e.run(t, imp.ID)
	got, _ := e.svc.Get(ctx, imp.ID)
	if got.State != "cancelled" || got.ObservationsAccepted != 0 {
		t.Fatalf("cancelled import ran: %+v", got)
	}
	if _, err := e.svc.Retry(ctx, audit.System, imp.ID); err != nil {
		t.Fatal(err)
	}
	e.run(t, imp.ID)
	got, _ = e.svc.Get(ctx, imp.ID)
	if got.State != "succeeded" || got.ObservationsAccepted != 6 {
		t.Fatalf("retried import %+v", got)
	}
}

func TestMemberTime(t *testing.T) {
	vin, at := MemberTime("folder/" + testVIN + "_20261102112251.json")
	if vin != testVIN || at == nil || !at.Equal(time.Date(2026, 11, 2, 11, 22, 51, 0, time.UTC)) {
		t.Fatalf("%s %v", vin, at)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(delivery(testVIN, base()...))
	f.Add([]byte(`{"Data":[{"key":1}]}`))
	f.Add([]byte(`[1,2,3]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(Document{Name: "x.json", Data: data})
	})
}
