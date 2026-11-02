package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/clock"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// Methods describe how a principal authenticated.
const (
	MethodPassword = "password"
	MethodOIDC     = "oidc"
	MethodSetup    = "setup"
	MethodToken    = "token"
)

// Options configure the service.
type Options struct {
	Clock    clock.Clock
	Log      *slog.Logger
	Metrics  *metrics.Metrics
	Params   Params
	SetupTTL time.Duration
	// Throttling of failed logins (hallpass pattern).
	FreeFailures int
	MaxDelay     time.Duration
	Decay        time.Duration
}

// Service implements authentication and user management.
type Service struct {
	s        *store.Store
	kr       *keyring.Keyring
	audit    *audit.Writer
	settings *settings.Service
	opts     Options
	log      *slog.Logger

	usedMu sync.Mutex
	used   map[string]tokenUse // API token last-used buffer (ADR-0014)
}

type tokenUse struct {
	at time.Time
	ip string
}

// New builds the service and installs the root-lockout guard on system settings.
func New(s *store.Store, kr *keyring.Keyring, a *audit.Writer, st *settings.Service, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Log == nil {
		opts.Log = obs.Discard()
	}
	if opts.Params == (Params{}) {
		opts.Params = DefaultParams
	}
	if opts.SetupTTL <= 0 {
		opts.SetupTTL = 30 * time.Minute
	}
	if opts.FreeFailures <= 0 {
		opts.FreeFailures = 3
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 15 * time.Minute
	}
	if opts.Decay <= 0 {
		opts.Decay = 24 * time.Hour
	}
	svc := &Service{s: s, kr: kr, audit: a, settings: st, opts: opts, log: obs.Component(opts.Log, "auth"), used: map[string]tokenUse{}}
	st.LockoutGuard = svc.guardSettings
	return svc
}

// Settings returns the settings service.
func (svc *Service) Settings() *settings.Service { return svc.settings }

func (svc *Service) now() time.Time { return svc.opts.Clock.Now() }

// randomToken returns prefix + 43 base64url characters (256 bits).
func randomToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenMAC returns HMAC(token key, domain|raw) under the active key version.
func (svc *Service) tokenMAC(domain, raw string) ([]byte, error) {
	return svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/"+domain+"/v1|"+raw))
}

// tokenMACs returns the MAC under every live key version (lookups survive token-key rotation).
func (svc *Service) tokenMACs(domain, raw string) ([][]byte, error) {
	return svc.kr.MACAll(keyring.PurposeToken, []byte("wagenpulse/"+domain+"/v1|"+raw))
}

// User is the API representation of a user (never includes credentials).
type User struct {
	ID                string     `json:"id"`
	Username          string     `json:"username"`
	DisplayName       string     `json:"display_name"`
	Email             string     `json:"email,omitempty"`
	Role              Role       `json:"role"`
	Status            string     `json:"status"`
	HasPassword       bool       `json:"has_password"`
	LocalLoginEnabled bool       `json:"local_login_enabled"`
	TOTPEnabled       bool       `json:"totp_enabled"`
	LastLoginAt       *time.Time `json:"last_login_at,omitempty"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ToUser converts a row.
func ToUser(u db.User) User {
	out := User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email.String, Role: Role(u.Role), Status: u.Status,
		HasPassword: u.PasswordHash.Valid && u.PasswordHash.String != "", LocalLoginEnabled: u.LocalLoginEnabled == 1,
		TOTPEnabled: u.TotpEnabledAt.Valid, Revision: u.Revision, CreatedAt: time.UnixMicro(u.CreatedAt).UTC(),
		UpdatedAt: time.UnixMicro(u.UpdatedAt).UTC()}
	if u.LastLoginAt.Valid {
		t := time.UnixMicro(u.LastLoginAt.Int64).UTC()
		out.LastLoginAt = &t
	}
	return out
}

// Principal is an authenticated caller.
type Principal struct {
	UserID      string
	Username    string
	DisplayName string
	Role        Role
	Method      string // password, oidc, setup, token
	SessionID   string // for sessions
	ProviderID  string // for OIDC sessions
	Token       *TokenGrant
	perms       PermSet
	// MustEnrollTOTP is set when settings require TOTP and the user has none; only enrollment and logout are allowed.
	MustEnrollTOTP bool
}

// TokenGrant describes the token behind a token-authenticated principal.
type TokenGrant struct {
	ID           string
	Name         string
	Scopes       PermSet
	VehicleIDs   []string // empty = all vehicles
	NeverExpires bool
	ExpiresAt    *time.Time
}

// Can reports whether the principal holds p.
func (p *Principal) Can(perm Permission) bool { return p != nil && p.perms.Has(perm) }

// Permissions returns the effective permissions.
func (p *Principal) Permissions() []Permission { return p.perms.Sorted() }

// IsRoot reports whether the principal is the root user authenticated interactively (a token never counts as root
// for root-only actions, ADR-0014).
func (p *Principal) IsRoot() bool { return p != nil && p.Role == RoleRoot && p.Token == nil }

// VehicleAllowed reports whether a token restriction permits access to vehicleID.
func (p *Principal) VehicleAllowed(vehicleID string) bool {
	if p == nil {
		return false
	}
	if p.Token == nil || len(p.Token.VehicleIDs) == 0 {
		return true
	}
	for _, v := range p.Token.VehicleIDs {
		if v == vehicleID {
			return true
		}
	}
	return false
}

// VehicleRestricted reports whether the principal may only see some vehicles.
func (p *Principal) VehicleRestricted() bool {
	return p != nil && p.Token != nil && len(p.Token.VehicleIDs) > 0
}

// AuditActor returns the audit actor of the principal.
func (p *Principal) AuditActor() audit.Actor {
	if p == nil {
		return audit.Actor{Type: audit.ActorSystem}
	}
	if p.Token != nil {
		return audit.Actor{Type: audit.ActorToken, ID: p.Token.ID}
	}
	return audit.Actor{Type: audit.ActorUser, ID: p.UserID}
}

func principalFor(u db.User, method string) *Principal {
	return &Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: Role(u.Role), Method: method,
		perms: NewPermSet(RolePermissions(Role(u.Role))...)}
}

// ErrInvalidCredentials is the uniform login failure.
var ErrInvalidCredentials = apperr.New(apperr.CodeUnauthorized, "invalid username or password")

// ErrUnauthenticated means no valid credential was presented.
var ErrUnauthenticated = apperr.New(apperr.CodeUnauthorized, "authentication required")

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(v int64, ok bool) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: ok} }

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// NormalizeUsername lowercases and validates a username: 1..64 of [a-z0-9._-@].
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 64 {
		return "", apperr.New(apperr.CodeInvalid, "username must be 1..64 characters")
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' && r != '@' {
			return "", apperr.New(apperr.CodeInvalid, "username may contain only a-z, 0-9, '.', '_', '-' and '@'")
		}
	}
	return s, nil
}
