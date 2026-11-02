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
	// Captcha makes the sign-in page show a bot challenge.
	Captcha bool
	// Hits counts requests by path prefix.
	Hits map[string]int
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
	path := r.URL.Path
	for _, pre := range []string{"/proxy_api/consent", "/proxy_api/euda-apim/datarequest", "/proxy_api/euda-apim/datadelivery", "/oidc", "/u/login", "/login"} {
		if strings.HasPrefix(path, pre) {
			p.Hits[pre]++
		}
	}
	switch {
	case path == "/oidc/v1/authorize":
		http.Redirect(w, r, "/u/login?state=abc&redirect="+r.URL.Query().Get("redirect_uri"), http.StatusFound)
	case path == "/u/login" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/html")
		if p.Captcha {
			_, _ = fmt.Fprint(w, `<html><body>Please complete the CAPTCHA</body></html>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<html><form method="post" action="/u/login?state=abc"><input type="hidden" name="state" value="abc"><input name="username"><input name="password"></form></html>`)
	case path == "/u/login" && r.Method == http.MethodPost:
		_ = r.ParseForm()
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
		if !p.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
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

func (p *Portal) api(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
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
