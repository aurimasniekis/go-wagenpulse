// Login flows adapted from go-cupra internal/cupra/eudataact/login.go (same author) and the community-documented
// VW Identity sign-in sequence (docs/CAPABILITIES.md E4–E6).

package vwdataact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// AuthorizeURL builds the portal's OIDC authorization URL (the portal is the confidential client and exchanges the
// code at its /login callback).
func (c *Client) AuthorizeURL() (string, error) {
	b, ok := Brands[c.cfg.Brand]
	if !ok {
		return "", &Error{Class: ClassAuth, Msg: "unknown brand for password login"}
	}
	key := c.cfg.StateKey
	if key == "" {
		key = b.StateKey
	}
	if key == "" {
		return "", &Error{Class: ClassAuth, Msg: "password login for this brand needs the portal brand key (portal_brand_key); " +
			"copy it from the portal's login URL (state=country__language__BRANDKEY) or use a session cookie"}
	}
	country, lang := strings.ToLower(c.cfg.Country), strings.ToLower(c.cfg.Language)
	q := url.Values{}
	q.Set("client_id", b.ClientID)
	q.Set("response_type", "code")
	q.Set("scope", "openid cars profile")
	q.Set("state", country+"__"+lang+"__"+key)
	q.Set("redirect_uri", c.cfg.BaseURL+"/login")
	q.Set("prompt", "login")
	return strings.TrimRight(c.cfg.IdentityBase, "/") + "/oidc/v1/authorize?" + q.Encode(), nil
}

// PasswordLogin returns a login function that signs in with the owner's brand account. It supports the two sign-in
// pages the identity provider is documented to serve (Auth0 Universal Login and the classic signin-service). It never
// solves CAPTCHAs, never accepts terms or consent on the user's behalf and never retries a rejected password.
func PasswordLogin(email, password string) func(ctx context.Context, c *Client) error {
	return func(ctx context.Context, c *Client) error {
		authorize, err := c.AuthorizeURL()
		if err != nil {
			return err
		}
		page, finalURL, status, err := c.fetch(ctx, http.MethodGet, authorize, nil, c.cfg.BaseURL+"/")
		if err != nil {
			return atEndpoint(err, "login:authorize")
		}
		if status >= 400 {
			return &Error{Class: ClassUnavailable, Status: status, Endpoint: "login:authorize", Msg: "the identity provider did not serve a sign-in page"}
		}
		// Submit the form as the browser would. Whether a captcha is required is decided by the identity provider's
		// answer (or a challenge page instead of the form), never by strings that the sign-in page always contains.
		switch {
		case strings.Contains(finalURL, "/u/login"):
			err = c.auth0Login(ctx, page, finalURL, email, password)
		case strings.Contains(finalURL, "/signin-service/"):
			err = c.classicLogin(ctx, page, finalURL, email, password)
		case challenged(page):
			return &Error{Class: ClassChallenge, Endpoint: "login:authorize", Msg: "the identity provider presented a bot challenge; sign in with a browser and use a session cookie"}
		default:
			return &Error{Class: ClassProtocol, Endpoint: "login:authorize", Msg: "the identity provider served an unknown sign-in page"}
		}
		if err != nil {
			return err
		}
		// The redirect chain ends at the portal, which sets its session cookies. Verify the session. Only a rejected
		// session, a challenge or a rate limit count: the vehicle list's live shape is unverified (docs/CAPABILITIES.md),
		// so any other failure of it says nothing about the sign-in, and the data calls that follow judge the session.
		if _, err := c.Vehicles(ctx); err != nil {
			switch ClassOf(err) {
			case ClassAuth:
				return &Error{Class: ClassAuth, Endpoint: "login:session", Msg: "sign-in did not establish a portal session (wrong password, pending terms or consent?)"}
			case ClassChallenge, ClassRateLimited, "":
				return err
			}
		}
		return nil
	}
}

func (c *Client) auth0Login(ctx context.Context, page, pageURL, email, password string) error {
	action, fields := parseForm(page)
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	form.Set("username", email)
	form.Set("password", password)
	if form.Get("action") == "" {
		form.Set("action", "default")
	}
	post := resolve(pageURL, action)
	body, final, status, err := c.fetch(ctx, http.MethodPost, post, form, pageURL)
	if err != nil {
		return atEndpoint(err, "login:credentials")
	}
	return atEndpoint(c.afterSubmit(body, final, status), "login:credentials")
}

var templateModelRe = regexp.MustCompile(`(?s)templateModel\s*:\s*(\{.*?\})\s*,\s*\n`)
var csrfRe = regexp.MustCompile(`csrf_token\s*:\s*'([^']{1,200})'`)

func (c *Client) classicLogin(ctx context.Context, page, pageURL, email, password string) error {
	action, fields := parseForm(page)
	if fields["_csrf"] == "" || fields["hmac"] == "" {
		return &Error{Class: ClassProtocol, Endpoint: "login:authorize", Msg: "the classic sign-in form changed (missing _csrf/hmac)"}
	}
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	form.Set("email", email)
	body, final, status, err := c.fetch(ctx, http.MethodPost, resolve(pageURL, action), form, pageURL)
	if err != nil {
		return atEndpoint(err, "login:identifier")
	}
	if status >= 400 {
		return &Error{Class: ClassAuth, Status: status, Endpoint: "login:identifier", Msg: "the identity provider rejected the account identifier"}
	}
	if challenged(body) {
		return &Error{Class: ClassChallenge, Endpoint: "login:identifier", Msg: "the identity provider presented a bot challenge; use a session cookie"}
	}
	m := templateModelRe.FindStringSubmatch(body)
	if m == nil {
		return &Error{Class: ClassProtocol, Endpoint: "login:identifier", Msg: "the password page changed (no templateModel)"}
	}
	var tm struct {
		HMAC       string `json:"hmac"`
		RelayState string `json:"relayState"`
		PostAction string `json:"postAction"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal([]byte(m[1]), &tm); err != nil || tm.HMAC == "" || tm.PostAction == "" {
		return &Error{Class: ClassProtocol, Endpoint: "login:identifier", Msg: "the password page changed (templateModel)"}
	}
	if tm.Error != "" {
		return &Error{Class: ClassAuth, Endpoint: "login:identifier", Msg: "the identity provider rejected the account"}
	}
	csrf := fields["_csrf"]
	if cm := csrfRe.FindStringSubmatch(body); cm != nil {
		csrf = cm[1]
	}
	pw := url.Values{}
	pw.Set("_csrf", csrf)
	pw.Set("relayState", tm.RelayState)
	pw.Set("hmac", tm.HMAC)
	pw.Set("email", email)
	pw.Set("password", password)
	body, final2, status, err := c.fetch(ctx, http.MethodPost, resolve(final, tm.PostAction), pw, final)
	if err != nil {
		return atEndpoint(err, "login:password")
	}
	return atEndpoint(c.afterSubmit(body, final2, status), "login:password")
}

// afterSubmit interprets where the credential POST ended.
func (c *Client) afterSubmit(body, final string, status int) error {
	switch {
	case challenged(body):
		return &Error{Class: ClassChallenge, Msg: "the identity provider presented a bot challenge; use a session cookie"}
	case strings.Contains(final, "/terms") || strings.Contains(final, "/consent") || strings.Contains(final, "marketing"):
		return &Error{Class: ClassAuth, Msg: "the account has pending terms or consent; sign in once in a browser to review them"}
	case strings.HasPrefix(final, c.cfg.BaseURL):
		return nil
	case status >= 400 || strings.Contains(final, "error") || strings.Contains(final, "/login/authenticate"):
		return &Error{Class: ClassAuth, Status: status, Msg: "the identity provider rejected the credentials"}
	}
	return nil // verified by the caller through a portal request
}

var (
	inputRe   = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	attrNameR = regexp.MustCompile(`(?is)\bname\s*=\s*"([^"]*)"`)
	attrValR  = regexp.MustCompile(`(?is)\bvalue\s*=\s*"([^"]*)"`)
	formActR  = regexp.MustCompile(`(?is)<form\b[^>]*\baction\s*=\s*"([^"]*)"`)
	// captchaFieldRe matches what a page carries only when a captcha answer is actually demanded: a captcha input or
	// a widget site key. Sign-in pages load captcha scripts and empty containers on every visit, so the bare word
	// "captcha" is not evidence of a challenge.
	captchaFieldRe = regexp.MustCompile(`(?is)<input\b[^>]*\bname\s*=\s*"(captcha|g-recaptcha-response|h-captcha-response|cf-turnstile-response|fc-token|arkose[^"]*)"|\bdata-(captcha-)?sitekey\s*=`)
	// interstitialRe matches a challenge page served instead of a sign-in form.
	interstitialRe = regexp.MustCompile(`(?is)<title>\s*just a moment|cf-chl|challenge-platform|are you (a )?human\?|verify you are human`)
)

// challenged reports whether a page demands a captcha answer or is a bot-challenge interstitial.
func challenged(html string) bool {
	return captchaFieldRe.MatchString(html) || interstitialRe.MatchString(html)
}

func parseForm(html string) (string, map[string]string) {
	fields := map[string]string{}
	action := ""
	if m := formActR.FindStringSubmatch(html); m != nil {
		action = m[1]
	}
	for _, tag := range inputRe.FindAllString(html, 200) {
		nm := attrNameR.FindStringSubmatch(tag)
		if nm == nil {
			continue
		}
		val := ""
		if vm := attrValR.FindStringSubmatch(tag); vm != nil {
			val = strings.ReplaceAll(vm[1], "&amp;", "&")
		}
		fields[nm[1]] = val
	}
	return action, fields
}

func resolve(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// fetch performs a browser-like request following redirects and returns the body, the final URL and the status.
func (c *Client) fetch(ctx context.Context, method, rawURL string, form url.Values, referer string) (string, string, int, error) {
	if err := c.pace(ctx); err != nil {
		return "", "", 0, err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		// The parse error quotes the URL, which can carry a state or nonce: never pass it on.
		return "", "", 0, &Error{Class: ClassProtocol, Msg: "the sign-in request could not be built"}
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", 0, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			return "", "", 0, &Error{Class: ClassUnavailable, Msg: "network error during sign-in"}
		}
		return "", "", 0, &Error{Class: ClassUnavailable, Msg: "network error during sign-in"}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return string(b), resp.Request.URL.String(), resp.StatusCode, nil
}
