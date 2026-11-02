package sources

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/jobs"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/providers/vwdataact/fakeportal"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

const vin = "WVWZZZTEST0000001"

type env struct {
	s      *store.Store
	svc    *Service
	col    *Collector
	ing    *ingest.Service
	portal *fakeportal.Portal
	veh    vehicles.Vehicle
}

func synthetic(ts string, soc string) []byte {
	doc, _ := json.Marshal(map[string]any{"vin": vin, "user_id": "u", "Data": []map[string]any{
		{"key": "11111111-1111-3111-8111-111111111111", "dataFieldName": "soc", "value": soc, "timestampUtc": ts},
	}})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(vin + "_20261101060000.json")
	_, _ = w.Write(doc)
	_ = zw.Close()
	return buf.Bytes()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	s := storetest.Open(t)
	kr := keyringtest.Load(t, s)
	aw := audit.NewWriter(kr)
	runner := jobs.NewRunner(s, jobs.Options{})
	blobs, _ := ingest.NewBlobs(filepath.Join(t.TempDir(), "blobs"))
	st := settings.New(s, aw, 24*time.Hour, 48*time.Hour)
	ing := ingest.New(s, blobs, aw, runner, st, ingest.Options{})
	svc := New(s, kr, aw, &secretref.Resolver{EnvPrefix: "WAGENPULSE_SECRET_"}, nil)
	p := fakeportal.New()
	t.Cleanup(p.Close)
	col := NewCollector(svc, ing, runner, CollectorOptions{BaseURL: p.URL(), IdentityBase: p.URL(), HTTPClient: p.Server.Client(), MinInterval: time.Millisecond})
	veh, err := vehicles.New(s, aw).Create(ctx, audit.System, vehicles.Input{DisplayName: "Car", VIN: vin, Brand: "cupra"})
	if err != nil {
		t.Fatal(err)
	}
	return &env{s: s, svc: svc, col: col, ing: ing, portal: p, veh: veh}
}

func (e *env) source(t *testing.T, mode, secret string, user *string) Source {
	t.Helper()
	src, err := e.svc.Create(context.Background(), audit.System, Input{Name: "Portal", Enabled: true, Brand: "cupra", Country: "de", Language: "en",
		AuthMode: mode, Username: user, Secret: &secret, CollectContinuous: true, VehicleIDs: []string{e.veh.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func (e *env) runOnce(t *testing.T, id string) Source {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"source_id": id})
	if err := e.col.run(context.Background(), jobs.Job{ID: "j", Kind: CollectJob, Attempt: 1, MaxAttempts: 1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	s, err := e.svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCookieCollectionDedupAndNoContent(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.Files["req-1"] = []fakeportal.File{
			{Name: "20261101061500_" + vin + ".zip", CreatedOn: now.Add(-15 * time.Minute), Data: synthetic("2026-11-01T06:00:00.000Z", "60")},
			{Name: "20261101063000_" + vin + ".zip", CreatedOn: now, Data: synthetic("2026-11-01T06:00:00.000Z", "60")}, // identical bytes, new name
			{Name: "20261101064500_" + vin + "_no_content_found.zip", CreatedOn: now.Add(-5 * time.Minute), Data: []byte("x")},
		}
	})
	src := e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
	if src.Interface != "unofficial" || !src.SecretSet || src.SecretFingerprint == "" {
		t.Fatalf("source %+v", src)
	}
	got := e.runOnce(t, src.ID)
	if got.Status.State != "authorized" || got.Status.LastSuccessAt == nil || got.Status.NextRunAt == nil {
		t.Fatalf("status %+v", got.Status)
	}
	imps, _ := e.ing.List(context.Background(), ingest.ImportFilter{SourceID: src.ID})
	if len(imps) != 1 || imps[0].Kind != "collection" || imps[0].FilesTotal != 2 || imps[0].Backfill {
		t.Fatalf("imports %+v", imps)
	}
	var outcomes []string
	rows, err := e.s.ReadDB().Query(`SELECT outcome FROM remote_files ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var o string
		_ = rows.Scan(&o)
		outcomes = append(outcomes, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(outcomes, ",") != "duplicate,stored,no_content" { // newest first: the older name repeats its bytes
		t.Fatalf("remote files %v", outcomes)
	}
	// A second run downloads nothing new (checkpoint).
	downloads := e.portal.Hits["/proxy_api/euda-apim/datadelivery"]
	e.runOnce(t, src.ID)
	if after := e.portal.Hits["/proxy_api/euda-apim/datadelivery"]; after != downloads+1 { // one list call, no downloads
		t.Fatalf("second run made %d delivery calls", after-downloads)
	}
	for ua := range e.portal.UserAgents {
		if !strings.HasPrefix(ua, "WagenPulse/") {
			t.Fatalf("dishonest user agent %q", ua)
		}
	}
}

func TestStatesAuthRateLimitNoRequest(t *testing.T) {
	e := newEnv(t)
	src := e.source(t, AuthSessionCookie, "PORTALSESSION=expired", nil)
	if got := e.runOnce(t, src.ID); got.Status.State != "reauthorization_required" {
		t.Fatalf("expired cookie: %+v", got.Status)
	}
	// Scheduler skips sources needing reauthorization.
	if err := e.col.Schedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret := "PORTALSESSION=valid-session"
	in := Input{Name: "Portal", Enabled: true, Brand: "cupra", Country: "de", Language: "en", AuthMode: AuthSessionCookie, Secret: &secret,
		CollectContinuous: true, VehicleIDs: []string{e.veh.ID}}
	if _, err := e.svc.Update(context.Background(), audit.System, src.ID, 0, in); err != nil {
		t.Fatal(err)
	}
	if got := e.runOnce(t, src.ID); got.Status.State != "no_active_request" {
		t.Fatalf("no request: %+v", got.Status)
	}
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.RateLimit, p.RetryAfterSecs = 1, 1800
	})
	got := e.runOnce(t, src.ID)
	if got.Status.State != "rate_limited" || got.Status.RetryAfter == nil || time.Until(*got.Status.RetryAfter) < 25*time.Minute {
		t.Fatalf("rate limit: %+v", got.Status)
	}
}

func TestPasswordLoginAndChallenge(t *testing.T) {
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Email, p.Password = "owner@example.com", "portal-password"
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.Files["req-1"] = []fakeportal.File{{Name: "a.zip", CreatedOn: time.Now(), Data: synthetic("2026-11-01T07:00:00.000Z", "61")}}
	})
	user := "owner@example.com"
	src := e.source(t, AuthPassword, "portal-password", &user)
	if got := e.runOnce(t, src.ID); got.Status.State != "authorized" {
		t.Fatalf("password login: %+v", got.Status)
	}
	// The session is persisted sealed and reused (no new login on the next run).
	logins := e.portal.Hits["/u/login"]
	e.runOnce(t, src.ID)
	if e.portal.Hits["/u/login"] != logins {
		t.Fatal("stored session was not reused")
	}
	// A captcha the identity provider actually demands is never solved or bypassed: one credential POST, rejected,
	// then the source waits for the owner.
	e.portal.Set(func(p *fakeportal.Portal) { p.Captcha, p.SessionCookie = true, "rotated" })
	posts := e.portal.Hits["/u/login"]
	if got := e.runOnce(t, src.ID); got.Status.State != "reauthorization_required" || got.Status.LastErrorClass != "challenge" {
		t.Fatalf("captcha: %+v", got.Status)
	}
	if n := e.portal.Hits["/u/login"] - posts; n != 2 { // the sign-in page GET and a single credential POST
		t.Fatalf("captcha flow hit /u/login %d times, want 2 (no retries)", n)
	}
	// A challenge page in place of the sign-in form stops before any credentials are sent.
	e.portal.Set(func(p *fakeportal.Portal) { p.Captcha, p.Interstitial, p.SessionCookie = false, true, "rotated-again" })
	posts = e.portal.Hits["/u/login"]
	if got := e.runOnce(t, src.ID); got.Status.State != "reauthorization_required" || got.Status.LastErrorClass != "challenge" {
		t.Fatalf("interstitial: %+v", got.Status)
	}
	if e.portal.Hits["/u/login"] != posts {
		t.Fatal("credentials were sent to a challenge page")
	}
}

// Regression: Auth0's sign-in page always ships captcha scripts and an empty widget container. That alone is not a
// challenge; the login must proceed (it was refused before any attempt).
func TestCaptchaMarkupIsNotAChallenge(t *testing.T) {
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Email, p.Password = "owner@example.com", "portal-password"
		p.Requests[vin] = map[string]string{"partial": "req-1"}
	})
	user := "owner@example.com"
	src := e.source(t, AuthPassword, "portal-password", &user)
	if got := e.runOnce(t, src.ID); got.Status.State != "authorized" {
		t.Fatalf("login with captcha markup on the page: %+v", got.Status)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	bad := []Input{
		{Name: "", Brand: "cupra", Country: "de", Language: "en", AuthMode: AuthSessionCookie, CollectContinuous: true},
		{Name: "x", Brand: "tesla", Country: "de", Language: "en", AuthMode: AuthSessionCookie, CollectContinuous: true},
		{Name: "x", Brand: "audi", Country: "de", Language: "en", AuthMode: AuthPassword, CollectContinuous: true}, // no brand key, no user
		{Name: "x", Brand: "cupra", Country: "de", Language: "en", AuthMode: AuthSessionCookie, CollectContinuous: true, IntervalMinutes: 5},
		{Name: "x", Brand: "cupra", Country: "de", Language: "en", AuthMode: AuthSessionCookie, SecretRef: "env:HOME"},
	}
	for i, in := range bad {
		if _, err := e.svc.Create(context.Background(), audit.System, in); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
