package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc/fakeidp"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring/keyringtest"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/storetest"
)

const publicURL = "http://wagen.test"

type env struct {
	svc  *Service
	au   *auth.Service
	idp  *fakeidp.IdP
	root *auth.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(nil)
	t.Cleanup(srv.Close)
	idp, err := fakeidp.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = idp.Handler()
	idp.AddClient(fakeidp.Client{ID: "wagenpulse", Secret: "client-secret", RedirectURIs: []string{publicURL + CallbackPath}})
	idp.AddUser(fakeidp.User{Subject: "sub-alice", Email: "alice@example.com", EmailVerified: true, Name: "Alice"})
	idp.AddUser(fakeidp.User{Subject: "sub-mallory", Email: "mallory@evil.example", EmailVerified: true, Name: "Mallory"})
	idp.AddUser(fakeidp.User{Subject: "sub-unverified", Email: "bob@example.com", EmailVerified: false, Name: "Bob"})

	s := storetest.Open(t)
	kr := keyringtest.Load(t, s)
	aw := audit.NewWriter(kr)
	st := settings.New(s, aw, 30*24*time.Hour, 365*24*time.Hour)
	au := auth.New(s, kr, aw, st, auth.Options{Params: auth.TestParams})
	refs := &secretref.Resolver{EnvPrefix: "WAGENPULSE_SECRET_"}
	svc := New(s, kr, aw, au, NewUpstream(srv.Client()), refs, publicURL, nil, nil)
	svc.AllowHTTPIssuers = true
	tok, _, err := au.IssueSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := au.CompleteSetup(ctx, auth.SetupInput{Token: tok, Username: "root", Password: "a-long-root-password"}, auth.SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := au.Authenticate(ctx, res.Session.Token)
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, au: au, idp: idp, root: root}
}

func (e *env) provider(t *testing.T, in ProviderInput) Provider {
	t.Helper()
	secret := "client-secret"
	in.Issuer, in.ClientID, in.ClientSecret, in.Enabled = e.idp.Issuer(), "wagenpulse", &secret, true
	if in.Slug == "" {
		in.Slug = "test"
	}
	in.DisplayName = "Test IdP"
	p, err := e.svc.Create(context.Background(), e.root, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// roundTrip runs the browser part: follow the authorize URL as subject and return the callback query.
func (e *env) roundTrip(t *testing.T, authURL, subject string) url.Values {
	t.Helper()
	e.idp.Set(func(i *fakeidp.IdP) { i.Current = subject })
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Path != CallbackPath {
		t.Fatalf("unexpected redirect %q (%d)", resp.Header.Get("Location"), resp.StatusCode)
	}
	return loc.Query()
}

func (e *env) login(t *testing.T, slug, subject string) (*CallbackResult, error) {
	t.Helper()
	ctx := context.Background()
	st, err := e.svc.Start(ctx, slug, IntentLogin, nil, "/vehicles")
	if err != nil {
		t.Fatal(err)
	}
	q := e.roundTrip(t, st.AuthURL, subject)
	return e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), q.Get("error"), auth.SessionMeta{IP: "192.0.2.1"})
}

func TestNotLinkedThenLinkThenLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.provider(t, ProviderInput{})
	if p.ClientSecretSet != true || p.SecretFingerprint == "" || p.RedirectURI != publicURL+CallbackPath {
		t.Fatalf("provider view %+v", p)
	}
	if _, err := e.login(t, "test", "sub-alice"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlinked login: %v", err)
	}
	st, err := e.svc.Start(ctx, "test", IntentLink, e.root, "/account")
	if err != nil {
		t.Fatal(err)
	}
	q := e.roundTrip(t, st.AuthURL, "sub-alice")
	if _, err := e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), "", auth.SessionMeta{}); err != nil {
		t.Fatalf("link: %v", err)
	}
	res, err := e.login(t, "test", "sub-alice")
	if err != nil || res.Session == nil || res.ReturnTo != "/vehicles" {
		t.Fatalf("login after link: %+v %v", res, err)
	}
	pr, err := e.au.Authenticate(ctx, res.Session.Token)
	if err != nil || pr.Role != auth.RoleRoot || pr.Method != auth.MethodOIDC {
		t.Fatalf("principal %+v %v", pr, err)
	}
	// Linking the same identity to another account is refused.
	viewer := auth.RoleViewer
	u, _ := e.au.CreateUser(ctx, e.root, auth.UserInput{Username: "vic", Role: &viewer, Password: "a-long-enough-password"})
	lr, _ := e.au.Login(ctx, "vic", "a-long-enough-password", auth.SessionMeta{})
	vp, _ := e.au.Authenticate(ctx, lr.Session.Token)
	st, _ = e.svc.Start(ctx, "test", IntentLink, vp, "/")
	q = e.roundTrip(t, st.AuthURL, "sub-alice")
	if _, err := e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), "", auth.SessionMeta{}); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("double link: %v (user %s)", err, u.ID)
	}
}

func TestStateBindingAndReplay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.provider(t, ProviderInput{})
	st, err := e.svc.Start(ctx, "test", IntentLogin, nil, "/")
	if err != nil {
		t.Fatal(err)
	}
	q := e.roundTrip(t, st.AuthURL, "sub-alice")
	if _, err := e.svc.Callback(ctx, q.Get("state"), "other-browser", q.Get("code"), "", auth.SessionMeta{}); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("cookie mismatch: %v", err)
	}
	// The state was not consumed by the mismatched attempt? It must be single use either way: first real use fails
	// with not linked, second use is invalid.
	if _, err := e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), "", auth.SessionMeta{}); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("first use: %v", err)
	}
	if _, err := e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), "", auth.SessionMeta{}); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("replay: %v", err)
	}
}

func TestJITProvisioning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Create(ctx, e.root, ProviderInput{Slug: "x", DisplayName: "X", Issuer: e.idp.Issuer(), ClientID: "c",
		Provisioning: "jit"}); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("jit without domains: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.root, ProviderInput{Slug: "y", DisplayName: "Y", Issuer: e.idp.Issuer(), ClientID: "c",
		Provisioning: "jit", AllowedDomains: []string{"example.com"}, DefaultRole: "admin"}); apperr.CodeOf(err) != apperr.CodeInvalid {
		t.Fatalf("jit admin: %v", err)
	}
	e.provider(t, ProviderInput{Provisioning: "jit", AllowedDomains: []string{"example.com"}, DefaultRole: "operator", RequireVerifiedEmail: true})
	if _, err := e.login(t, "test", "sub-mallory"); !errors.Is(err, ErrDomainDenied) {
		t.Fatalf("foreign domain: %v", err)
	}
	if _, err := e.login(t, "test", "sub-unverified"); !errors.Is(err, ErrEmailUnverify) {
		t.Fatalf("unverified email: %v", err)
	}
	res, err := e.login(t, "test", "sub-alice")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.au.Authenticate(ctx, res.Session.Token)
	if p.Role != auth.RoleOperator || p.Username != "alice@example.com" {
		t.Fatalf("jit principal %+v", p)
	}
}

func TestDisableProviderProtectsRoot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.provider(t, ProviderInput{})
	st, _ := e.svc.Start(ctx, "test", IntentLink, e.root, "/")
	q := e.roundTrip(t, st.AuthURL, "sub-alice")
	if _, err := e.svc.Callback(ctx, q.Get("state"), st.State, q.Get("code"), "", auth.SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	// Root now has password + SSO. Disable local login: allowed (SSO remains).
	sys := settings.DefaultSystem()
	sys.LocalLoginEnabled = false
	if _, err := e.au.Settings().UpdateSystem(ctx, e.root.AuditActor(), 0, sys); err != nil {
		t.Fatalf("disable local with SSO linked: %v", err)
	}
	// Disabling or deleting the provider would now lock root out.
	in := ProviderInput{DisplayName: p.DisplayName, Issuer: p.Issuer, ClientID: p.ClientID, Enabled: false}
	if _, err := e.svc.Update(ctx, e.root, p.ID, 0, in); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("disable provider: %v", err)
	}
	if err := e.svc.Delete(ctx, e.root, p.ID); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("delete provider: %v", err)
	}
	ids, _ := e.svc.Identities(ctx, e.root.UserID)
	if err := e.svc.Unlink(ctx, e.root, e.root.UserID, ids[0].ID); apperr.CodeOf(err) != apperr.CodeConflict {
		t.Fatalf("unlink last method: %v", err)
	}
}
