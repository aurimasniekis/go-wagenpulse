package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/adapters"
	"github.com/aurimasniekis/go-wagenpulse/internal/adapters/pulsetest"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/netguard"
	"github.com/aurimasniekis/go-wagenpulse/internal/routes"
	"github.com/aurimasniekis/go-wagenpulse/internal/rules"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

const (
	vin    = "WVWZZZTEST0000009"
	keySOC = "11111111-1111-3111-8111-111111111111"
)

type pipeline struct {
	s       *store.Store
	ing     *ingest.Service
	runner  *jobs.Runner
	adapts  *adapters.Service
	mgr     *adapters.Manager
	evs     *events.Service
	rules   *rules.Service
	routes  *routes.Service
	veh     vehicles.Vehicle
	nats    *pulsetest.Env
	minutes int
}

func setup(t *testing.T) *pipeline {
	t.Helper()
	ctx := context.Background()
	s := storetest.Open(t)
	kr := keyringtest.Load(t, s)
	aw := audit.NewWriter(kr)
	dict := dictionary.New(s, aw)
	if _, _, err := dict.Import(ctx, audit.System, &dictionary.Document{Format: dictionary.Format, Kind: "continuous", Title: "t", DocumentVersion: "1",
		DocumentDate: "2026-01-01", SourceSHA256: "x", Entries: []dictionary.Entry{{Key: keySOC, Name: "soc", Unit: "%", DataType: "int"}}}); err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(s, jobs.Options{PollInterval: 10 * time.Millisecond})
	blobs, _ := ingest.NewBlobs(filepath.Join(t.TempDir(), "blobs"))
	ing := ingest.New(s, blobs, aw, runner, settings.New(s, aw, 24*time.Hour, 48*time.Hour), ingest.Options{})
	engine := rules.NewEngine(s, nil)
	ing.SetSink(engine)
	refs := &secretref.Resolver{EnvPrefix: "WAGENPULSE_SECRET_"}
	adapts := adapters.NewService(s, kr, aw, refs)
	guard, _ := netguard.NewWithOptions(netguard.Options{AllowCIDRs: []string{"127.0.0.1/32"}})
	mgr := adapters.NewManager(adapts, adapters.ManagerOptions{Guard: guard, InstallationID: "ins_test", BaseBackoff: 50 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond, Poll: 20 * time.Millisecond})
	engine.OnEvents = mgr.Wake
	veh, err := vehicles.New(s, aw).Create(ctx, audit.System, vehicles.Input{DisplayName: "Family Car", VIN: vin, Brand: "volkswagen"})
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline{s: s, ing: ing, runner: runner, adapts: adapts, mgr: mgr, evs: events.NewService(s, aw), rules: rules.NewService(s, aw, nil),
		routes: routes.NewService(s, aw), veh: veh, nats: pulsetest.Start(t, pulsetest.Options{})}
}

func (p *pipeline) ingest(t *testing.T, policy string, backfill bool, values ...string) ingest.Import {
	t.Helper()
	ctx := context.Background()
	var recs []map[string]any
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute).Add(time.Duration(p.minutes) * time.Minute)
	p.minutes += len(values)
	for i, v := range values {
		recs = append(recs, map[string]any{"key": keySOC, "dataFieldName": "soc", "value": v,
			"timestampUtc": base.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z")})
	}
	b, _ := json.Marshal(map[string]any{"vin": vin, "Data": recs})
	d, err := p.ing.StoreRaw(ctx, bytes.NewReader(b), ingest.RawMeta{Origin: "upload", Limit: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	imp, err := p.ing.Create(ctx, ingest.CreateInput{Kind: ingest.KindUpload, EventPolicy: policy, Backfill: backfill, Deliveries: []ingest.StoredDelivery{d},
		Actor: audit.System})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"import_id": imp.ID})
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	go p.runner.Run(runCtx)
	for {
		got, _ := p.ing.Get(ctx, imp.ID)
		if got.State != "queued" && got.State != "running" {
			return got
		}
		if runCtx.Err() != nil {
			t.Fatalf("import did not finish: %+v (%s)", got, payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestPipelineToPulse(t *testing.T) {
	p := setup(t)
	ctx := context.Background()
	good := string(p.nats.CredsBytes("wagenpulse", pulsetest.Grants{Publish: []string{"evt.wagenpulse.>"}}))
	noGrant := string(p.nats.CredsBytes("nogrant", pulsetest.Grants{Publish: []string{"evt.other.>"}}))
	a1, err := p.adapts.Create(ctx, audit.System, adapters.Input{Name: "pulse-home", Enabled: true, Creds: &good,
		Config: adapters.PulseConfig{URL: p.nats.URL, PublishTimeoutMS: 1000, MaxAttempts: 3}})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := p.adapts.Create(ctx, audit.System, adapters.Input{Name: "pulse-misconfigured", Enabled: true, Creds: &noGrant,
		Config: adapters.PulseConfig{URL: p.nats.URL, PublishTimeoutMS: 1000, MaxAttempts: 2}})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := p.rules.Create(ctx, audit.System, rules.Input{Name: "SoC high", Enabled: true, Definition: rules.Definition{Kind: rules.KindThreshold,
		SignalKeys: []string{keySOC}, Direction: "above", Threshold: 80, Hysteresis: 5, Severity: "warning"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []adapters.Adapter{a1, a2} {
		if _, err := p.routes.Create(ctx, audit.System, routes.Input{Name: "to " + a.Name, Enabled: true, AdapterID: a.ID, RuleIDs: []string{rule.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	mctx, stop := context.WithCancel(ctx)
	defer stop()
	go p.mgr.Run(mctx)

	imp := p.ingest(t, "publish", false, "70", "85", "90", "70", "86")
	if imp.EventsCreated != 2 { // 85 crosses; 90 stays above; 70 re-arms (<= 75); 86 crosses again
		t.Fatalf("events %+v", imp)
	}
	waitFor(t, "deliveries to the healthy adapter", func() bool {
		a, _ := p.adapts.Get(ctx, a1.ID)
		return a.Queue.Succeeded == 2
	})
	msgs := p.nats.Stored("evt.wagenpulse.>")
	if len(msgs) != 2 || msgs[0].Subject != "evt.wagenpulse.vehicle.threshold_crossed.family-car" {
		t.Fatalf("stored %d messages, first subject %q", len(msgs), msgs[0].Subject)
	}
	var env events.Envelope
	if err := json.Unmarshal(msgs[0].Data, &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 1 || env.Type != events.TypeThresholdCrossed || env.Vehicle.VIN != "" || env.Rule.ID != rule.ID || *env.Value.Num != 85 ||
		env.Threshold.Value != 80 || msgs[0].Header.Get("ce-type") != "wagenpulse.threshold.crossed.v1" || msgs[0].Header.Get("Nats-Msg-Id") == "" {
		t.Fatalf("envelope %+v headers %v", env, msgs[0].Header)
	}
	// The adapter without a publish grant times out and dead-letters without affecting the healthy one.
	waitFor(t, "dead deliveries on the misconfigured adapter", func() bool {
		a, _ := p.adapts.Get(ctx, a2.ID)
		return a.Queue.Dead == 2
	})
	ds, _, _ := p.evs.Deliveries(ctx, events.DeliveryFilter{AdapterID: a2.ID})
	d, _ := p.evs.Delivery(ctx, ds[0].ID)
	if d.LastErrorClass != adapters.ClassTimeout || len(d.Attempt) != 2 || !strings.Contains(d.LastError, "grant") {
		t.Fatalf("misconfigured delivery %+v", d)
	}

	// Backfill events reach only routes that opted in (none here): created but not delivered.
	bf := p.ingest(t, "publish", true, "60", "95")
	if bf.EventsCreated != 1 {
		t.Fatalf("backfill %+v", bf)
	}
	evs, _, _ := p.evs.Events(ctx, events.EventFilter{ImportID: bf.ID})
	if len(evs) != 1 || !evs[0].Backfill || evs[0].Deliveries.Total != 0 {
		t.Fatalf("backfill events %+v", evs)
	}

	// Outage: events wait in the outbox and are delivered after recovery.
	p.nats.Shutdown()
	out := p.ingest(t, "publish", false, "50", "99")
	if out.EventsCreated != 1 {
		t.Fatalf("outage import %+v", out)
	}
	time.Sleep(300 * time.Millisecond)
	p.nats.Restart()
	waitFor(t, "delivery after the outage", func() bool {
		a, _ := p.adapts.Get(ctx, a1.ID)
		return a.Queue.Succeeded == 3 && a.Queue.Pending == 0 && a.Queue.Retrying == 0
	})

	// Replay: preview, then confirm; replays publish again with a new Nats-Msg-Id.
	req := events.ReplayRequest{AdapterID: a1.ID, From: time.Now().Add(-24 * time.Hour), To: time.Now().Add(time.Hour), Types: []string{events.TypeThresholdCrossed}}
	prev, err := p.evs.Replay(ctx, audit.System, req)
	if err != nil || prev.Events != 4 || prev.Confirmed {
		t.Fatalf("preview %+v %v", prev, err)
	}
	req.Confirm = true
	res, err := p.evs.Replay(ctx, audit.System, req)
	if err != nil || res.Created != 4 {
		t.Fatalf("replay %+v %v", res, err)
	}
	waitFor(t, "replayed deliveries", func() bool { return len(p.nats.Stored("evt.wagenpulse.>")) == 7 })
}

func TestAdapterValidationAndTest(t *testing.T) {
	p := setup(t)
	ctx := context.Background()
	creds := string(p.nats.CredsBytes("wagenpulse", pulsetest.Grants{Publish: []string{"evt.wagenpulse.>"}}))
	bad := []adapters.Input{
		{Name: "x", Creds: &creds, Config: adapters.PulseConfig{URL: "http://example.com"}},
		{Name: "x", Creds: &creds, Config: adapters.PulseConfig{URL: "nats://user:pass@example.com:4222"}},
		{Name: "x", Creds: &creds, Config: adapters.PulseConfig{URL: p.nats.URL, SubjectTemplate: "events.{domain}"}},
		{Name: "x", Creds: &creds, Config: adapters.PulseConfig{URL: p.nats.URL, SubjectTemplate: "evt.{domain}.{unknown}.x"}},
		{Name: "x", Config: adapters.PulseConfig{URL: p.nats.URL}},
	}
	for i, in := range bad {
		if _, err := p.adapts.Create(ctx, audit.System, in); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	a, err := p.adapts.Create(ctx, audit.System, adapters.Input{Name: "ok", Enabled: true, Creds: &creds, Config: adapters.PulseConfig{URL: p.nats.URL,
		PublishTimeoutMS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if a.SecretFingerprint == "" || !a.CredsSet {
		t.Fatalf("adapter %+v", a)
	}
	res, err := p.mgr.Test(ctx, audit.System, a.ID)
	if err != nil || !res.OK || res.Subject != "evt.wagenpulse.vehicle.adapter_test.test" || res.Sequence == 0 {
		t.Fatalf("test %+v %v", res, err)
	}
	// SSRF guard: a destination outside the allowed ranges is refused at dial time.
	guard, _ := netguard.NewWithOptions(netguard.Options{})
	strict := adapters.NewManager(p.adapts, adapters.ManagerOptions{Guard: guard})
	res, _ = strict.Test(ctx, audit.System, a.ID)
	if res.OK {
		t.Fatalf("loopback destination allowed: %+v", res)
	}
}
