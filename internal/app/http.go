// HTTP wiring adapted from Porch internal/app/http.go (Apache-2.0).

package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aurimasniekis/go-wagenpulse/internal/api"
	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
	"github.com/aurimasniekis/go-wagenpulse/internal/httpx"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/webui"
)

// surface classifies a path for metrics (bounded label values).
func surface(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case strings.HasPrefix(path, "/auth/"):
		return "auth"
	default:
		return "ui"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (a *App) accessLog(next http.Handler) http.Handler {
	log := a.component("http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		ctx := obs.WithID(r.Context(), obs.FieldHTTPRequestID, httpx.GetRequestID(r.Context()))
		next.ServeHTTP(sw, r.WithContext(ctx))
		d := time.Since(start)
		a.Metrics.HTTPRequest(surface(r.URL.Path), r.Method, sw.status, d)
		lvl := slog.LevelDebug
		if sw.status >= 500 {
			lvl = slog.LevelWarn
		}
		// Only the path is logged: query strings may carry filter values, never credentials (those are refused).
		log.Log(ctx, lvl, "http request", "http.request.method", r.Method, "url.path", r.URL.Path, "http.response.status_code", sw.status,
			"event.duration", d.Nanoseconds(), "source.ip", httpx.ClientIP(r.Context()).String())
	})
}

// APIDeps returns the API dependencies.
func (a *App) APIDeps() *api.Deps {
	return &api.Deps{Log: a.Log, Version: buildinfo.Get().Version, Origin: a.Cfg.PublicOrigin(), SecureCookies: a.Cfg.SecureCookies(),
		Store: a.Store, Keyring: a.Keyring, Audit: a.Audit, Auth: a.Auth, OIDC: a.OIDC, Settings: a.Settings, Metrics: a.Metrics,
		Clock: a.Clock, Ready: a.Ready, StartedAt: a.StartedAt, LoginRate: httpx.NewLimiter(a.Clock, 20, 10),
		TokenRate: httpx.NewLimiter(a.Clock, 600, 120), InstallationID: a.InstallationID, Extra: a.domainRegistrations(), D: a.Domain}
}

// PublicHandler is the handler for the public listener.
func (a *App) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	deps := a.APIDeps()
	api.New(mux, deps)
	a.registerUploads(mux, deps)
	mux.HandleFunc("GET /auth/oidc/{slug}/start", a.oidcStart)
	mux.HandleFunc("GET "+oidc.CallbackPath, a.oidcCallback)
	mux.Handle("/", webui.Handler(webui.Dist(), nil))
	var h http.Handler = mux
	h = a.limitBody(h)
	h = httpx.SecurityHeaders(a.Cfg.SecureCookies(), h)
	h = a.accessLog(h)
	h = a.ClientIP.Middleware(h)
	return httpx.RequestID(h)
}

// limitBody caps request bodies; upload endpoints get the ingest limit.
func (a *App) limitBody(next http.Handler) http.Handler {
	small := httpx.LimitBody(a.Cfg.HTTP.MaxBodyBytes, next)
	large := httpx.LimitBody(a.Cfg.Ingest.MaxUploadBytes+(1<<20), next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, api.Prefix+"/uploads") {
			large.ServeHTTP(w, r)
			return
		}
		small.ServeHTTP(w, r)
	})
}

// oidcStart begins an SSO login or link: GET /auth/oidc/{slug}/start?intent=login|link&return_to=/path.
func (a *App) oidcStart(w http.ResponseWriter, r *http.Request) {
	ctx := audit.WithMeta(r.Context(), audit.Meta{SourceIP: httpx.ClientIP(r.Context()).String(), RequestID: httpx.GetRequestID(r.Context())})
	intent := r.URL.Query().Get("intent")
	if intent != oidc.IntentLink {
		intent = oidc.IntentLogin
	}
	var p *auth.Principal
	if intent == oidc.IntentLink {
		var err error
		if p, err = a.Auth.Authenticate(ctx, auth.SessionFromRequest(r)); err != nil {
			a.loginRedirect(w, r, "link_requires_session")
			return
		}
	}
	res, err := a.OIDC.Start(ctx, r.PathValue("slug"), intent, p, r.URL.Query().Get("return_to"))
	if err != nil {
		a.loginRedirect(w, r, string(apperr.CodeOf(err)))
		return
	}
	http.SetCookie(w, oidc.StateCookieFor(a.Cfg.SecureCookies(), res.State, int((10*time.Minute).Seconds())))
	http.Redirect(w, r, res.AuthURL, http.StatusFound) //nolint:gosec // the provider's discovered authorization endpoint
}

// oidcCallback finishes an SSO round trip and redirects back into the UI.
func (a *App) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx := audit.WithMeta(r.Context(), audit.Meta{SourceIP: httpx.ClientIP(r.Context()).String(), RequestID: httpx.GetRequestID(r.Context())})
	q := r.URL.Query()
	http.SetCookie(w, oidc.StateCookieFor(a.Cfg.SecureCookies(), "", -1))
	res, err := a.OIDC.Callback(ctx, q.Get("state"), oidc.StateFromRequest(r), q.Get("code"), q.Get("error"),
		auth.SessionMeta{IP: httpx.ClientIP(r.Context()).String(), UserAgent: r.UserAgent()})
	if err != nil {
		reason := "sso_failed"
		switch {
		case errors.Is(err, oidc.ErrNotLinked):
			reason = "sso_not_linked"
		case errors.Is(err, oidc.ErrDomainDenied), errors.Is(err, oidc.ErrEmailUnverify):
			reason = "sso_denied"
		case errors.Is(err, oidc.ErrAlreadyLinked):
			reason = "sso_already_linked"
		case errors.Is(err, oidc.ErrStateInvalid):
			reason = "sso_expired"
		case errors.Is(err, oidc.ErrUserInactive):
			reason = "account_suspended"
		}
		if apperr.CodeOf(err) == "" {
			a.component("oidc").WarnContext(ctx, "sso callback failed", obs.Err(err))
		}
		a.loginRedirect(w, r, reason)
		return
	}
	if res.Session != nil {
		http.SetCookie(w, auth.SessionCookie(a.Cfg.SecureCookies(), res.Session.Token, max(1, int(time.Until(res.Session.ExpiresAt).Seconds()))))
	}
	target := httpx.SafeReturnTo(res.ReturnTo, "/")
	if res.Intent == oidc.IntentLink {
		target = "/account?linked=1"
	}
	http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // target is a same-origin path validated by SafeReturnTo
}

func (a *App) loginRedirect(w http.ResponseWriter, r *http.Request, reason string) {
	if reason == "" {
		reason = "sso_failed"
	}
	http.Redirect(w, r, "/login?error="+url.QueryEscape(reason), http.StatusFound)
}

// ManagementHandler serves metrics, health and (opt-in) pprof on the private listener.
func (a *App) ManagementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", a.Metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		err := a.Ready(r.Context())
		a.Metrics.SetReady(err == nil)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "not ready: %v\n", err)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	if a.Cfg.Management.Pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

func (a *App) tlsConfig() (*tls.Config, error) {
	c := a.Cfg.HTTP
	if c.TLSCertFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

// Listeners are bound sockets (tests bind :0 and read the addresses).
type Listeners struct {
	Public, Management net.Listener
}

// Listen binds both listeners.
func (a *App) Listen(ctx context.Context) (*Listeners, error) {
	var lc net.ListenConfig
	pub, err := lc.Listen(ctx, "tcp", a.Cfg.HTTP.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", a.Cfg.HTTP.Listen, err)
	}
	mgmt, err := lc.Listen(ctx, "tcp", a.Cfg.Management.Listen)
	if err != nil {
		_ = pub.Close()
		return nil, fmt.Errorf("listen %s: %w", a.Cfg.Management.Listen, err)
	}
	return &Listeners{Public: pub, Management: mgmt}, nil
}

// Serve runs the servers and background workers until ctx is canceled, then shuts down gracefully: listeners stop
// accepting, in-flight requests and jobs get workers.shutdown_grace to finish, buffered token usage is flushed.
func (a *App) Serve(ctx context.Context, ls *Listeners) error {
	tc, err := a.tlsConfig()
	if err != nil {
		return err
	}
	c := a.Cfg.HTTP
	public := &http.Server{Handler: a.PublicHandler(), ReadHeaderTimeout: c.ReadHeaderTimeout.D(), ReadTimeout: c.ReadTimeout.D(), //nolint:contextcheck // handlers use request contexts
		WriteTimeout: c.WriteTimeout.D(), IdleTimeout: c.IdleTimeout.D(), MaxHeaderBytes: c.MaxHeaderBytes, TLSConfig: tc,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	mgmt := &http.Server{Handler: a.ManagementHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		if tc != nil {
			err = public.ServeTLS(ls.Public, "", "")
		} else {
			err = public.Serve(ls.Public)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	g.Go(func() error {
		if err := mgmt.Serve(ls.Management); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	var workers errgroup.Group
	for _, w := range a.backgroundWorkers() {
		workers.Go(func() error {
			w(workCtx)
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		grace := a.Cfg.Workers.ShutdownGrace.D()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		a.Log.Info("shutting down", "grace", grace.String())
		err := errors.Join(public.Shutdown(sctx), mgmt.Shutdown(sctx))
		stopWork()
		done := make(chan struct{})
		go func() { _ = workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-sctx.Done():
			a.Log.Warn("background workers did not stop within the grace period; leased work will be recovered on restart")
		}
		if ferr := a.Auth.FlushTokenUsage(context.WithoutCancel(ctx)); ferr != nil {
			a.Log.Warn("flushing token usage failed", obs.Err(ferr))
		}
		return err
	})
	a.Log.Info("wagenpulse serving", "public", ls.Public.Addr().String(), "management", ls.Management.Addr().String(),
		"public_url", a.Cfg.PublicURL, "tls", tc != nil, "version", buildinfo.Get().Version)
	return g.Wait()
}
