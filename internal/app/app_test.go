package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/api"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/config"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
)

// testServer runs a development-mode instance on httptest and returns it with its app.
func testServer(t *testing.T) (*httptest.Server, *App) {
	t.Helper()
	cfg := config.Default()
	cfg.Mode = config.ModeDevelopment
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	srv := httptest.NewUnstartedServer(nil)
	cfg.PublicURL = "http://" + srv.Listener.Addr().String()
	cfg.Outbound.AllowCIDRs = []string{"127.0.0.1/32"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := auth.TestParams
	a, err := Open(context.Background(), cfg, obs.Discard(), Options{HashParams: &p, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	srv.Config.Handler = a.PublicHandler()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, a
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
	auth string
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, hc: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", c.base)
	if c.csrf != "" {
		req.Header.Set(api.CSRFHeader, c.csrf)
	}
	if c.auth != "" {
		req.Header.Set("Authorization", "Bearer "+c.auth)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

func bootstrap(t *testing.T, srv *httptest.Server, a *App) *client {
	t.Helper()
	tok, _, err := a.Auth.IssueSetupToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, srv.URL)
	var me api.Me
	if code := c.do("POST", "/api/v1/setup", map[string]any{"token": tok, "username": "root", "password": "a-very-long-password"}, &me); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	if me.User.Role != auth.RoleRoot || me.CSRFToken == "" {
		t.Fatalf("me %+v", me)
	}
	c.csrf = me.CSRFToken
	return c
}

func TestSetupLoginCSRFAndTokens(t *testing.T) {
	srv, a := testServer(t)
	c := bootstrap(t, srv, a)
	// CSRF is required for cookie mutations.
	noCSRF := *c
	noCSRF.csrf = ""
	if code := noCSRF.do("POST", "/api/v1/users", map[string]any{"username": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF: %d", code)
	}
	var u auth.User
	if code := c.do("POST", "/api/v1/users", map[string]any{"username": "olga", "role": "operator", "password": "another-long-password"}, &u); code != 201 {
		t.Fatalf("create user: %d", code)
	}
	// Mint a token for automation (root: non-expiring requires acknowledgement).
	var created auth.CreatedToken
	body := map[string]any{"name": "terraform", "scopes": []string{"users.read", "vehicles.read", "settings.read"}, "never_expires": true}
	if code := c.do("POST", "/api/v1/tokens", body, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("never_expires without acknowledgement: %d", code)
	}
	body["acknowledge_never_expires"] = true
	if code := c.do("POST", "/api/v1/tokens", body, &created); code != 201 || !strings.HasPrefix(created.Secret, auth.TokenPrefix) {
		t.Fatalf("create token: %d %+v", code, created)
	}
	tc := newClient(t, srv.URL)
	tc.auth = created.Secret
	var users api.Page[auth.User]
	if code := tc.do("GET", "/api/v1/users", nil, &users); code != 200 || len(users.Items) != 2 {
		t.Fatalf("token list users: %d %+v", code, users)
	}
	// Scope limits apply.
	if code := tc.do("POST", "/api/v1/users", map[string]any{"username": "z"}, nil); code != http.StatusForbidden {
		t.Fatalf("token without users.write: %d", code)
	}
	// Tokens are refused for interactive-only operations.
	if code := tc.do("PUT", "/api/v1/me/password", map[string]any{"new_password": "zzzzzzzzzzzzzzzz"}, nil); code != http.StatusForbidden {
		t.Fatalf("token on interactive op: %d", code)
	}
	// Credentials in URLs are refused outright.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/users?token="+created.Secret, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("query token: %d", resp.StatusCode)
	}
	// A bogus bearer with a valid session cookie fails: tokens and sessions never merge.
	mixed := *c
	mixed.auth = "wgp_at_aaaaaaaaaaaa_" + strings.Repeat("a", 43)
	if code := mixed.do("GET", "/api/v1/users", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("bad bearer + cookie: %d", code)
	}
	// Revocation is immediate.
	if code := c.do("DELETE", "/api/v1/tokens/"+created.ID, nil, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code := tc.do("GET", "/api/v1/users", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
	// Logout ends the session.
	if code := c.do("POST", "/api/v1/auth/logout", nil, nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code := c.do("GET", "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
}

func TestLoginAndOriginCheck(t *testing.T) {
	srv, a := testServer(t)
	bootstrap(t, srv, a)
	c := newClient(t, srv.URL)
	var lr api.LoginResponse
	if code := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "root", "password": "wrong-password"}, nil); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	if code := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "root", "password": "a-very-long-password"}, &lr); code != 200 || lr.Me == nil {
		t.Fatalf("login: %d", code)
	}
	// Cross-origin login POSTs are refused (login CSRF).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"root","password":"a-very-long-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d", resp.StatusCode)
	}
}

func TestEveryOperationDeclaresPermission(t *testing.T) {
	mux := http.NewServeMux()
	a := &App{Domain: &Domain{}}
	h := api.New(mux, &api.Deps{Extra: a.domainRegistrations()})
	n := 0
	for path, item := range h.OpenAPI().Paths {
		for _, o := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if o == nil {
				continue
			}
			n++
			perm, _ := o.Metadata["x-permission"].(string)
			switch {
			case perm == "":
				t.Errorf("%s %s (%s) declares no permission", o.Method, path, o.OperationID)
			case perm == "public":
				if len(o.Security) != 0 {
					t.Errorf("%s is public but has security", o.OperationID)
				}
			case !auth.ValidPermission(auth.Permission(perm)):
				t.Errorf("%s declares unknown permission %q", o.OperationID, perm)
			}
		}
	}
	if n < 30 {
		t.Fatalf("only %d operations found", n)
	}
}

// raw sends a request with the client's credentials and extra headers, returning the response (body read).
func (c *client) raw(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r) //nolint:noctx // test
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", c.base)
	if c.csrf != "" {
		req.Header.Set(api.CSRFHeader, c.csrf)
	}
	if c.auth != "" {
		req.Header.Set("Authorization", "Bearer "+c.auth)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// Regressions found by the Terraform provider's acceptance tests.
func TestAPIContractRegressions(t *testing.T) {
	srv, a := testServer(t)
	c := bootstrap(t, srv, a)

	// 1. Unrestricted tokens report vehicle_ids as [] (the schema says array), on create and on read.
	resp, body := c.raw(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "t", "scopes": []string{"users.read"}}, nil)
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	if resp.StatusCode != http.StatusCreated || created["vehicle_ids"] == nil {
		t.Fatalf("create token: %d vehicle_ids=%v", resp.StatusCode, created["vehicle_ids"])
	}
	_, body = c.raw(http.MethodGet, "/api/v1/tokens/"+created["id"].(string), nil, nil)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if ids, ok := got["vehicle_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("read token vehicle_ids=%v", got["vehicle_ids"])
	}

	// 2. Settings: defaults are revision 1; the first stored write becomes 2, so a stale If-Match "1" is refused.
	resp, body = c.raw(http.MethodGet, "/api/v1/settings/retention", nil, nil)
	if resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("defaults etag %q", resp.Header.Get("ETag"))
	}
	var ret map[string]any
	_ = json.Unmarshal(body, &ret)
	ret["events_days"] = float64(200)
	resp, _ = c.raw(http.MethodPut, "/api/v1/settings/retention", ret, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("first write: %d etag %q", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp, _ = c.raw(http.MethodPut, "/api/v1/settings/retention", ret, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("concurrent first write with a stale ETag: %d", resp.StatusCode)
	}

	// 3. Rate-limited token requests say when to retry.
	tc := newClient(t, srv.URL)
	tc.auth = created["token"].(string)
	limited := false
	for range 200 {
		r, _ := tc.raw(http.MethodGet, "/api/v1/users", nil, nil)
		if r.StatusCode == http.StatusTooManyRequests {
			if r.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("token was never rate limited")
	}
}
