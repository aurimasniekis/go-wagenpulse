// Package settings owns the runtime settings documents: system settings (login methods, session lifetimes, ingestion
// defaults) and retention settings (ADR-0004, ADR-0024). Both are singletons stored as JSON in the settings table with
// a revision used as the API's ETag.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/store/db"
)

// Keys of the settings documents.
const (
	KeySystem    = "system"
	KeyRetention = "retention"
)

// Event policies for imports (ADR-0017).
const (
	EventPolicyNone    = "none"
	EventPolicyPreview = "preview"
	EventPolicyPublish = "publish"
)

// ValidEventPolicy reports whether p is a known import event policy.
func ValidEventPolicy(p string) bool {
	return p == EventPolicyNone || p == EventPolicyPreview || p == EventPolicyPublish
}

// System are installation-wide runtime settings.
type System struct {
	// LocalLoginEnabled allows username/password login for every user whose own local login is enabled.
	LocalLoginEnabled bool `json:"local_login_enabled"`
	// OIDCLoginEnabled allows sign-in through enabled OIDC providers.
	OIDCLoginEnabled bool `json:"oidc_login_enabled"`
	// RequireTOTPForAdmins forces root and admins with local passwords to enroll TOTP before using the API.
	RequireTOTPForAdmins bool `json:"require_totp_for_admins"`
	// SessionIdleMinutes and SessionAbsoluteHours bound browser sessions (capped by the bootstrap configuration).
	SessionIdleMinutes   int `json:"session_idle_minutes"`
	SessionAbsoluteHours int `json:"session_absolute_hours"`
	// TokenDefaultTTLDays and TokenMaxTTLDays bound expiring API tokens.
	TokenDefaultTTLDays int `json:"token_default_ttl_days"`
	TokenMaxTTLDays     int `json:"token_max_ttl_days"`
	// UploadEventPolicy and HistoricalEventPolicy are the defaults for imports (ADR-0017).
	UploadEventPolicy     string `json:"upload_event_policy"`
	HistoricalEventPolicy string `json:"historical_event_policy"`
	// StaleAfterMinutes marks a latest value as stale in views (per-signal overrides may follow).
	StaleAfterMinutes int `json:"stale_after_minutes"`
}

// DefaultSystem returns the defaults.
func DefaultSystem() System {
	return System{
		LocalLoginEnabled: true, OIDCLoginEnabled: true, SessionIdleMinutes: 12 * 60, SessionAbsoluteHours: 7 * 24,
		TokenDefaultTTLDays: 90, TokenMaxTTLDays: 366, UploadEventPolicy: EventPolicyNone, HistoricalEventPolicy: EventPolicyNone,
		StaleAfterMinutes: 120,
	}
}

// Validate checks bounds. maxIdle/maxAbsolute come from the bootstrap configuration.
func (s System) Validate(maxIdle, maxAbsolute time.Duration) error {
	v := &apperr.Validation{}
	if !s.LocalLoginEnabled && !s.OIDCLoginEnabled {
		v.Add("local_login_enabled", "at least one login method must stay enabled")
	}
	if s.SessionIdleMinutes < 5 || time.Duration(s.SessionIdleMinutes)*time.Minute > maxIdle {
		v.Add("session_idle_minutes", "must be within 5..%d", int(maxIdle.Minutes()))
	}
	if s.SessionAbsoluteHours < 1 || time.Duration(s.SessionAbsoluteHours)*time.Hour > maxAbsolute ||
		time.Duration(s.SessionAbsoluteHours)*time.Hour < time.Duration(s.SessionIdleMinutes)*time.Minute {
		v.Add("session_absolute_hours", "must be within 1..%d and not shorter than the idle timeout", int(maxAbsolute.Hours()))
	}
	if s.TokenMaxTTLDays < 1 || s.TokenMaxTTLDays > 366 {
		v.Add("token_max_ttl_days", "must be within 1..366")
	}
	if s.TokenDefaultTTLDays < 1 || s.TokenDefaultTTLDays > s.TokenMaxTTLDays {
		v.Add("token_default_ttl_days", "must be within 1..token_max_ttl_days")
	}
	if !ValidEventPolicy(s.UploadEventPolicy) {
		v.Add("upload_event_policy", "must be none, preview or publish")
	}
	if !ValidEventPolicy(s.HistoricalEventPolicy) {
		v.Add("historical_event_policy", "must be none, preview or publish")
	}
	if s.StaleAfterMinutes < 1 || s.StaleAfterMinutes > 60*24*30 {
		v.Add("stale_after_minutes", "must be within 1..43200")
	}
	return v.Err()
}

// Retention controls how long data is kept (ADR-0024). Zero days means "keep forever" where allowed.
type Retention struct {
	ObservationsDays  int `json:"observations_days"`
	RawDeliveriesDays int `json:"raw_deliveries_days"`
	EventsDays        int `json:"events_days"`
	AuditDays         int `json:"audit_days"`
	FinishedJobsDays  int `json:"finished_jobs_days"`
}

// DefaultRetention returns the defaults.
func DefaultRetention() Retention {
	return Retention{ObservationsDays: 730, RawDeliveriesDays: 90, EventsDays: 180, AuditDays: 730, FinishedJobsDays: 90}
}

// Validate checks bounds.
func (r Retention) Validate() error {
	v := &apperr.Validation{}
	if r.ObservationsDays != 0 && (r.ObservationsDays < 7 || r.ObservationsDays > 36500) {
		v.Add("observations_days", "must be 0 (forever) or within 7..36500")
	}
	if r.RawDeliveriesDays != 0 && (r.RawDeliveriesDays < 1 || r.RawDeliveriesDays > 36500) {
		v.Add("raw_deliveries_days", "must be 0 (forever) or within 1..36500")
	}
	if r.EventsDays != 0 && (r.EventsDays < 7 || r.EventsDays > 36500) {
		v.Add("events_days", "must be 0 (forever) or within 7..36500")
	}
	if r.AuditDays != 0 && (r.AuditDays < 90 || r.AuditDays > 36500) {
		v.Add("audit_days", "must be 0 (forever) or within 90..36500")
	}
	if r.FinishedJobsDays < 1 || r.FinishedJobsDays > 3650 {
		v.Add("finished_jobs_days", "must be within 1..3650")
	}
	return v.Err()
}

// Doc is a settings document with its revision.
type Doc[T any] struct {
	Value     T
	Revision  int64
	UpdatedAt time.Time
	UpdatedBy string
}

// Service reads and writes settings.
type Service struct {
	s           *store.Store
	audit       *audit.Writer
	maxIdle     time.Duration
	maxAbsolute time.Duration
	// LockoutGuard, if set, is called inside the update transaction for system settings and may refuse a change that
	// would lock root out (set by the auth package to avoid an import cycle).
	LockoutGuard func(ctx context.Context, tx *store.Tx, next System) error
}

// New returns a settings service.
func New(s *store.Store, a *audit.Writer, maxIdle, maxAbsolute time.Duration) *Service {
	return &Service{s: s, audit: a, maxIdle: maxIdle, maxAbsolute: maxAbsolute}
}

func read[T any](ctx context.Context, q *db.Queries, key string, def T) (Doc[T], error) {
	row, err := q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return Doc[T]{Value: def, Revision: 1}, nil
	}
	if err != nil {
		return Doc[T]{}, err
	}
	v := def // unknown/missing fields keep defaults (forward compatibility)
	if err := json.Unmarshal([]byte(row.Value), &v); err != nil {
		return Doc[T]{}, err
	}
	return Doc[T]{Value: v, Revision: row.Revision, UpdatedAt: time.UnixMicro(row.UpdatedAt), UpdatedBy: row.UpdatedBy.String}, nil
}

// System returns the current system settings.
func (svc *Service) System(ctx context.Context) (Doc[System], error) {
	return read(ctx, svc.s.R(), KeySystem, DefaultSystem())
}

// SystemTx reads system settings inside a write transaction.
func SystemTx(ctx context.Context, tx *store.Tx) (System, error) {
	d, err := read(ctx, tx.Q, KeySystem, DefaultSystem())
	return d.Value, err
}

// Retention returns the current retention settings.
func (svc *Service) Retention(ctx context.Context) (Doc[Retention], error) {
	return read(ctx, svc.s.R(), KeyRetention, DefaultRetention())
}

// ErrRevision is returned when If-Match does not match the stored revision.
var ErrRevision = apperr.New(apperr.CodePrecondition, "the settings were changed by someone else; reload and retry")

func write[T any](ctx context.Context, tx *store.Tx, key string, def T, ifRevision int64, v T, actor audit.Actor) (Doc[T], error) {
	cur, err := read(ctx, tx.Q, key, def)
	if err != nil {
		return Doc[T]{}, err
	}
	if ifRevision != 0 && ifRevision != cur.Revision {
		return Doc[T]{}, ErrRevision
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Doc[T]{}, err
	}
	if err := tx.Q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: string(b), UpdatedAt: tx.NowMicros(),
		UpdatedBy: sql.NullString{String: actor.ID, Valid: actor.ID != ""}}); err != nil {
		return Doc[T]{}, err
	}
	return read(ctx, tx.Q, key, def)
}

// UpdateSystem replaces the system settings (ifRevision 0 = unconditional).
func (svc *Service) UpdateSystem(ctx context.Context, actor audit.Actor, ifRevision int64, v System) (Doc[System], error) {
	if err := v.Validate(svc.maxIdle, svc.maxAbsolute); err != nil {
		return Doc[System]{}, err
	}
	var out Doc[System]
	err := svc.s.W().TxNamed(ctx, "settings.system", func(tx *store.Tx) error {
		if svc.LockoutGuard != nil {
			if err := svc.LockoutGuard(ctx, tx, v); err != nil {
				return err
			}
		}
		var err error
		if out, err = write(ctx, tx, KeySystem, DefaultSystem(), ifRevision, v, actor); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "settings.system.updated",
			Target: audit.Target{Type: "settings", ID: KeySystem}, Details: map[string]any{
				"local_login_enabled": v.LocalLoginEnabled, "oidc_login_enabled": v.OIDCLoginEnabled,
				"require_totp_for_admins": v.RequireTOTPForAdmins, "revision": out.Revision,
			}})
		return err
	})
	return out, err
}

// UpdateRetention replaces the retention settings.
func (svc *Service) UpdateRetention(ctx context.Context, actor audit.Actor, ifRevision int64, v Retention) (Doc[Retention], error) {
	if err := v.Validate(); err != nil {
		return Doc[Retention]{}, err
	}
	var out Doc[Retention]
	err := svc.s.W().TxNamed(ctx, "settings.retention", func(tx *store.Tx) error {
		var err error
		if out, err = write(ctx, tx, KeyRetention, DefaultRetention(), ifRevision, v, actor); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "settings.retention.updated",
			Target: audit.Target{Type: "settings", ID: KeyRetention}, Details: map[string]any{
				"observations_days": v.ObservationsDays, "raw_deliveries_days": v.RawDeliveriesDays, "events_days": v.EventsDays,
				"audit_days": v.AuditDays, "finished_jobs_days": v.FinishedJobsDays, "revision": out.Revision,
			}})
		return err
	})
	return out, err
}

// ResetSystem restores defaults (Terraform destroy of the singleton).
func (svc *Service) ResetSystem(ctx context.Context, actor audit.Actor) (Doc[System], error) {
	return svc.UpdateSystem(ctx, actor, 0, DefaultSystem())
}

// ResetRetention restores defaults.
func (svc *Service) ResetRetention(ctx context.Context, actor audit.Actor) (Doc[Retention], error) {
	return svc.UpdateRetention(ctx, actor, 0, DefaultRetention())
}
