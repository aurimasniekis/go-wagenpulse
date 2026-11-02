package auth

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// Session cookie names: __Host- prefixed over https (Secure, Path=/, no Domain), a plain name for http://localhost
// development.
const (
	CookieName         = "__Host-wgp_session"
	InsecureCookieName = "wgp_session"
	sessionPrefix      = "wgp_ses_"
	sessionTouchEvery  = time.Minute
)

// SessionMeta describes the client creating a session.
type SessionMeta struct {
	IP        string
	UserAgent string
}

// NewSession is a freshly issued session (the raw token is returned once and set as a cookie).
type NewSession struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

// createSessionTx issues a session for u inside tx.
func (svc *Service) createSessionTx(ctx context.Context, tx *store.Tx, u db.User, method, providerID string, meta SessionMeta) (*NewSession, error) {
	sys, err := settings.SystemTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	raw, err := randomToken(sessionPrefix)
	if err != nil {
		return nil, err
	}
	mac, err := svc.tokenMAC("session", raw)
	if err != nil {
		return nil, err
	}
	idle := time.Duration(sys.SessionIdleMinutes) * time.Minute
	absolute := time.Duration(sys.SessionAbsoluteHours) * time.Hour
	if method == MethodSetup {
		absolute = time.Hour
	}
	id := ids.New(ids.Session)
	ua := meta.UserAgent
	if len(ua) > 256 {
		ua = ua[:256]
	}
	if err := tx.Q.InsertSession(ctx, db.InsertSessionParams{ID: id, TokenMac: mac, UserID: u.ID, Method: method,
		ProviderID: nullStr(providerID), Ip: nullStr(meta.IP), UserAgent: nullStr(ua), CreatedAt: tx.NowMicros(),
		LastSeenAt: tx.NowMicros(), IdleExpiresAt: tx.Now.Add(idle).UnixMicro(), AbsoluteExpiresAt: tx.Now.Add(absolute).UnixMicro()}); err != nil {
		return nil, err
	}
	if err := tx.Q.TouchUserLogin(ctx, db.TouchUserLoginParams{LastLoginAt: nullInt(tx.NowMicros(), true), ID: u.ID}); err != nil {
		return nil, err
	}
	exp := tx.Now.Add(absolute)
	if i := tx.Now.Add(idle); i.Before(exp) {
		exp = i
	}
	return &NewSession{Token: raw, ID: id, ExpiresAt: exp}, nil
}

// SessionFromRequest returns the raw session token from the request cookie.
func SessionFromRequest(r *http.Request) string {
	for _, name := range []string{CookieName, InsecureCookieName} {
		if c, err := r.Cookie(name); err == nil && strings.HasPrefix(c.Value, sessionPrefix) && len(c.Value) < 128 {
			return c.Value
		}
	}
	return ""
}

// SessionCookie builds the cookie for a session token. maxAge < 0 clears it.
func SessionCookie(secure bool, token string, maxAge int) *http.Cookie {
	name := InsecureCookieName
	if secure {
		name = CookieName
	}
	return &http.Cookie{Name: name, Value: token, //nolint:gosec // Secure follows the public URL scheme (https in production)
		Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// Authenticate validates a session token and returns its principal. Idle expiry slides at most once per minute.
func (svc *Service) Authenticate(ctx context.Context, raw string) (*Principal, error) {
	if raw == "" || !strings.HasPrefix(raw, sessionPrefix) {
		return nil, ErrUnauthenticated
	}
	macs, err := svc.tokenMACs("session", raw)
	if err != nil {
		return nil, err
	}
	var row db.Session
	found := false
	for _, m := range macs {
		if r, err := svc.s.R().GetSessionByMAC(ctx, m); err == nil {
			row, found = r, true
			break
		} else if !isNoRows(err) {
			return nil, err
		}
	}
	now := svc.now()
	if !found || row.RevokedAt.Valid || now.UnixMicro() >= row.IdleExpiresAt || now.UnixMicro() >= row.AbsoluteExpiresAt {
		return nil, ErrUnauthenticated
	}
	u, err := svc.s.R().GetUser(ctx, row.UserID)
	if err != nil {
		if isNoRows(err) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if u.Status != "active" {
		return nil, ErrUnauthenticated
	}
	sys, err := svc.settings.System(ctx)
	if err != nil {
		return nil, err
	}
	// Login-method changes apply to existing sessions immediately.
	if (row.Method == MethodPassword && (!sys.Value.LocalLoginEnabled || u.LocalLoginEnabled == 0)) ||
		(row.Method == MethodOIDC && !sys.Value.OIDCLoginEnabled) {
		return nil, ErrUnauthenticated
	}
	if now.Sub(time.UnixMicro(row.LastSeenAt)) > sessionTouchEvery {
		idle := time.Duration(sys.Value.SessionIdleMinutes) * time.Minute
		_ = svc.s.W().TxNamed(ctx, "auth.session_touch", func(tx *store.Tx) error {
			return tx.Q.TouchSession(ctx, db.TouchSessionParams{LastSeenAt: tx.NowMicros(), IdleExpiresAt: tx.Now.Add(idle).UnixMicro(), ID: row.ID})
		})
	}
	p := principalFor(u, row.Method)
	p.SessionID, p.ProviderID = row.ID, row.ProviderID.String
	if row.Method == MethodSetup {
		// A setup session exists only to finish enrollment.
		p.perms = NewPermSet(PermSystemRead)
	}
	if sys.Value.RequireTOTPForAdmins && (p.Role == RoleRoot || p.Role == RoleAdmin) && row.Method == MethodPassword && !u.TotpEnabledAt.Valid {
		p.MustEnrollTOTP = true
	}
	return p, nil
}

// CSRFToken derives the CSRF token for a session: base64url(HMAC(token key, "csrf|"+session ID)).
func (svc *Service) CSRFToken(p *Principal) string {
	if p == nil || p.SessionID == "" {
		return ""
	}
	m, err := svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/csrf/v1|"+p.SessionID))
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(m)
}

// VerifyCSRF checks a presented CSRF token in constant time against every live key version.
func (svc *Service) VerifyCSRF(p *Principal, presented string) bool {
	if p == nil || p.SessionID == "" || presented == "" {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(presented)
	if err != nil {
		return false
	}
	macs, err := svc.kr.MACAll(keyring.PurposeToken, []byte("wagenpulse/csrf/v1|"+p.SessionID))
	if err != nil {
		return false
	}
	for _, m := range macs {
		if hmac.Equal(m, got) {
			return true
		}
	}
	return false
}

// Logout revokes the principal's session.
func (svc *Service) Logout(ctx context.Context, p *Principal) error {
	if p == nil || p.SessionID == "" {
		return nil
	}
	return svc.s.W().TxNamed(ctx, "auth.logout", func(tx *store.Tx) error {
		if _, err := tx.Q.RevokeSession(ctx, db.RevokeSessionParams{RevokedAt: nullInt(tx.NowMicros(), true), RevokedReason: nullStr("logout"), ID: p.SessionID}); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.AuditActor(), Action: "auth.logout", Target: audit.Target{Type: "session", ID: p.SessionID}})
		return err
	})
}

// Session is the API view of a session (no token material).
type Session struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// ListSessions returns the active sessions of a user.
func (svc *Service) ListSessions(ctx context.Context, userID, currentID string) ([]Session, error) {
	now := svc.now().UnixMicro()
	rows, err := svc.s.R().ListUserSessions(ctx, db.ListUserSessionsParams{UserID: userID, AbsoluteExpiresAt: now, IdleExpiresAt: now})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		exp := min(r.IdleExpiresAt, r.AbsoluteExpiresAt)
		out = append(out, Session{ID: r.ID, Method: r.Method, IP: r.Ip.String, UserAgent: r.UserAgent.String,
			CreatedAt: time.UnixMicro(r.CreatedAt).UTC(), LastSeenAt: time.UnixMicro(r.LastSeenAt).UTC(),
			ExpiresAt: time.UnixMicro(exp).UTC(), Current: r.ID == currentID})
	}
	return out, nil
}

// RevokeSession revokes one session of a user (self-service or admin).
func (svc *Service) RevokeSession(ctx context.Context, actor *Principal, userID, sessionID string) error {
	return svc.s.W().TxNamed(ctx, "auth.revoke_session", func(tx *store.Tx) error {
		rows, err := tx.Q.ListUserSessions(ctx, db.ListUserSessionsParams{UserID: userID, AbsoluteExpiresAt: tx.NowMicros(), IdleExpiresAt: tx.NowMicros()})
		if err != nil {
			return err
		}
		found := false
		for _, r := range rows {
			found = found || r.ID == sessionID
		}
		if !found {
			return apperr.New(apperr.CodeNotFound, "session not found")
		}
		if _, err := tx.Q.RevokeSession(ctx, db.RevokeSessionParams{RevokedAt: nullInt(tx.NowMicros(), true), RevokedReason: nullStr("revoked"), ID: sessionID}); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor.AuditActor(), Action: "auth.session.revoked",
			Target: audit.Target{Type: "session", ID: sessionID}, Details: map[string]any{"user_id": userID}})
		return err
	})
}

// revokeUserSessionsTx revokes every session of a user except exceptID.
func revokeUserSessionsTx(ctx context.Context, tx *store.Tx, userID, exceptID, reason string) error {
	_, err := tx.Q.RevokeUserSessions(ctx, db.RevokeUserSessionsParams{RevokedAt: nullInt(tx.NowMicros(), true), RevokedReason: nullStr(reason),
		UserID: userID, ExceptID: exceptID})
	return err
}

// CreateSessionTx issues a session inside tx (used by the OIDC flow).
func (svc *Service) CreateSessionTx(ctx context.Context, tx *store.Tx, u db.User, method, providerID string, meta SessionMeta) (*NewSession, error) {
	return svc.createSessionTx(ctx, tx, u, method, providerID, meta)
}

// SystemAllowsOIDC reports whether OIDC login is enabled in system settings.
func (svc *Service) SystemAllowsOIDC(ctx context.Context) (bool, error) {
	sys, err := svc.settings.System(ctx)
	return sys.Value.OIDCLoginEnabled, err
}
