// Package api serves the versioned WagenPulse API under /api/v1 with Huma v2 (ADR-0018). The OpenAPI 3.1 document is
// generated from this code (`wagenpulse openapi`), committed as api/openapi.json and drift-checked in CI. The web UI,
// automation and the Terraform provider all use this API; there is no other configuration path.
//
// Every operation declares the permission it requires in its metadata; the authentication middleware enforces it
// before the handler runs, and a test asserts that no operation is missing one (ADR-0010).
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc"
	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/httpx"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Prefix is the API mount point.
const Prefix = "/api/v1"

// APIVersion is reported by /system/info and checked by the Terraform provider.
const APIVersion = "v1"

// CSRFHeader carries the session's CSRF token on state-changing cookie-authenticated requests.
const CSRFHeader = "X-CSRF-Token"

// Deps are the services the API needs. Domain services are added by later milestones; nil services make their
// operations unavailable rather than panicking.
type Deps struct {
	Log           *slog.Logger
	Version       string
	Origin        string // public origin, for Origin checks
	SecureCookies bool
	Store         *store.Store
	Keyring       *keyring.Keyring
	Audit         *audit.Writer
	Auth          *auth.Service
	OIDC          *oidc.Service
	Settings      *settings.Service
	Metrics       *metrics.Metrics
	Clock         clock.Clock
	Ready         func(context.Context) error
	StartedAt     time.Time
	LoginRate     *httpx.Limiter
	TokenRate     *httpx.Limiter
	Extra         []func(huma.API, *Deps) // additional registrations
	D             *Domain                 // domain services (nil when only generating the OpenAPI document)
	// InstallationID identifies this installation (event sources, diagnostics).
	InstallationID string
}

func (d *Deps) now() time.Time {
	if d.Clock == nil {
		return time.Now()
	}
	return d.Clock.Now()
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return obs.Discard()
	}
	return d.Log
}

// Security scheme names.
const (
	schemeSession = "session"
	schemeToken   = "apiToken"
)

// Operation metadata keys.
const (
	metaPermission  = "x-permission"
	metaInteractive = "x-interactive-only"
	metaTOTPExempt  = "x-totp-enrollment-allowed"
)

// Config returns the Huma configuration (shared with the openapi command).
func Config(version string) huma.Config {
	c := huma.DefaultConfig("WagenPulse API", version)
	c.Servers = []*huma.Server{{URL: Prefix}}
	c.DocsPath = ""
	c.OpenAPIPath = "/openapi"
	c.SchemasPath = ""
	c.CreateHooks = nil
	c.Info.Description = "Versioned API of WagenPulse, shared by the web UI, automation and the Terraform provider. " +
		"Browser clients authenticate with the session cookie and send the session's CSRF token in X-CSRF-Token on " +
		"state-changing requests. Automation uses `Authorization: Bearer wgp_at_…` API tokens (never in URLs). When both " +
		"a bearer token and a session cookie are present, only the token is used. Errors are RFC 9457 problem documents " +
		"with a stable `code`. Mutable resources carry an ETag; send it in If-Match to avoid lost updates (412 on " +
		"mismatch). POST requests accept Idempotency-Key for safe retries."
	c.Info.License = &huma.License{Name: "Apache-2.0", URL: "https://www.apache.org/licenses/LICENSE-2.0"}
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		schemeSession: {Type: "apiKey", In: "cookie", Name: auth.CookieName},
		schemeToken:   {Type: "http", Scheme: "bearer", Description: "API token (wgp_at_…); see ADR-0014."},
	}
	return c
}

// New registers every operation on mux and returns the API.
func New(mux *http.ServeMux, d *Deps) huma.API {
	inner := http.NewServeMux()
	mux.Handle(Prefix+"/", d.idempotency(d.rejectQueryCredentials(inner)))
	api := humago.NewWithPrefix(inner, Prefix, Config(d.Version))
	api.UseMiddleware(d.authenticate(api))
	registerSystem(api, d)
	registerAuth(api, d)
	registerUsers(api, d)
	registerTokens(api, d)
	registerSettings(api, d)
	registerOIDCProviders(api, d)
	registerAudit(api, d)
	registerVehicles(api, d)
	registerSignals(api, d)
	registerSources(api, d)
	registerImports(api, d)
	registerRules(api, d)
	registerAdapters(api, d)
	registerRoutes(api, d)
	registerEvents(api, d)
	registerOverview(api, d)
	for _, fn := range d.Extra {
		fn(api, d)
	}
	return api
}

// OpenAPI returns the generated document without serving anything (the openapi command).
func OpenAPI(version string, extra ...func(huma.API, *Deps)) ([]byte, error) {
	mux := http.NewServeMux()
	api := New(mux, &Deps{Version: version, Extra: extra})
	return api.OpenAPI().MarshalJSON()
}

var authSecurity = []map[string][]string{{schemeSession: {}}, {schemeToken: {}}}

// op builds an operation requiring perm. Use public() for unauthenticated operations.
func op(id, method, path string, perm auth.Permission, summary string, tags ...string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags, Security: authSecurity,
		Metadata:    map[string]any{metaPermission: string(perm)},
		Description: fmt.Sprintf("Requires permission `%s`.", perm)}
}

// public builds an unauthenticated operation.
func public(id, method, path, summary string, tags ...string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags,
		Metadata: map[string]any{metaPermission: "public"}}
}

// interactive marks an operation as unavailable to API tokens (credentials, TOTP, sessions).
func interactive(o huma.Operation) huma.Operation {
	o.Metadata[metaInteractive] = true
	o.Description += " Interactive sessions only (API tokens are refused)."
	return o
}

// totpExempt allows the operation while the user must still enroll TOTP.
func totpExempt(o huma.Operation) huma.Operation {
	o.Metadata[metaTOTPExempt] = true
	return o
}

// created sets 201 as the default status.
func created(o huma.Operation) huma.Operation {
	o.DefaultStatus = http.StatusCreated
	return o
}

// noContent sets 204 as the default status.
func noContent(o huma.Operation) huma.Operation {
	o.DefaultStatus = http.StatusNoContent
	return o
}

type principalKey struct{}

// Principal returns the authenticated principal (nil for public operations).
func Principal(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalKey{}).(*auth.Principal)
	return p
}

// WithPrincipal attaches a principal (tests, plain handlers).
func WithPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// bearerToken returns a bearer credential from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// rejectQueryCredentials refuses credentials in URLs (they leak into logs, history and referrers).
func (d *Deps) rejectQueryCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, k := range []string{"token", "access_token", "api_token", "apikey", "api_key"} {
			if q.Has(k) {
				d.writeIdempotencyProblem(w, http.StatusBadRequest, string(apperr.CodeInvalid),
					"credentials are never accepted in URLs; send Authorization: Bearer <token>")
				return
			}
		}
		if strings.Contains(r.URL.RawQuery, auth.TokenPrefix) {
			d.writeIdempotencyProblem(w, http.StatusBadRequest, string(apperr.CodeInvalid),
				"credentials are never accepted in URLs; send Authorization: Bearer <token>")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the credential for operations that declare security, enforces Origin + CSRF on unsafe
// cookie-authenticated methods, checks the operation's permission and attaches audit metadata.
func (d *Deps) authenticate(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		r, _ := humago.Unwrap(ctx)
		meta := audit.Meta{SourceIP: httpx.ClientIP(r.Context()).String(), RequestID: httpx.GetRequestID(r.Context())}
		o := ctx.Operation()
		if o == nil || len(o.Security) == 0 {
			next(huma.WithContext(ctx, audit.WithMeta(ctx.Context(), meta)))
			return
		}
		perm, _ := o.Metadata[metaPermission].(string)
		var p *auth.Principal
		if bearer, ok := bearerToken(r); ok {
			// Bearer requests are authenticated by the token alone; any session cookie is ignored (ADR-0014).
			if d.TokenRate != nil && !d.TokenRate.Allow(tokenRateKey(bearer)) {
				_ = writeProblem(api, ctx, problem(http.StatusTooManyRequests, apperr.CodeRateLimited, "too many requests for this token", nil))
				return
			}
			tp, err := d.Auth.AuthenticateToken(r.Context(), bearer, meta.SourceIP)
			if err != nil {
				_ = writeProblem(api, ctx, problem(http.StatusUnauthorized, apperr.CodeUnauthorized, "invalid, expired or revoked API token", nil))
				return
			}
			if o.Metadata[metaInteractive] == true {
				_ = writeProblem(api, ctx, problem(http.StatusForbidden, apperr.CodeForbidden, "this operation needs an interactive sign-in", nil))
				return
			}
			meta.AuthMethod = auth.MethodToken
			p = tp
		} else {
			if !httpx.IsSafeMethod(r.Method) && !httpx.SameOrigin(r, d.Origin) {
				_ = writeProblem(api, ctx, problem(http.StatusForbidden, apperr.CodeForbidden, "cross-origin request refused", nil))
				return
			}
			sp, err := d.Auth.Authenticate(r.Context(), auth.SessionFromRequest(r))
			if err != nil {
				_ = writeProblem(api, ctx, problem(http.StatusUnauthorized, apperr.CodeUnauthorized, "authentication required", nil))
				return
			}
			if !httpx.IsSafeMethod(r.Method) && !d.Auth.VerifyCSRF(sp, r.Header.Get(CSRFHeader)) {
				_ = writeProblem(api, ctx, problem(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token", nil))
				return
			}
			if sp.MustEnrollTOTP && o.Metadata[metaTOTPExempt] != true {
				_ = writeProblem(api, ctx, problem(http.StatusForbidden, "totp_enrollment_required",
					"set up two-factor authentication (TOTP) before continuing", nil))
				return
			}
			meta.AuthMethod = sp.Method
			p = sp
		}
		if perm != "" && perm != "public" && !p.Can(auth.Permission(perm)) {
			_ = writeProblem(api, ctx, problem(http.StatusForbidden, apperr.CodeForbidden, "missing permission "+perm, nil))
			return
		}
		c := obs.WithID(audit.WithMeta(ctx.Context(), meta), obs.FieldUserID, p.UserID)
		if p.Token != nil {
			c = obs.WithID(c, obs.FieldTokenID, p.Token.ID)
		}
		next(huma.WithContext(ctx, context.WithValue(c, principalKey{}, p)))
	}
}

// tokenRateKey derives the limiter key from the non-secret lookup part only.
func tokenRateKey(raw string) string {
	if lookup, _, ok := auth.ParseToken(raw); ok {
		return "tok:" + lookup
	}
	return "tok:invalid"
}

// IfMatch is embedded by inputs of conditional mutations.
type IfMatch struct {
	IfMatch string `header:"If-Match" required:"false" doc:"ETag (revision) the change is based on; mismatches fail with 412."`
}

// Revision parses the If-Match header ("" or "*" → 0 = unconditional).
func (m IfMatch) Revision() (int64, error) {
	s := strings.TrimSpace(m.IfMatch)
	if s == "" || s == "*" {
		return 0, nil
	}
	s = strings.TrimPrefix(s, "W/")
	s = strings.Trim(s, `"`)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, problem(http.StatusBadRequest, apperr.CodeInvalid, "If-Match must be an ETag returned by this API", nil)
	}
	return v, nil
}

func etag(revision int64) string { return fmt.Sprintf(`"%d"`, revision) }

// Pagination is embedded by list inputs.
type Pagination struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Page size."`
	Cursor string `query:"cursor" maxLength:"256" doc:"Opaque cursor from next_cursor."`
}

// After decodes the cursor.
func (p Pagination) After() (string, error) {
	if p.Cursor == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(p.Cursor)
	if err != nil || len(b) == 0 || len(b) > 128 {
		return "", problem(http.StatusBadRequest, apperr.CodeInvalid, "invalid cursor", nil)
	}
	return string(b), nil
}

// Lim returns the effective limit.
func (p Pagination) Lim() int {
	if p.Limit <= 0 {
		return 50
	}
	return min(p.Limit, 200)
}

// nextCursor returns the cursor after lastKey when the page was full.
func nextCursor(n, limit int, lastKey string) string {
	if n < limit || lastKey == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(lastKey))
}

// Page is a list response.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty" doc:"Pass as cursor to fetch the next page; absent on the last page."`
}

// fail converts a domain error into an RFC 9457 problem, logging unexpected errors without leaking them.
func (d *Deps) fail(ctx context.Context, opName string, err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return problem(statusFor(ae.Code), ae.Code, ae.Message, ae.Fields)
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return huma.NewError(499, "client closed request")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return problem(http.StatusServiceUnavailable, apperr.CodeUnavailable, "the request timed out", nil)
	}
	d.log().ErrorContext(ctx, "api error", "op", opName, obs.Err(err))
	return problem(http.StatusInternalServerError, "internal", "internal error (see server logs for the request ID)", nil)
}

func statusFor(c apperr.Code) int {
	switch c {
	case apperr.CodeInvalid:
		return http.StatusUnprocessableEntity
	case apperr.CodeNotFound:
		return http.StatusNotFound
	case apperr.CodeAlreadyExists, apperr.CodeConflict:
		return http.StatusConflict
	case apperr.CodeForbidden, apperr.CodeReauth, apperr.CodeMFARequired:
		return http.StatusForbidden
	case apperr.CodePrecondition:
		return http.StatusPreconditionFailed
	case apperr.CodeUnavailable:
		return http.StatusServiceUnavailable
	case apperr.CodeRateLimited:
		return http.StatusTooManyRequests
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}

// sessionMeta builds session metadata from the request in ctx.
func sessionMeta(ctx context.Context, userAgent string) auth.SessionMeta {
	return auth.SessionMeta{IP: audit.MetaFrom(ctx).SourceIP, UserAgent: userAgent}
}

// AuthenticateHTTP authenticates a plain (non-Huma) handler request with the same rules as API operations: bearer
// token (never merged with a session) or session + Origin + CSRF for unsafe methods, then the permission check.
// It returns a problem document (with status) on failure.
func (d *Deps) AuthenticateHTTP(r *http.Request, perm auth.Permission) (*auth.Principal, *Problem) {
	ip := httpx.ClientIP(r.Context()).String()
	var p *auth.Principal
	if bearer, ok := bearerToken(r); ok {
		if d.TokenRate != nil && !d.TokenRate.Allow(tokenRateKey(bearer)) {
			return nil, problem(http.StatusTooManyRequests, apperr.CodeRateLimited, "too many requests for this token", nil)
		}
		tp, err := d.Auth.AuthenticateToken(r.Context(), bearer, ip)
		if err != nil {
			return nil, problem(http.StatusUnauthorized, apperr.CodeUnauthorized, "invalid, expired or revoked API token", nil)
		}
		p = tp
	} else {
		if !httpx.IsSafeMethod(r.Method) && !httpx.SameOrigin(r, d.Origin) {
			return nil, problem(http.StatusForbidden, apperr.CodeForbidden, "cross-origin request refused", nil)
		}
		sp, err := d.Auth.Authenticate(r.Context(), auth.SessionFromRequest(r))
		if err != nil {
			return nil, problem(http.StatusUnauthorized, apperr.CodeUnauthorized, "authentication required", nil)
		}
		if !httpx.IsSafeMethod(r.Method) && !d.Auth.VerifyCSRF(sp, r.Header.Get(CSRFHeader)) {
			return nil, problem(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token", nil)
		}
		if sp.MustEnrollTOTP {
			return nil, problem(http.StatusForbidden, "totp_enrollment_required", "set up two-factor authentication (TOTP) before continuing", nil)
		}
		p = sp
	}
	if !p.Can(perm) {
		return nil, problem(http.StatusForbidden, apperr.CodeForbidden, "missing permission "+string(perm), nil)
	}
	return p, nil
}

// WriteProblem writes a problem document from a plain handler.
func WriteProblem(w http.ResponseWriter, p *Problem) {
	for k, vs := range p.GetHeaders() {
		w.Header()[k] = vs
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// FailHTTP converts an error for plain handlers (unexpected errors are logged, not leaked).
func (d *Deps) FailHTTP(w http.ResponseWriter, r *http.Request, opName string, err error) {
	var p *Problem
	if errors.As(d.fail(r.Context(), opName, err), &p) {
		WriteProblem(w, p)
		return
	}
	WriteProblem(w, problem(http.StatusInternalServerError, "internal", "internal error", nil))
}

// ClientIPString returns the proxy-aware client IP of a plain handler request.
func (d *Deps) ClientIPString(r *http.Request) string { return httpx.ClientIP(r.Context()).String() }

// RequestID returns the request ID of a plain handler request.
func (d *Deps) RequestID(r *http.Request) string { return httpx.GetRequestID(r.Context()) }

// Fail converts a domain error into a problem (for operations registered outside this package).
func (d *Deps) Fail(ctx context.Context, opName string, err error) error {
	return d.fail(ctx, opName, err)
}
