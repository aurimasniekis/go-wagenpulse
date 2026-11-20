// Package vwdataact is a client for the Volkswagen Group EU Data Act portal's web backend.
//
// UNOFFICIAL INTERFACE (ADR-0005, docs/CAPABILITIES.md): Volkswagen does not document a machine API. The endpoints
// below are the ones the portal's own web application calls, as observed and published by community integrations
// (evcc, HA_VAG-EU-Data-Act, CarConnectivity) and the author's go-cupra project, from which this client is adapted.
// They may change without notice. The client is acquisition-only: it lists and downloads deliveries; parsing and
// storage happen in internal/ingest.
//
// Behaviour toward the portal: an honest product User-Agent, no priming requests, a minimum interval between
// requests, Retry-After honoured on 429 — following the October 2026 requests VW made to community clients (E5).
package vwdataact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default portal and identity hosts (fixed; not configurable through the API to avoid SSRF).
const (
	DefaultBaseURL      = "https://eu-data-act.drivesomethinggreater.com"
	DefaultIdentityBase = "https://identity.vwgroup.io"

	vehiclesPath = "/proxy_api/consent/me/vehicles"
	metadataPath = "/proxy_api/euda-apim/datarequest/vehicles/%s/metadata/%s"
	listPath     = "/proxy_api/euda-apim/datadelivery/vehicles/%s/%s/list"
	downloadPath = "/proxy_api/euda-apim/datadelivery/vehicles/%s/%s/download"

	// NoContentSuffix marks deliveries in which the vehicle sent no data.
	NoContentSuffix = "_no_content_found.zip"
)

// Request types: continuous ("partial") and the one-time historical export ("all").
const (
	RequestContinuous = "partial"
	RequestHistorical = "all"
)

// Brand describes the portal's OIDC client for a brand. Client IDs are public identifiers taken from the portal's
// login selector as documented by community clients (E4); state keys are only listed where evidence exists.
type Brand struct {
	ClientID string
	StateKey string
}

// Brands maps WagenPulse brand identifiers to the portal's identity client.
var Brands = map[string]Brand{
	"volkswagen":            {ClientID: "9b58543e-1c15-4193-91d5-8a14145bebb0@apps_vw-dilab_com", StateKey: "VOLKSWAGEN_PASSENGER_CARS"},
	"volkswagen_commercial": {ClientID: "9b58543e-1c15-4193-91d5-8a14145bebb0@apps_vw-dilab_com"},
	"audi":                  {ClientID: "cc29b87a-5e9a-4362-aecf-5adea6b01bbb@apps_vw-dilab_com"},
	"skoda":                 {ClientID: "3ea88bf9-1d4e-4a68-b3ad-4098c1f1d246@apps_vw-dilab_com"},
	"seat":                  {ClientID: "f85e5b69-e3b2-43aa-9c0d-1b7d0e0b576f@apps_vw-dilab_com"},
	"cupra":                 {ClientID: "f85e5b69-e3b2-43aa-9c0d-1b7d0e0b576f@apps_vw-dilab_com", StateKey: "CUPRA"},
	"bentley":               {ClientID: "d38aac0f-3d89-4a63-8538-b75b31322c7b@apps_vw-dilab_com"},
}

// Error classes reported to source status.
const (
	ClassAuth        = "auth" // session expired / credentials rejected → reauthorization_required
	ClassNoRequest   = "no_active_request"
	ClassRateLimited = "rate_limited"
	ClassUnavailable = "unavailable" // network, 5xx
	ClassChallenge   = "challenge"   // CAPTCHA / bot challenge: never bypassed
	ClassProtocol    = "protocol"    // unexpected response shape (the unofficial interface changed)
	ClassTooLarge    = "too_large"
)

// Error is a classified client error. Messages never contain credentials, cookies or response bodies.
type Error struct {
	Class      string
	Status     int
	RetryAfter time.Duration
	Msg        string
	// Endpoint is the short name of the call that failed (identifier, vehicles, list, download, login:<step>). It is a
	// name, never a URL or query string.
	Endpoint string
}

func (e *Error) Error() string {
	switch {
	case e.Status != 0 && e.Endpoint != "":
		return fmt.Sprintf("%s (HTTP %d from %s): %s", e.Class, e.Status, e.Endpoint, e.Msg)
	case e.Status != 0:
		return fmt.Sprintf("%s (HTTP %d): %s", e.Class, e.Status, e.Msg)
	}
	return e.Class + ": " + e.Msg
}

// Endpoint names used in errors and logs.
const (
	EndpointVehicles   = "vehicles"
	EndpointIdentifier = "identifier"
	EndpointList       = "list"
	EndpointDownload   = "download"
)

// detail renders the status and endpoint for a reason, for example "404 from vehicles".
func detail(status int, endpoint string) string {
	switch {
	case status != 0 && endpoint != "":
		return fmt.Sprintf(" (%d from %s)", status, endpoint)
	case status != 0:
		return fmt.Sprintf(" (%d)", status)
	case endpoint != "":
		return " (" + endpoint + ")"
	}
	return ""
}

// atEndpoint names the failing call on a classified error that does not name one yet.
func atEndpoint(err error, endpoint string) error {
	var e *Error
	if err != nil && errors.As(err, &e) && e.Endpoint == "" {
		e.Endpoint = endpoint
	}
	return err
}

// EndpointOf returns the endpoint name carried by err ("" when none).
func EndpointOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Endpoint
	}
	return ""
}

// StatusOf returns the HTTP status carried by err (0 when none).
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// ClassOf returns the class of err ("" for unclassified errors).
func ClassOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// Config parameterizes the client.
type Config struct {
	BaseURL      string
	IdentityBase string
	Brand        string
	StateKey     string // override when Brands has none for the brand
	Country      string // ISO code, e.g. "de"
	Language     string // e.g. "en"
	UserAgent    string
	HTTPClient   *http.Client // SSRF-guarded client from the app; a cookie jar is attached
	// MinInterval spaces consecutive requests (default 1s).
	MinInterval time.Duration
	// MaxDownload bounds one downloaded file.
	MaxDownload int64
}

// Client talks to the portal with one owner session.
type Client struct {
	cfg   Config
	hc    *http.Client
	login func(ctx context.Context) error
	mu    sync.Mutex
	last  time.Time
	// logins counts login attempts by this client. A client lives for one collection run, and at most one login
	// happens per run: a session that is rejected right after a fresh login is not retried again.
	logins int
}

// New builds a client. login (optional) establishes or refreshes the session in the cookie jar.
func New(cfg Config, login func(ctx context.Context, c *Client) error) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.IdentityBase == "" {
		cfg.IdentityBase = DefaultIdentityBase
	}
	if cfg.MinInterval == 0 {
		cfg.MinInterval = time.Second
	}
	if cfg.MaxDownload <= 0 {
		cfg.MaxDownload = 50 << 20
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "WagenPulse (+https://github.com/aurimasniekis/go-wagenpulse)"
	}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 60 * time.Second}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: base.Transport, Timeout: base.Timeout, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 15 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && !strings.HasPrefix(cfg.BaseURL, "http://") {
			return errors.New("refusing a non-https redirect")
		}
		return nil
	}}
	c := &Client{cfg: cfg, hc: hc}
	if login != nil {
		c.login = func(ctx context.Context) error { return login(ctx, c) }
	}
	return c, nil
}

// SetCookies seeds the session from a Cookie header value ("name=value; name2=value2").
func (c *Client) SetCookies(raw string) error {
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return err
	}
	var cookies []*http.Cookie
	for part := range strings.SplitSeq(raw, ";") {
		part = strings.TrimSpace(part)
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n,") {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: strings.TrimSpace(value)}) //nolint:gosec // a client-side jar entry, not a response cookie
	}
	if len(cookies) == 0 {
		return &Error{Class: ClassAuth, Msg: "no cookies could be parsed from the session value"}
	}
	c.hc.Jar.SetCookies(u, cookies)
	return nil
}

// Cookies returns the portal session cookies (to persist the refreshed session, sealed).
func (c *Client) Cookies() string {
	u, _ := url.Parse(c.cfg.BaseURL)
	var parts []string
	for _, ck := range c.hc.Jar.Cookies(u) {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

// Login runs the configured login function. It runs at most once per client; a second call reports ClassAuth.
func (c *Client) Login(ctx context.Context) error {
	if c.login == nil {
		return &Error{Class: ClassAuth, Msg: "the portal session expired; paste a fresh session cookie from your browser"}
	}
	c.mu.Lock()
	n := c.logins
	c.logins++
	c.mu.Unlock()
	if n > 0 {
		return &Error{Class: ClassAuth, Endpoint: "login:session", Msg: "the portal rejected the session again right after signing in; reauthorize the source"}
	}
	// A sign-in failure is never a failure of the data endpoint that happened to trigger it.
	return atEndpoint(c.login(ctx), "login")
}

// Logins returns how many logins this client attempted (0 = it is still using a stored session).
func (c *Client) Logins() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logins
}

// CanLogin reports whether the client can sign in by itself (password mode).
func (c *Client) CanLogin() bool { return c.login != nil }

// VehicleInfo is a vehicle visible on the portal.
type VehicleInfo struct {
	VIN string `json:"vin"`
}

var vinRe = regexp.MustCompile(`(?i)"(?:vin|vehicleIdentificationNumber)"\s*:\s*"([A-HJ-NPR-Z0-9]{17})"`)

// Vehicles lists the VINs linked to the account (used to verify a session).
func (c *Client) Vehicles(ctx context.Context) ([]VehicleInfo, error) {
	body, _, err := c.get(ctx, EndpointVehicles, c.cfg.BaseURL+vehiclesPath+"?viewPosition=FRONT_LEFT", nil, 4<<20)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []VehicleInfo{}
	for _, m := range vinRe.FindAllSubmatch(body, -1) {
		vin := strings.ToUpper(string(m[1]))
		if !seen[vin] {
			seen[vin] = true
			out = append(out, VehicleInfo{VIN: vin})
		}
	}
	return out, nil
}

var identifierRe = regexp.MustCompile(`(?i)"identifier"\s*:\s*"([^"]{1,200})"`)

// Identifier returns the data-request identifier for a VIN and request type. ClassNoRequest means no request of that
// type is active (it must be created by the owner in a browser).
func (c *Client) Identifier(ctx context.Context, vin, requestType string) (string, error) {
	body, status, err := c.get(ctx, EndpointIdentifier, c.cfg.BaseURL+fmt.Sprintf(metadataPath, url.PathEscape(vin), url.PathEscape(requestType)), nil, 1<<20)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Class == ClassProtocol && (e.Status == http.StatusNotFound || e.Status == http.StatusBadRequest) {
			return "", &Error{Class: ClassNoRequest, Status: e.Status, Endpoint: EndpointIdentifier, Msg: "no active data request of this type on the portal"}
		}
		return "", err
	}
	if status == http.StatusNotFound || strings.Contains(strings.ToLower(string(body)), "no data request") {
		return "", &Error{Class: ClassNoRequest, Msg: "no active data request of this type on the portal"}
	}
	m := identifierRe.FindSubmatch(body)
	if m == nil {
		return "", &Error{Class: ClassNoRequest, Msg: "the portal returned no data-request identifier"}
	}
	return string(m[1]), nil
}

// File is one delivered ZIP on the portal.
type File struct {
	Name      string     `json:"name"`
	CreatedOn *time.Time `json:"created_on,omitempty"`
	Size      int64      `json:"size"`
}

type rawFile struct {
	Name      string          `json:"name"`
	CreatedOn json.RawMessage `json:"createdOn"`
	Size      json.RawMessage `json:"size"`
}

// List returns delivered files, newest first. An empty result means nothing was delivered yet.
func (c *Client) List(ctx context.Context, vin, identifier, requestType string) ([]File, error) {
	body, status, err := c.get(ctx, EndpointList, c.cfg.BaseURL+fmt.Sprintf(listPath, url.PathEscape(vin), url.PathEscape(identifier)),
		map[string]string{"type": requestType}, 4<<20)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	var raws []rawFile
	if err := json.Unmarshal(body, &raws); err != nil {
		var wrapped struct {
			Files []rawFile `json:"files"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			return nil, &Error{Class: ClassProtocol, Endpoint: EndpointList, Msg: "the file list has an unexpected structure"}
		}
		raws = wrapped.Files
	}
	out := make([]File, 0, len(raws))
	for _, r := range raws {
		if r.Name == "" || len(r.Name) > 255 || strings.ContainsAny(r.Name, "/\\") {
			continue
		}
		f := File{Name: r.Name, CreatedOn: parseTime(r.CreatedOn)}
		if n, err := strconv.ParseInt(strings.Trim(string(r.Size), `"`), 10, 64); err == nil {
			f.Size = n
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].CreatedOn, out[j].CreatedOn
		if a != nil && b != nil && !a.Equal(*b) {
			return a.After(*b)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

func parseTime(raw json.RawMessage) *time.Time {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 1e11 && ms < 1e14 {
		t := time.UnixMilli(ms).UTC()
		return &t
	}
	return nil
}

// Download returns one delivered file (bounded by MaxDownload).
func (c *Client) Download(ctx context.Context, vin, identifier, requestType, name string) ([]byte, error) {
	body, _, err := c.get(ctx, EndpointDownload, c.cfg.BaseURL+fmt.Sprintf(downloadPath, url.PathEscape(vin), url.PathEscape(identifier)),
		map[string]string{"filename": name, "type": requestType}, c.cfg.MaxDownload)
	return body, err
}

// get issues a GET to the named endpoint, signing in again once (per client) when the session is rejected or has expired.
//
// An expired portal session does not always answer 401/403: the request can be redirected into the sign-in flow,
// which ends in an HTML page with status 200. Such an answer to a data endpoint is treated as an expired session,
// never as data or as "no active request".
func (c *Client) get(ctx context.Context, endpoint, rawURL string, headers map[string]string, limit int64) ([]byte, int, error) {
	body, status, err := c.getOnce(ctx, endpoint, rawURL, headers, limit)
	return body, status, atEndpoint(err, endpoint)
}

func (c *Client) getOnce(ctx context.Context, endpoint, rawURL string, headers map[string]string, limit int64) ([]byte, int, error) {
	resp, err := c.do(ctx, rawURL, headers, limit)
	if err != nil {
		return nil, 0, err
	}
	if c.sessionRejected(resp) && c.login != nil && c.Logins() == 0 {
		if lerr := c.Login(ctx); lerr != nil {
			return nil, resp.status, lerr
		}
		if resp, err = c.do(ctx, rawURL, headers, limit); err != nil {
			return nil, 0, err
		}
	}
	if c.sessionRejected(resp) {
		return nil, resp.status, &Error{Class: ClassAuth, Status: resp.status, Msg: "the portal rejected the session; reauthorize the source"}
	}
	return resp.body, resp.status, classify(resp.status, endpoint)
}

// response is what do returns: the body and where the request ended after redirects.
type response struct {
	body        []byte
	status      int
	finalURL    *url.URL
	contentType string
}

// sessionRejected reports a 401/403, a redirect into the sign-in flow (onto the identity provider, or onto the portal
// outside its API), or an HTML page where the API answers JSON or a file. A redirect to any other host (for example
// a storage host serving a download) is not a rejection.
func (c *Client) sessionRejected(r response) bool {
	if r.status == http.StatusUnauthorized || r.status == http.StatusForbidden {
		return true
	}
	if r.status >= 300 {
		return false
	}
	if r.finalURL != nil {
		base, _ := url.Parse(c.cfg.BaseURL)
		identity, _ := url.Parse(c.cfg.IdentityBase)
		switch {
		case base != nil && strings.EqualFold(r.finalURL.Host, base.Host):
			if !strings.HasPrefix(r.finalURL.Path, "/proxy_api/") {
				return true
			}
		case identity != nil && strings.EqualFold(r.finalURL.Host, identity.Host):
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.contentType)), "text/html")
}

func classify(status int, endpoint string) error {
	switch {
	case status < 400:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Class: ClassAuth, Status: status, Endpoint: endpoint, Msg: "the portal rejected the session; reauthorize the source"}
	case status == http.StatusTooManyRequests:
		return &Error{Class: ClassRateLimited, Status: status, Endpoint: endpoint, Msg: "the portal is rate limiting requests"}
	case status >= 500:
		return &Error{Class: ClassUnavailable, Status: status, Endpoint: endpoint, Msg: "the portal is unavailable" + detail(status, endpoint)}
	default:
		return &Error{Class: ClassProtocol, Status: status, Endpoint: endpoint, Msg: "the portal returned an unexpected status" + detail(status, endpoint)}
	}
}

func (c *Client) do(ctx context.Context, rawURL string, headers map[string]string, limit int64) (response, error) {
	if err := c.pace(ctx); err != nil {
		return response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		// The parse error quotes the URL, which can carry a VIN: never pass it on.
		return response{}, &Error{Class: ClassProtocol, Msg: "the portal request could not be built"}
	}
	req.Header.Set("Accept", "application/json, application/zip, */*")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, &Error{Class: ClassUnavailable, Msg: "network error reaching the portal"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return response{status: resp.StatusCode}, &Error{Class: ClassRateLimited, Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
			Msg: "the portal is rate limiting requests"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return response{}, &Error{Class: ClassUnavailable, Msg: "reading the portal response failed"}
	}
	if int64(len(body)) > limit {
		return response{status: resp.StatusCode}, &Error{Class: ClassTooLarge, Msg: "the portal response exceeds the size limit"}
	}
	return response{body: body, status: resp.StatusCode, finalURL: resp.Request.URL, contentType: resp.Header.Get("Content-Type")}, nil
}

// pace enforces MinInterval between requests.
func (c *Client) pace(ctx context.Context) error {
	c.mu.Lock()
	wait := c.cfg.MinInterval - time.Since(c.last)
	c.last = time.Now().Add(max(wait, 0))
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(h); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, 24*time.Hour)
	}
	if t, err := http.ParseTime(h); err == nil {
		return min(max(time.Until(t), 0), 24*time.Hour)
	}
	return 0
}
