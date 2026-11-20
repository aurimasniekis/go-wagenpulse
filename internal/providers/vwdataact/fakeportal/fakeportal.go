// Package fakeportal is an in-process imitation of the EU Data Act portal's web backend and a minimal identity
// provider, shaped after the community-documented behaviour (docs/CAPABILITIES.md E4–E6). It is a test fixture: it
// is never compiled into the server and its responses are synthetic.
package fakeportal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// File is a delivered file.
type File struct {
	Name      string
	CreatedOn time.Time
	Data      []byte
}

// Portal is the fake.
type Portal struct {
	Server *httptest.Server

	mu sync.Mutex
	// SessionCookie is the cookie value the portal accepts (name "PORTALSESSION").
	SessionCookie string
	// Password is the password the identity accepts for Email.
	Email, Password string
	// Requests maps VIN → request type → identifier ("" = no active request).
	Requests map[string]map[string]string
	// Files maps identifier → files.
	Files map[string][]File
	// RateLimit makes the next N data requests answer 429 with Retry-After seconds.
	RateLimit      int
	RetryAfterSecs int
	// Captcha makes the sign-in form demand a captcha answer (Auth0 bot detection triggered); a credential POST
	// without one is rejected.
	Captcha bool
	// Interstitial serves a bot-challenge page instead of the sign-in form (a CDN challenge in front of the IdP).
	Interstitial bool
	// Expired selects how the portal answers an API request with a session it no longer accepts:
	//   ExpiredUnauthorized ("", the default): HTTP 401;
	//   ExpiredRedirect: a redirect into the sign-in flow, which ends in an HTML sign-in page with status 200;
	//   ExpiredEmpty: as if the account had nothing (no vehicles, 404 "No data request found" for metadata).
	// The real portal's behaviour is not verified (docs/CAPABILITIES.md); these shapes keep the collector honest about
	// all of them.
	Expired string
	// Fail makes an endpoint ("vehicles", "identifier", "list", "download") answer the given HTTP status. It applies to
	// an authorized session and to an expired one answered as ExpiredEmpty.
	Fail map[string]int
	// MovedTo imitates a portal that moved hosts (2026-11-16, docs/CAPABILITIES.md E7): every API request answers 301
	// to that base URL, under a rewritten path that answers 404 there.
	MovedTo string
	// Hits counts requests by path prefix.
	Hits map[string]int
	// Total counts every request that reached the portal, whatever its path.
	Total int
	// UserAgents seen.
	UserAgents map[string]bool
}

// New starts a fake portal.
func New() *Portal {
	p := &Portal{SessionCookie: "valid-session", Requests: map[string]map[string]string{}, Files: map[string][]File{}, Hits: map[string]int{},
		UserAgents: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}

// Close stops the server.
func (p *Portal) Close() { p.Server.Close() }

// URL is the base URL.
func (p *Portal) URL() string { return p.Server.URL }

// Set mutates the fake under its lock.
func (p *Portal) Set(fn func(*Portal)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
}

func (p *Portal) authorized(r *http.Request) bool {
	c, err := r.Cookie("PORTALSESSION")
	return err == nil && c.Value == p.SessionCookie
}

func (p *Portal) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.UserAgents[r.UserAgent()] = true
	p.Total++
	path := r.URL.Path
	for _, pre := range []string{"/proxy_api/consent", "/proxy_api/euda-apim/datarequest", "/proxy_api/euda-apim/datadelivery", "/oidc", "/u/login", "/login"} {
		if strings.HasPrefix(path, pre) {
			p.Hits[pre]++
		}
	}
	switch {
	case path == "/oidc/v1/authorize":
		if p.Interstitial {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body><div id="cf-chl-widget"></div></body></html>`)
			return
		}
		http.Redirect(w, r, "/u/login?state=abc&redirect="+r.URL.Query().Get("redirect_uri"), http.StatusFound)
	case path == "/u/login" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, p.loginPage(""))
	case path == "/u/login" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		if p.Captcha && r.PostForm.Get("captcha") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, p.loginPage("Solve the challenge question to verify you are not a robot."))
			return
		}
		if r.PostForm.Get("username") != p.Email || r.PostForm.Get("password") != p.Password {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `<html>Wrong email or password</html>`)
			return
		}
		http.Redirect(w, r, p.Server.URL+"/login?code=xyz", http.StatusFound)
	case path == "/login":
		http.SetCookie(w, &http.Cookie{Name: "PORTALSESSION", Value: p.SessionCookie, Path: "/"})
		_, _ = fmt.Fprint(w, "ok")
	case strings.HasPrefix(path, "/proxy_api/"):
		if p.MovedTo != "" {
			http.Redirect(w, r, p.MovedTo+"/en"+path, http.StatusMovedPermanently)
			return
		}
		if !p.authorized(r) {
			switch p.Expired {
			case ExpiredRedirect:
				http.Redirect(w, r, "/oidc/v1/authorize?client_id=x&redirect_uri="+p.Server.URL+"/login", http.StatusFound)
			case ExpiredEmpty:
				p.expiredEmpty(w, r)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		if p.RateLimit > 0 && strings.HasPrefix(path, "/proxy_api/euda-apim/") {
			p.RateLimit--
			w.Header().Set("Retry-After", fmt.Sprint(p.RetryAfterSecs))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		p.api(w, r)
	default:
		http.NotFound(w, r)
	}
}

// loginPage imitates Auth0 Universal Login. Like the real page it always loads the captcha script and an empty widget
// container, whether or not a captcha is required; only a triggered challenge adds the widget's site key and the
// captcha input.
func (p *Portal) loginPage(errMsg string) string {
	widget := ""
	if p.Captcha {
		widget = `<div class="ulp-captcha" data-captcha-sitekey="fake-site-key" data-captcha-provider="recaptcha_v2"></div><input type="hidden" name="captcha" value="">`
	}
	if errMsg != "" {
		errMsg = `<span class="ulp-input-error-message">` + errMsg + `</span>`
	}
	return `<html><head><script src="/u/assets/captcha.js"></script><style>.ulp-captcha-container{display:none}</style></head><body>` +
		`<form method="post" action="/u/login?state=abc"><input type="hidden" name="state" value="abc"><input name="username"><input name="password">` +
		`<div class="ulp-captcha-container">` + widget + `</div>` + errMsg + `</form></body></html>`
}

// Expired-session behaviours (see Portal.Expired).
const (
	ExpiredUnauthorized = ""
	ExpiredRedirect     = "redirect"
	ExpiredEmpty        = "empty"
)

func (p *Portal) expiredEmpty(w http.ResponseWriter, r *http.Request) {
	if st := p.Fail[endpointOf(r.URL.Path)]; st != 0 {
		w.WriteHeader(st)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/proxy_api/consent/me/vehicles" {
		_, _ = fmt.Fprint(w, `{"vehicles":[]}`)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, `{"message":"No data request found"}`)
}

// endpointOf names an API path the way Fail does.
func endpointOf(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/proxy_api/consent/me/vehicles":
		return "vehicles"
	case len(parts) == 7 && parts[2] == "datarequest" && parts[5] == "metadata":
		return "identifier"
	case len(parts) == 7 && parts[2] == "datadelivery":
		return parts[6]
	}
	return ""
}

func (p *Portal) api(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if st := p.Fail[endpointOf(r.URL.Path)]; st != 0 {
		w.WriteHeader(st)
		return
	}
	switch {
	case r.URL.Path == "/proxy_api/consent/me/vehicles":
		var vins []string
		for vin := range p.Requests {
			vins = append(vins, fmt.Sprintf(`{"vin":%q}`, vin))
		}
		_, _ = fmt.Fprintf(w, `{"vehicles":[%s]}`, strings.Join(vins, ","))
	case len(parts) == 7 && parts[2] == "datarequest" && parts[5] == "metadata":
		ident := p.Requests[parts[4]][parts[6]]
		if ident == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"No data request found"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"Identifier":%q,"type":%q}`, ident, parts[6])
	case len(parts) == 7 && parts[2] == "datadelivery" && parts[6] == "list":
		files := p.Files[parts[5]]
		if len(files) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var items []string
		for _, f := range files {
			items = append(items, fmt.Sprintf(`{"name":%q,"createdOn":%q,"size":%d}`, f.Name, f.CreatedOn.Format(time.RFC3339), len(f.Data)))
		}
		_, _ = fmt.Fprintf(w, `[%s]`, strings.Join(items, ","))
	case len(parts) == 7 && parts[2] == "datadelivery" && parts[6] == "download":
		name := r.Header.Get("filename")
		for _, f := range p.Files[parts[5]] {
			if f.Name == name {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(f.Data)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}
