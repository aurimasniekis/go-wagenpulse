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
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
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
	e.portal.Set(func(p *fakeportal.Portal) { p.Requests[vin] = map[string]string{} }) // the account holds the vehicle, no request
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

// A signed-in source without bound vehicles names the WagenPulse vehicles the portal account holds (never the VIN).
func TestUnboundSourceNamesMatchingVehicles(t *testing.T) {
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) { p.Requests[vin] = map[string]string{"partial": "req-1"} })
	secret := "PORTALSESSION=valid-session"
	src, err := e.svc.Create(context.Background(), audit.System, Input{Name: "Portal", Enabled: true, Brand: "cupra", Country: "de",
		Language: "en", AuthMode: AuthSessionCookie, Secret: &secret, CollectContinuous: true})
	if err != nil {
		t.Fatal(err)
	}
	got := e.runOnce(t, src.ID).Status
	if got.State != "authorized" || !strings.Contains(got.Reason, "matching: Car") || strings.Contains(got.Reason, vin) {
		t.Fatalf("unbound status: %+v", got)
	}
}

// Regression: a stored portal session that expired was reported as "no active data request" (a success) and the
// collector never signed in again, so deliveries stopped although the request was active. An expired session does not
// necessarily answer 401: it can redirect into the sign-in flow (an HTML page with status 200) or answer like an
// account with nothing on it. In password mode the collector now signs in again (once) and collects.
func TestExpiredSessionSignsInAgainInsteadOfNoRequest(t *testing.T) {
	for _, mode := range []string{fakeportal.ExpiredUnauthorized, fakeportal.ExpiredRedirect, fakeportal.ExpiredEmpty} {
		t.Run("expired="+mode, func(t *testing.T) {
			e := newEnv(t)
			now := time.Now().UTC().Truncate(time.Second)
			e.portal.Set(func(p *fakeportal.Portal) {
				p.Email, p.Password = "owner@example.com", "portal-password"
				p.Requests[vin] = map[string]string{"partial": "req-1"}
				p.Files["req-1"] = []fakeportal.File{{Name: "a.zip", CreatedOn: now.Add(-time.Hour), Data: synthetic("2026-11-01T07:00:00.000Z", "61")}}
			})
			user := "owner@example.com"
			src := e.source(t, AuthPassword, "portal-password", &user)
			if got := e.runOnce(t, src.ID); got.Status.State != "authorized" {
				t.Fatalf("first run: %+v", got.Status)
			}
			// The stored session expires; a new delivery is waiting on the portal.
			e.portal.Set(func(p *fakeportal.Portal) {
				p.SessionCookie, p.Expired = "fresh-session", mode
				p.Files["req-1"] = append(p.Files["req-1"], fakeportal.File{Name: "b.zip", CreatedOn: now, Data: synthetic("2026-11-01T07:15:00.000Z", "62")})
			})
			logins := e.portal.Hits["/login"]
			got := e.runOnce(t, src.ID)
			if got.Status.State != "authorized" {
				t.Fatalf("expired session reported as %+v", got.Status)
			}
			if n := e.portal.Hits["/login"] - logins; n != 1 {
				t.Fatalf("signed in %d times in one run, want 1", n)
			}
			var stored int
			if err := e.s.ReadDB().QueryRow(`SELECT count(*) FROM remote_files WHERE name = 'b.zip' AND outcome = 'stored'`).Scan(&stored); err != nil || stored != 1 {
				t.Fatalf("new delivery not collected after signing in again (%d, %v)", stored, err)
			}
		})
	}
}

// In cookie mode an expired session cannot be renewed: it must surface as reauthorization_required, not as "no active
// request".
func TestExpiredCookieSessionNeedsReauthorization(t *testing.T) {
	for _, mode := range []string{fakeportal.ExpiredRedirect, fakeportal.ExpiredEmpty} {
		t.Run("expired="+mode, func(t *testing.T) {
			e := newEnv(t)
			e.portal.Set(func(p *fakeportal.Portal) { p.Requests[vin] = map[string]string{"partial": "req-1"} })
			src := e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
			first := e.runOnce(t, src.ID).Status
			if first.State != "authorized" {
				t.Fatalf("first run: %+v", first)
			}
			// The cookie that worked expires.
			e.portal.Set(func(p *fakeportal.Portal) { p.SessionCookie, p.Expired = "fresh-session", mode })
			got := e.runOnce(t, src.ID).Status
			if got.State != "reauthorization_required" || !got.LastSuccessAt.Equal(*first.LastSuccessAt) {
				t.Fatalf("expired cookie reported as %+v", got)
			}
		})
	}
}

// A cookie that was just entered is trusted: an account without vehicles is reported as such, not as an expired session.
func TestFreshCookieWithoutVehiclesIsNotExpired(t *testing.T) {
	e := newEnv(t)
	src := e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
	got := e.runOnce(t, src.ID).Status
	if got.State != "no_active_request" || got.LastErrorClass != "no_vehicles" || strings.Contains(got.Reason, "expired") {
		t.Fatalf("fresh cookie, no vehicles: %+v", got)
	}
}

// Signing in again is bounded to one login per run, and a login that meets a challenge is a failure, not a success.
func TestReloginIsBoundedAndChallengeIsAFailure(t *testing.T) {
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Email, p.Password = "owner@example.com", "portal-password"
		p.Requests[vin] = map[string]string{"partial": "req-1"}
	})
	user := "owner@example.com"
	src := e.source(t, AuthPassword, "portal-password", &user)
	if got := e.runOnce(t, src.ID); got.Status.State != "authorized" {
		t.Fatalf("first run: %+v", got.Status)
	}
	// The account lists no vehicles even with a working session: one fresh login rules out an expired session. Once
	// a fresh session confirmed it, later runs do not sign in again until "collect now".
	e.portal.Set(func(p *fakeportal.Portal) { delete(p.Requests, vin) })
	for i, want := range []int{1, 0, 0} {
		logins := e.portal.Hits["/login"]
		got := e.runOnce(t, src.ID)
		if got.Status.State != "no_active_request" || got.Status.LastErrorClass != "no_vehicles" || !strings.Contains(got.Status.Reason, "lists no vehicles") {
			t.Fatalf("run %d, no vehicles: %+v", i, got.Status)
		}
		if n := e.portal.Hits["/login"] - logins; n != want {
			t.Fatalf("run %d signed in %d times, want %d", i, n, want)
		}
	}
	if err := e.col.CollectNow(context.Background(), audit.System, src.ID); err != nil {
		t.Fatal(err)
	}
	logins := e.portal.Hits["/login"]
	e.runOnce(t, src.ID)
	if n := e.portal.Hits["/login"] - logins; n != 1 {
		t.Fatalf("after collect now signed in %d times, want 1", n)
	}
	// The session expires and the sign-in now demands a captcha: a failure that needs the owner.
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.SessionCookie, p.Expired, p.Captcha = "fresh-session", fakeportal.ExpiredRedirect, true
	})
	posts := e.portal.Hits["/u/login"]
	got := e.runOnce(t, src.ID)
	if got.Status.State != "reauthorization_required" || got.Status.LastErrorClass != "challenge" {
		t.Fatalf("challenge on re-login: %+v", got.Status)
	}
	// The followed redirect of the expired session, then one login: the sign-in page GET and a single credential POST.
	if n := e.portal.Hits["/u/login"] - posts; n != 3 {
		t.Fatalf("re-login hit /u/login %d times, want 3", n)
	}
}

// A source that keeps reporting "no active request" stops counting as a silent success.
func TestPersistentNoRequestIsSurfaced(t *testing.T) {
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) { p.Requests[vin] = map[string]string{} })
	src := e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
	var last Source
	for i := 1; i < stuckNoRequestRuns; i++ {
		last = e.runOnce(t, src.ID)
		if last.Status.State != "no_active_request" || last.Status.LastSuccessAt == nil || last.Status.ConsecutiveFailures != int64(i) {
			t.Fatalf("run %d: %+v", i, last.Status)
		}
	}
	got := e.runOnce(t, src.ID).Status
	if got.State != "no_active_request" || got.ConsecutiveFailures != stuckNoRequestRuns || !got.LastSuccessAt.Equal(*last.Status.LastSuccessAt) ||
		!strings.Contains(got.Reason, "consecutive runs") {
		t.Fatalf("persistent no request: %+v", got)
	}
	// A request appears: the streak ends.
	e.portal.Set(func(p *fakeportal.Portal) { p.Requests[vin] = map[string]string{"partial": "req-1"} })
	if got := e.runOnce(t, src.ID).Status; got.State != "authorized" || got.ConsecutiveFailures != 0 {
		t.Fatalf("after a request appeared: %+v", got)
	}
}

// logEnv routes the collector's log into a buffer (JSON, as in production).
func (e *env) captureLog() *bytes.Buffer {
	var buf bytes.Buffer
	l, _ := obs.NewLogger(&buf, "debug", "json")
	e.col.log = obs.Component(l, "collector")
	return &buf
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("log line %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func runLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range logLines(t, buf) {
		if msg, _ := m["message"].(string); strings.HasPrefix(msg, "collection run ") {
			out = append(out, m)
		}
	}
	return out
}

func TestFailureReasonNamesStatusAndEndpoint(t *testing.T) {
	// vehicles: a source without bound vehicles only probes the vehicle list.
	e := newEnv(t)
	e.portal.Set(func(p *fakeportal.Portal) { p.Fail = map[string]int{"vehicles": 404} })
	secret := "PORTALSESSION=valid-session"
	src, err := e.svc.Create(context.Background(), audit.System, Input{Name: "Portal", Enabled: true, Brand: "cupra", Country: "de", Language: "en",
		AuthMode: AuthSessionCookie, Secret: &secret, CollectContinuous: true})
	if err != nil {
		t.Fatal(err)
	}
	got := e.runOnce(t, src.ID)
	if got.Status.State != "error" || !strings.Contains(got.Status.Reason, "unexpected status (404 from vehicles)") {
		t.Fatalf("vehicles: %+v", got.Status)
	}

	// download: a listed file that cannot be downloaded.
	e = newEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.Files["req-1"] = []fakeportal.File{{Name: "20261101061500_" + vin + ".zip", CreatedOn: now, Data: synthetic("2026-11-01T06:00:00.000Z", "60")}}
		p.Fail = map[string]int{"download": 404}
	})
	src = e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
	got = e.runOnce(t, src.ID)
	if got.Status.State != "error" || !strings.Contains(got.Status.Reason, "unexpected status (404 from download)") {
		t.Fatalf("download: %+v", got.Status)
	}
	if strings.Contains(got.Status.Reason, vin) || strings.Contains(got.Status.Reason, "http") {
		t.Fatalf("reason leaks a URL or VIN: %q", got.Status.Reason)
	}
}

func TestFailedRunIsLoggedWithCorrelationID(t *testing.T) {
	e := newEnv(t)
	buf := e.captureLog()
	now := time.Now().UTC().Truncate(time.Second)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.Files["req-1"] = []fakeportal.File{{Name: "20261101061500_" + vin + ".zip", CreatedOn: now, Data: synthetic("2026-11-01T06:00:00.000Z", "60")}}
		p.Fail = map[string]int{"download": 404}
	})
	const cookie = "PORTALSESSION=valid-session"
	src := e.source(t, AuthSessionCookie, cookie, nil)
	got := e.runOnce(t, src.ID)
	lines := runLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("want one run line, got %d: %s", len(lines), buf.String())
	}
	l := lines[0]
	if l["log.level"] != "warn" {
		t.Fatalf("level: %v", l)
	}
	cid, _ := l["wagenpulse.correlation_id"].(string)
	if cid == "" || cid != got.Status.CorrelationID {
		t.Fatalf("log correlation %q, source API %q", cid, got.Status.CorrelationID)
	}
	if l["source_id"] != src.ID {
		t.Fatalf("source id missing: %v", l)
	}
	if l["class"] != "protocol" || l["endpoint"] != "download" || l["status"] != float64(404) || l["attempt"] != float64(1) {
		t.Fatalf("fields: %v", l)
	}
	if _, ok := l["duration_ms"]; !ok {
		t.Fatalf("duration missing: %v", l)
	}
	for _, secret := range []string{vin, "valid-session", "PORTALSESSION", "http://", "https://"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("log contains %q: %s", secret, buf.String())
		}
	}
}

func TestSuccessfulRunLogsOneInfoLine(t *testing.T) {
	e := newEnv(t)
	buf := e.captureLog()
	now := time.Now().UTC().Truncate(time.Second)
	e.portal.Set(func(p *fakeportal.Portal) {
		p.Requests[vin] = map[string]string{"partial": "req-1"}
		p.Files["req-1"] = []fakeportal.File{{Name: "20261101061500_" + vin + ".zip", CreatedOn: now, Data: synthetic("2026-11-01T06:00:00.000Z", "60")}}
	})
	src := e.source(t, AuthSessionCookie, "PORTALSESSION=valid-session", nil)
	got := e.runOnce(t, src.ID)
	lines := runLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("want one run line, got %d: %s", len(lines), buf.String())
	}
	l := lines[0]
	if l["log.level"] != "info" {
		t.Fatalf("level: %v", l)
	}
	if l["outcome"] != "authorized" || l["deliveries"] != float64(1) || l["wagenpulse.correlation_id"] != got.Status.CorrelationID || got.Status.CorrelationID == "" {
		t.Fatalf("fields: %v (status %+v)", l, got.Status)
	}
	for _, secret := range []string{vin, "valid-session", "PORTALSESSION"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("log contains %q: %s", secret, buf.String())
		}
	}
}

// A sign-in that fails while a data call re-runs it is reported as a sign-in failure, never as the data endpoint
// that happened to trigger it.
func TestReloginFailureNamesLoginEndpoint(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(p *fakeportal.Portal)
		want string
	}{
		"challenge page": {func(p *fakeportal.Portal) { p.Interstitial = true }, "login:authorize"},
		"captcha":        {func(p *fakeportal.Portal) { p.Captcha = true }, "login:"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			buf := e.captureLog()
			e.portal.Set(func(p *fakeportal.Portal) {
				p.Email, p.Password = "owner@example.com", "portal-password"
				p.Requests[vin] = map[string]string{"partial": "req-1"}
			})
			user := "owner@example.com"
			src := e.source(t, AuthPassword, "portal-password", &user)
			if got := e.runOnce(t, src.ID); got.Status.State != "authorized" {
				t.Fatalf("first run: %+v", got.Status)
			}
			e.portal.Set(func(p *fakeportal.Portal) {
				p.SessionCookie, p.Expired = "fresh-session", fakeportal.ExpiredRedirect
				tc.set(p)
			})
			buf.Reset()
			got := e.runOnce(t, src.ID)
			if got.Status.State != "reauthorization_required" {
				t.Fatalf("state: %+v", got.Status)
			}
			lines := runLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("want one run line: %s", buf.String())
			}
			ep, _ := lines[0]["endpoint"].(string)
			if !strings.HasPrefix(ep, tc.want) || strings.Contains(got.Status.Reason, "vehicles") {
				t.Fatalf("endpoint %q, reason %q: want %s*, not vehicles", ep, got.Status.Reason, tc.want)
			}
		})
	}
}
