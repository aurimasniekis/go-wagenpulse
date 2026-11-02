package oidc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/auth"
	"github.com/aurimasniekis/go-wagenpulse/internal/httpx"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

var errNoRows = sql.ErrNoRows

// Intents of a login round trip.
const (
	IntentLogin = "login"
	IntentLink  = "link"
)

const (
	stateTTL = 10 * time.Minute
	// StateCookie binds the round trip to the browser that started it (login CSRF protection).
	StateCookie         = "__Host-wgp_oidc"
	InsecureStateCookie = "wgp_oidc"
)

// StartResult is where to send the browser and the state to set as a cookie.
type StartResult struct {
	AuthURL string
	State   string
}

// Start begins a login (or, with intent link and an authenticated user, an identity link).
func (svc *Service) Start(ctx context.Context, slug, intent string, linkUser *auth.Principal, returnTo string) (*StartResult, error) {
	if ok, err := svc.auth.SystemAllowsOIDC(ctx); err != nil {
		return nil, err
	} else if !ok && intent == IntentLogin {
		return nil, apperr.New(apperr.CodeForbidden, "single sign-on is disabled")
	}
	r, err := svc.get(ctx, svc.s.R(), slug)
	if err != nil {
		return nil, err
	}
	if r.Enabled != 1 {
		return nil, ErrProviderNotFound
	}
	if intent == IntentLink && (linkUser == nil || linkUser.Token != nil) {
		return nil, apperr.New(apperr.CodeUnauthorized, "sign in first to link an identity")
	}
	cfg, err := svc.upstreamConfig(r)
	if err != nil {
		return nil, err
	}
	secrets, err := NewSecrets()
	if err != nil {
		return nil, err
	}
	state, err := random(32)
	if err != nil {
		return nil, err
	}
	authURL, err := svc.up.AuthURL(ctx, cfg, state, secrets, AuthOptions{}) // network I/O: outside any transaction
	if err != nil {
		svc.log.WarnContext(ctx, "oidc discovery failed", "provider", r.Slug, "err", err.Error())
		return nil, apperr.Wrap(apperr.CodeUnavailable, err, "the identity provider is unavailable")
	}
	stateMAC, err := svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/oidc-state/v1|"+state))
	if err != nil {
		return nil, err
	}
	id := ids.New(ids.LoginState)
	sb, _ := json.Marshal(secrets)
	sealed, err := svc.kr.Seal(keyring.PurposeField, keyring.FieldAAD("oidc_login_states", "secrets_enc", id), sb)
	if err != nil {
		return nil, err
	}
	var userID sql.NullString
	if intent == IntentLink {
		userID = sql.NullString{String: linkUser.UserID, Valid: true}
	}
	err = svc.s.W().TxNamed(ctx, "oidc.start", func(tx *store.Tx) error {
		return tx.Q.InsertOIDCState(ctx, db.InsertOIDCStateParams{ID: id, StateMac: stateMAC, ProviderID: r.ID, Intent: intent, UserID: userID,
			SecretsEnc: sealed, ReturnTo: httpx.SafeReturnTo(returnTo, "/"), CreatedAt: tx.NowMicros(), ExpiresAt: tx.Now.Add(stateTTL).UnixMicro()})
	})
	if err != nil {
		return nil, err
	}
	return &StartResult{AuthURL: authURL, State: state}, nil
}

// StateCookieFor builds the browser-binding cookie (maxAge < 0 clears it).
func StateCookieFor(secure bool, state string, maxAge int) *http.Cookie {
	name := InsecureStateCookie
	if secure {
		name = StateCookie
	}
	return &http.Cookie{Name: name, Value: state, //nolint:gosec // Secure follows the public URL scheme (https in production)
		Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// StateFromRequest reads the binding cookie.
func StateFromRequest(r *http.Request) string {
	for _, n := range []string{StateCookie, InsecureStateCookie} {
		if c, err := r.Cookie(n); err == nil && len(c.Value) < 100 {
			return c.Value
		}
	}
	return ""
}

// CallbackResult is the outcome of a callback.
type CallbackResult struct {
	Intent   string
	Session  *auth.NewSession
	ReturnTo string
}

// Callback-specific errors carry codes the UI maps to messages.
var (
	ErrStateInvalid  = apperr.New(apperr.CodeUnauthorized, "the sign-in attempt expired or was started in another browser; try again")
	ErrNotLinked     = apperr.New(apperr.CodeForbidden, "no WagenPulse account is linked to this identity; sign in with your password and link it under Account")
	ErrDomainDenied  = apperr.New(apperr.CodeForbidden, "your email domain is not allowed to sign in with this provider")
	ErrEmailUnverify = apperr.New(apperr.CodeForbidden, "the identity provider did not confirm your email address")
	ErrAlreadyLinked = apperr.New(apperr.CodeConflict, "this identity is already linked to another WagenPulse account")
	ErrUserInactive  = apperr.New(apperr.CodeForbidden, "your WagenPulse account is suspended")
)

// Callback completes a round trip: verifies state (and its browser binding), exchanges the code, validates the ID
// token, then logs in or links.
func (svc *Service) Callback(ctx context.Context, state, cookieState, code, upstreamErr string, meta auth.SessionMeta) (*CallbackResult, error) {
	if state == "" || cookieState == "" || state != cookieState {
		return nil, ErrStateInvalid
	}
	macs, err := svc.kr.MACAll(keyring.PurposeToken, []byte("wagenpulse/oidc-state/v1|"+state))
	if err != nil {
		return nil, err
	}
	var st db.OidcLoginState
	found := false
	err = svc.s.W().TxNamed(ctx, "oidc.take_state", func(tx *store.Tx) error {
		for _, m := range macs {
			r, err := tx.Q.TakeOIDCState(ctx, m)
			if err == nil {
				st, found = r, true
				return nil
			}
			if !errors.Is(err, errNoRows) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found || svc.now().UnixMicro() >= st.ExpiresAt {
		return nil, ErrStateInvalid
	}
	if upstreamErr != "" {
		return nil, apperr.New(apperr.CodeUnauthorized, "the identity provider refused the sign-in (%s)", truncate(upstreamErr, 60))
	}
	r, err := svc.s.R().GetOIDCProvider(ctx, st.ProviderID)
	if err != nil || r.Enabled != 1 {
		return nil, ErrProviderNotFound
	}
	pt, err := svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("oidc_login_states", "secrets_enc", st.ID), st.SecretsEnc)
	if err != nil {
		return nil, err
	}
	var secrets Secrets
	if err := json.Unmarshal(pt, &secrets); err != nil {
		return nil, err
	}
	cfg, err := svc.upstreamConfig(r)
	if err != nil {
		return nil, err
	}
	claims, err := svc.up.Exchange(ctx, cfg, code, secrets) // network I/O outside transactions
	if err != nil {
		svc.log.WarnContext(ctx, "oidc exchange failed", "provider", r.Slug, "err", err.Error())
		return nil, apperr.New(apperr.CodeUnauthorized, "the identity provider's response could not be verified")
	}
	res := &CallbackResult{Intent: st.Intent, ReturnTo: st.ReturnTo}
	switch st.Intent {
	case IntentLink:
		err = svc.link(ctx, r, st.UserID.String, claims)
	default:
		res.Session, err = svc.login(ctx, r, claims, meta)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (svc *Service) link(ctx context.Context, r db.OidcProvider, userID string, cl Claims) error {
	return svc.s.W().TxNamed(ctx, "oidc.link", func(tx *store.Tx) error {
		if existing, err := tx.Q.GetIdentityBySubject(ctx, db.GetIdentityBySubjectParams{ProviderID: r.ID, Subject: cl.Subject}); err == nil {
			if existing.UserID == userID {
				return nil
			}
			return ErrAlreadyLinked
		} else if !errors.Is(err, errNoRows) {
			return err
		}
		id := ids.New(ids.Identity)
		if err := tx.Q.InsertIdentity(ctx, db.InsertIdentityParams{ID: id, UserID: userID, ProviderID: r.ID, Subject: cl.Subject,
			Email: nullStr(cl.Email), LinkedAt: tx.NowMicros()}); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return apperr.New(apperr.CodeConflict, "your account already has an identity from this provider; unlink it first")
			}
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "identity.linked",
			Target: audit.Target{Type: "identity", ID: id}, Details: map[string]any{"provider": r.Slug}})
		return err
	})
}

func (svc *Service) login(ctx context.Context, r db.OidcProvider, cl Claims, meta auth.SessionMeta) (*auth.NewSession, error) {
	var out *auth.NewSession
	var denied error
	err := svc.s.W().TxNamed(ctx, "oidc.login", func(tx *store.Tx) error {
		denied = nil
		ident, err := tx.Q.GetIdentityBySubject(ctx, db.GetIdentityBySubjectParams{ProviderID: r.ID, Subject: cl.Subject})
		var u db.User
		switch {
		case err == nil:
			if u, err = tx.Q.GetUser(ctx, ident.UserID); err != nil {
				return err
			}
			if err := tx.Q.TouchIdentity(ctx, db.TouchIdentityParams{LastLoginAt: nullInt(tx.NowMicros()), Email: nullStr(cl.Email), ID: ident.ID}); err != nil {
				return err
			}
		case errors.Is(err, errNoRows):
			if r.Provisioning != "jit" {
				denied = ErrNotLinked
				return svc.deny(ctx, tx, r, "not_linked")
			}
			if u, denied, err = svc.provision(ctx, tx, r, cl); err != nil || denied != nil {
				if denied != nil {
					return svc.deny(ctx, tx, r, "jit_denied")
				}
				return err
			}
		default:
			return err
		}
		if u.Status != "active" {
			denied = ErrUserInactive
			return svc.deny(ctx, tx, r, "user_suspended")
		}
		s, err := svc.auth.CreateSessionTx(ctx, tx, u, auth.MethodOIDC, r.ID, meta)
		if err != nil {
			return err
		}
		out = s
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: u.ID}, Action: "auth.login",
			AuthMethod: auth.MethodOIDC, SourceIP: meta.IP, Target: audit.Target{Type: "session", ID: s.ID}, Details: map[string]any{"provider": r.Slug}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return nil, denied
	}
	return out, nil
}

func (svc *Service) deny(ctx context.Context, tx *store.Tx, r db.OidcProvider, reason string) error {
	_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser}, Action: "auth.login", Outcome: audit.OutcomeDenied,
		ReasonCode: reason, AuthMethod: auth.MethodOIDC, Details: map[string]any{"provider": r.Slug}})
	return err
}

// provision creates a JIT user: verified email in an allowed domain, provider's default role (never admin/root).
func (svc *Service) provision(ctx context.Context, tx *store.Tx, r db.OidcProvider, cl Claims) (db.User, error, error) {
	if r.RequireVerifiedEmail == 1 && !cl.EmailVerified {
		return db.User{}, ErrEmailUnverify, nil
	}
	var domains []string
	_ = json.Unmarshal([]byte(r.AllowedDomainsJson), &domains)
	_, domain, ok := strings.Cut(cl.Email, "@")
	if !ok || !slices.Contains(domains, strings.ToLower(domain)) {
		return db.User{}, ErrDomainDenied, nil
	}
	base, err := auth.NormalizeUsername(cl.Email)
	if err != nil {
		base = "user"
	}
	name := base
	for i := 2; ; i++ {
		if _, err := tx.Q.GetUserByUsername(ctx, name); errors.Is(err, errNoRows) {
			break
		} else if err != nil {
			return db.User{}, nil, err
		}
		if i > 50 {
			return db.User{}, apperr.New(apperr.CodeConflict, "cannot derive a unique username"), nil
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	display := strings.TrimSpace(cl.Name)
	if display == "" {
		display = name
	}
	if len([]rune(display)) > 120 {
		display = string([]rune(display)[:120])
	}
	uid := ids.New(ids.User)
	if err := tx.Q.InsertUser(ctx, db.InsertUserParams{ID: uid, Username: name, DisplayName: display, Email: nullStr(cl.Email), Role: r.DefaultRole,
		Status: "active", LocalLoginEnabled: 0, CreatedAt: tx.NowMicros(), UpdatedAt: tx.NowMicros()}); err != nil {
		return db.User{}, nil, err
	}
	if err := tx.Q.InsertIdentity(ctx, db.InsertIdentityParams{ID: ids.New(ids.Identity), UserID: uid, ProviderID: r.ID, Subject: cl.Subject,
		Email: nullStr(cl.Email), LinkedAt: tx.NowMicros()}); err != nil {
		return db.User{}, nil, err
	}
	if _, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "user.provisioned", Target: audit.Target{Type: "user", ID: uid},
		Details: map[string]any{"provider": r.Slug, "role": r.DefaultRole, "username": name}}); err != nil {
		return db.User{}, nil, err
	}
	u, err := tx.Q.GetUser(ctx, uid)
	return u, nil, err
}

// Identity is a linked identity as shown to its user and admins.
type Identity struct {
	ID           string     `json:"id"`
	ProviderID   string     `json:"provider_id"`
	ProviderSlug string     `json:"provider_slug"`
	ProviderName string     `json:"provider_name"`
	Email        string     `json:"email,omitempty"`
	LinkedAt     time.Time  `json:"linked_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// Identities lists a user's linked identities.
func (svc *Service) Identities(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := svc.s.R().ListUserIdentities(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		i := Identity{ID: r.ID, ProviderID: r.ProviderID, ProviderSlug: r.ProviderSlug, ProviderName: r.ProviderName, Email: r.Email.String,
			LinkedAt: time.UnixMicro(r.LinkedAt).UTC()}
		if r.LastLoginAt.Valid {
			t := time.UnixMicro(r.LastLoginAt.Int64).UTC()
			i.LastLoginAt = &t
		}
		out = append(out, i)
	}
	return out, nil
}

// Unlink removes an identity from a user, refusing to remove root's last login method.
func (svc *Service) Unlink(ctx context.Context, actor *auth.Principal, userID, identityID string) error {
	return svc.s.W().TxNamed(ctx, "oidc.unlink", func(tx *store.Tx) error {
		n, err := tx.Q.DeleteIdentity(ctx, db.DeleteIdentityParams{ID: identityID, UserID: userID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "identity not found")
		}
		if err := auth.CheckRootLoginTx(ctx, tx); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "identity.unlinked",
			Target: audit.Target{Type: "identity", ID: identityID}, Details: map[string]any{"user_id": userID}})
		return err
	})
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
