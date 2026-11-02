package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth/oidc"
	"github.com/aurimasniekis/go-wagenpulse/internal/httpx"
)

// Me describes the signed-in principal for the UI.
type Me struct {
	User           auth.User         `json:"user"`
	Method         string            `json:"method" doc:"password, oidc, setup or token"`
	Permissions    []auth.Permission `json:"permissions"`
	CSRFToken      string            `json:"csrf_token,omitempty" doc:"Send in X-CSRF-Token on state-changing cookie-authenticated requests."`
	MustEnrollTOTP bool              `json:"must_enroll_totp"`
	TokenID        string            `json:"token_id,omitempty" doc:"Set when authenticated by an API token."`
	VehicleIDs     []string          `json:"vehicle_ids,omitempty" doc:"Vehicle restriction of the API token, if any."`
}

func (d *Deps) me(ctx context.Context, p *auth.Principal) (Me, error) {
	u, err := d.Auth.GetUser(ctx, p.UserID)
	if err != nil {
		return Me{}, err
	}
	m := Me{User: u, Method: p.Method, Permissions: p.Permissions(), CSRFToken: d.Auth.CSRFToken(p), MustEnrollTOTP: p.MustEnrollTOTP}
	if p.Token != nil {
		m.TokenID, m.VehicleIDs = p.Token.ID, p.Token.VehicleIDs
	}
	return m, nil
}

// OriginHeaders are read on unauthenticated POSTs to stop login CSRF.
type OriginHeaders struct {
	Origin       string `header:"Origin" required:"false"`
	SecFetchSite string `header:"Sec-Fetch-Site" required:"false"`
	UserAgent    string `header:"User-Agent" required:"false"`
}

func (d *Deps) checkOrigin(h OriginHeaders) error {
	r := &http.Request{Header: http.Header{}, Method: http.MethodPost}
	if h.Origin != "" {
		r.Header.Set("Origin", h.Origin)
	}
	if h.SecFetchSite != "" {
		r.Header.Set("Sec-Fetch-Site", h.SecFetchSite)
	}
	if !httpx.SameOrigin(r, d.Origin) {
		return problem(http.StatusForbidden, apperr.CodeForbidden, "cross-origin request refused", nil)
	}
	return nil
}

func (d *Deps) loginLimited(ctx context.Context) error {
	if d.LoginRate != nil && !d.LoginRate.Allow("login:"+audit.MetaFrom(ctx).SourceIP) {
		return problem(http.StatusTooManyRequests, apperr.CodeRateLimited, "too many sign-in requests; slow down", nil)
	}
	return nil
}

func (d *Deps) sessionCookie(s *auth.NewSession) http.Cookie {
	maxAge := int(time.Until(s.ExpiresAt).Seconds())
	return *auth.SessionCookie(d.SecureCookies, s.Token, max(maxAge, 1))
}

// SetupRequest creates the root user with a one-time setup token.
type SetupRequest struct {
	Token       string `json:"token" minLength:"1" maxLength:"128" doc:"One-time token from 'wagenpulse admin setup-token' on the host."`
	Username    string `json:"username" minLength:"1" maxLength:"64"`
	DisplayName string `json:"display_name,omitempty" maxLength:"120"`
	Password    string `json:"password" minLength:"12" maxLength:"4096"`
}

// LoginRequest is a password login.
type LoginRequest struct {
	Username string `json:"username" minLength:"1" maxLength:"64"`
	Password string `json:"password" minLength:"1" maxLength:"4096"`
}

// LoginResponse is either a signed-in principal or a second-factor challenge.
type LoginResponse struct {
	Me        *Me    `json:"me,omitempty"`
	Challenge string `json:"challenge,omitempty" doc:"Present when a TOTP code is required; send it to /auth/login/totp within 5 minutes."`
}

// TOTPLoginRequest completes a login with a TOTP or recovery code.
type TOTPLoginRequest struct {
	Challenge string `json:"challenge" minLength:"1" maxLength:"128"`
	Code      string `json:"code" minLength:"6" maxLength:"16"`
}

type cookieOut[T any] struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      T
}

func registerAuth(api huma.API, d *Deps) {
	tags := []string{"auth"}
	huma.Register(api, public("get-setup-status", http.MethodGet, "/setup/status", "Whether first-run setup is complete", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body auth.SetupStatus }, error) {
			st, err := d.Auth.SetupStatus(ctx)
			if err != nil {
				return nil, d.fail(ctx, "setup-status", err)
			}
			return &struct{ Body auth.SetupStatus }{Body: st}, nil
		})
	huma.Register(api, public("complete-setup", http.MethodPost, "/setup", "Create the root user with a one-time setup token", tags...),
		func(ctx context.Context, in *struct {
			OriginHeaders
			Body SetupRequest
		}) (*cookieOut[Me], error) {
			if err := d.checkOrigin(in.OriginHeaders); err != nil {
				return nil, err
			}
			if err := d.loginLimited(ctx); err != nil {
				return nil, err
			}
			res, err := d.Auth.CompleteSetup(ctx, auth.SetupInput{Token: in.Body.Token, Username: in.Body.Username, DisplayName: in.Body.DisplayName,
				Password: in.Body.Password}, sessionMeta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.fail(ctx, "setup", err)
			}
			p, err := d.Auth.Authenticate(ctx, res.Session.Token)
			if err != nil {
				return nil, d.fail(ctx, "setup", err)
			}
			me, err := d.me(ctx, p)
			if err != nil {
				return nil, d.fail(ctx, "setup", err)
			}
			return &cookieOut[Me]{SetCookie: []http.Cookie{d.sessionCookie(res.Session)}, Body: me}, nil
		})
	huma.Register(api, public("get-login-methods", http.MethodGet, "/auth/methods", "Enabled sign-in methods", tags...),
		func(ctx context.Context, _ *struct{}) (*struct{ Body auth.LoginMethods }, error) {
			m, err := d.Auth.Methods(ctx)
			if err != nil {
				return nil, d.fail(ctx, "login-methods", err)
			}
			return &struct{ Body auth.LoginMethods }{Body: m}, nil
		})
	huma.Register(api, public("login", http.MethodPost, "/auth/login", "Sign in with username and password", tags...),
		func(ctx context.Context, in *struct {
			OriginHeaders
			Body LoginRequest
		}) (*cookieOut[LoginResponse], error) {
			if err := d.checkOrigin(in.OriginHeaders); err != nil {
				return nil, err
			}
			if err := d.loginLimited(ctx); err != nil {
				return nil, err
			}
			res, err := d.Auth.Login(ctx, in.Body.Username, in.Body.Password, sessionMeta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.fail(ctx, "login", err)
			}
			if res.Challenge != "" {
				return &cookieOut[LoginResponse]{Body: LoginResponse{Challenge: res.Challenge}}, nil
			}
			return d.loggedIn(ctx, res)
		})
	huma.Register(api, public("login-totp", http.MethodPost, "/auth/login/totp", "Complete sign-in with a TOTP or recovery code", tags...),
		func(ctx context.Context, in *struct {
			OriginHeaders
			Body TOTPLoginRequest
		}) (*cookieOut[LoginResponse], error) {
			if err := d.checkOrigin(in.OriginHeaders); err != nil {
				return nil, err
			}
			if err := d.loginLimited(ctx); err != nil {
				return nil, err
			}
			res, err := d.Auth.VerifyChallenge(ctx, in.Body.Challenge, in.Body.Code, sessionMeta(ctx, in.UserAgent))
			if err != nil {
				return nil, d.fail(ctx, "login-totp", err)
			}
			return d.loggedIn(ctx, res)
		})
	huma.Register(api, totpExempt(interactive(noContent(op("logout", http.MethodPost, "/auth/logout", auth.PermSystemRead, "Sign out", tags...)))),
		func(ctx context.Context, _ *struct{}) (*struct {
			SetCookie []http.Cookie `header:"Set-Cookie"`
		}, error) {
			if err := d.Auth.Logout(ctx, Principal(ctx)); err != nil {
				return nil, d.fail(ctx, "logout", err)
			}
			return &struct {
				SetCookie []http.Cookie `header:"Set-Cookie"`
			}{SetCookie: []http.Cookie{*auth.SessionCookie(d.SecureCookies, "", -1)}}, nil
		})
	huma.Register(api, totpExempt(op("get-me", http.MethodGet, "/me", auth.PermSystemRead, "The signed-in user, permissions and CSRF token", tags...)),
		func(ctx context.Context, _ *struct{}) (*struct{ Body Me }, error) {
			me, err := d.me(ctx, Principal(ctx))
			if err != nil {
				return nil, d.fail(ctx, "me", err)
			}
			return &struct{ Body Me }{Body: me}, nil
		})
	huma.Register(api, totpExempt(interactive(noContent(op("change-password", http.MethodPut, "/me/password", auth.PermSystemRead,
		"Change your password (other sessions are signed out)", tags...)))),
		func(ctx context.Context, in *struct {
			Body struct {
				CurrentPassword string `json:"current_password,omitempty" maxLength:"4096"`
				NewPassword     string `json:"new_password" minLength:"12" maxLength:"4096"`
			}
		}) (*struct{}, error) {
			if err := d.Auth.ChangeOwnPassword(ctx, Principal(ctx), in.Body.CurrentPassword, in.Body.NewPassword); err != nil {
				return nil, d.fail(ctx, "change-password", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, totpExempt(interactive(op("begin-totp", http.MethodPost, "/me/totp", auth.PermSystemRead,
		"Start TOTP enrollment (returns the secret for an authenticator app)", tags...))),
		func(ctx context.Context, _ *struct{}) (*struct{ Body auth.TOTPEnrollment }, error) {
			e, err := d.Auth.BeginTOTP(ctx, Principal(ctx))
			if err != nil {
				return nil, d.fail(ctx, "begin-totp", err)
			}
			return &struct{ Body auth.TOTPEnrollment }{Body: e}, nil
		})
	huma.Register(api, totpExempt(interactive(op("confirm-totp", http.MethodPost, "/me/totp/confirm", auth.PermSystemRead,
		"Enable TOTP with a current code; returns recovery codes once", tags...))),
		func(ctx context.Context, in *struct {
			Body struct {
				Code string `json:"code" minLength:"6" maxLength:"6"`
			}
		}) (*struct {
			Body struct {
				RecoveryCodes []string `json:"recovery_codes"`
			}
		}, error) {
			codes, err := d.Auth.ConfirmTOTP(ctx, Principal(ctx), in.Body.Code)
			if err != nil {
				return nil, d.fail(ctx, "confirm-totp", err)
			}
			out := &struct {
				Body struct {
					RecoveryCodes []string `json:"recovery_codes"`
				}
			}{}
			out.Body.RecoveryCodes = codes
			return out, nil
		})
	huma.Register(api, interactive(noContent(op("disable-totp", http.MethodDelete, "/me/totp", auth.PermSystemRead,
		"Disable your TOTP (requires a current code)", tags...))),
		func(ctx context.Context, in *struct {
			Code string `query:"code" maxLength:"16"`
		}) (*struct{}, error) {
			p := Principal(ctx)
			if err := d.Auth.DisableTOTP(ctx, p, p.UserID, in.Code); err != nil {
				return nil, d.fail(ctx, "disable-totp", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, interactive(op("list-my-sessions", http.MethodGet, "/me/sessions", auth.PermSystemRead, "Your active sessions", tags...)),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []auth.Session }, error) {
			p := Principal(ctx)
			ss, err := d.Auth.ListSessions(ctx, p.UserID, p.SessionID)
			if err != nil {
				return nil, d.fail(ctx, "list-sessions", err)
			}
			return &struct{ Body []auth.Session }{Body: ss}, nil
		})
	huma.Register(api, interactive(noContent(op("revoke-my-session", http.MethodDelete, "/me/sessions/{id}", auth.PermSystemRead,
		"Sign out one of your sessions", tags...))),
		func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"64"`
		}) (*struct{}, error) {
			p := Principal(ctx)
			if err := d.Auth.RevokeSession(ctx, p, p.UserID, in.ID); err != nil {
				return nil, d.fail(ctx, "revoke-session", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, interactive(op("list-my-identities", http.MethodGet, "/me/identities", auth.PermSystemRead, "Your linked SSO identities", tags...)),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []oidc.Identity }, error) {
			ids, err := d.OIDC.Identities(ctx, Principal(ctx).UserID)
			if err != nil {
				return nil, d.fail(ctx, "identities", err)
			}
			return &struct{ Body []oidc.Identity }{Body: ids}, nil
		})
	huma.Register(api, interactive(noContent(op("unlink-my-identity", http.MethodDelete, "/me/identities/{id}", auth.PermSystemRead,
		"Unlink an SSO identity from your account", tags...))),
		func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"64"`
		}) (*struct{}, error) {
			p := Principal(ctx)
			if err := d.OIDC.Unlink(ctx, p, p.UserID, in.ID); err != nil {
				return nil, d.fail(ctx, "unlink", err)
			}
			return &struct{}{}, nil
		})
}

func (d *Deps) loggedIn(ctx context.Context, res *auth.LoginResult) (*cookieOut[LoginResponse], error) {
	p, err := d.Auth.Authenticate(ctx, res.Session.Token)
	if err != nil {
		return nil, d.fail(ctx, "login", err)
	}
	me, err := d.me(ctx, p)
	if err != nil {
		return nil, d.fail(ctx, "login", err)
	}
	return &cookieOut[LoginResponse]{SetCookie: []http.Cookie{d.sessionCookie(res.Session)}, Body: LoginResponse{Me: &me}}, nil
}
