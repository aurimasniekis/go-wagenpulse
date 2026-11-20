// Package sources manages data-source configuration (credentials, collection schedule, request types, event
// policies, bound vehicles), its runtime authorization state, and the scheduled collector for the unofficial EU Data
// Act portal interface (ADR-0005).
package sources

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/providers/vwdataact"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/settings"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Kinds and auth modes.
const (
	KindPortal        = "vw_data_act_portal"
	AuthSessionCookie = "session_cookie"
	AuthPassword      = "password"
)

// Source is the API representation. Credentials are never returned; fingerprints change when they do.
type Source struct {
	ID                    string        `json:"id"`
	Name                  string        `json:"name"`
	Kind                  string        `json:"kind" enum:"vw_data_act_portal"`
	Interface             string        `json:"interface" doc:"Always 'unofficial': the portal's web backend is not a documented API (ADR-0005)."`
	Enabled               bool          `json:"enabled"`
	Brand                 string        `json:"brand"`
	PortalBrandKey        string        `json:"portal_brand_key,omitempty"`
	Country               string        `json:"country"`
	Language              string        `json:"language"`
	AuthMode              string        `json:"auth_mode" enum:"session_cookie,password"`
	UsernameSet           bool          `json:"username_set"`
	SecretSet             bool          `json:"secret_set"`
	SecretRef             string        `json:"secret_ref,omitempty"`
	SecretFingerprint     string        `json:"secret_fingerprint,omitempty"`
	CollectContinuous     bool          `json:"collect_continuous"`
	CollectHistorical     bool          `json:"collect_historical"`
	IntervalMinutes       int           `json:"interval_minutes"`
	LiveEventPolicy       string        `json:"live_event_policy" enum:"none,preview,publish"`
	HistoricalEventPolicy string        `json:"historical_event_policy" enum:"none,preview,publish"`
	VehicleIDs            []string      `json:"vehicle_ids"`
	Revision              int64         `json:"revision"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
	Status                *SourceStatus `json:"status,omitempty"`
}

// Status is the runtime authorization and collection state of a source.
type SourceStatus struct {
	State               string     `json:"state" enum:"configured,authorized,reauthorization_required,no_active_request,rate_limited,unavailable,unsupported,disabled,error"`
	Reason              string     `json:"reason,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastDeliveryAt      *time.Time `json:"last_delivery_at,omitempty"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	RetryAfter          *time.Time `json:"retry_after,omitempty"`
	ConsecutiveFailures int64      `json:"consecutive_failures"`
	LastErrorClass      string     `json:"last_error_class,omitempty"`
	CorrelationID       string     `json:"correlation_id,omitempty" doc:"Correlation ID of the last collection run; the server log lines of that run carry it."`
	LagSeconds          *int64     `json:"lag_seconds,omitempty" doc:"Seconds since the last successful collection."`
}

// Input creates or replaces a source. Credentials: Username/Secret are write-only (nil = keep); SecretRef replaces a
// stored secret with an external reference.
type Input struct {
	Name                  string
	Enabled               bool
	Brand                 string
	PortalBrandKey        string
	Country               string
	Language              string
	AuthMode              string
	Username              *string
	Secret                *string
	SecretRef             string
	CollectContinuous     bool
	CollectHistorical     bool
	IntervalMinutes       int
	LiveEventPolicy       string
	HistoricalEventPolicy string
	VehicleIDs            []string
}

// Service manages sources.
type Service struct {
	s     *store.Store
	kr    *keyring.Keyring
	audit *audit.Writer
	refs  *secretref.Resolver
	now   func() time.Time
	// Wake is called after configuration changes so the scheduler re-evaluates.
	Wake func()
}

// New returns a service.
func New(s *store.Store, kr *keyring.Keyring, a *audit.Writer, refs *secretref.Resolver, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{s: s, kr: kr, audit: a, refs: refs, now: now}
}

var (
	countryRE = regexp.MustCompile(`^[a-z]{2}$`)
	langRE    = regexp.MustCompile(`^[a-z]{2}$`)
	brandKey  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

func (in *Input) validate(refs *secretref.Resolver, creating bool) error {
	v := &apperr.Validation{}
	in.Name = strings.TrimSpace(in.Name)
	if l := len([]rune(in.Name)); l == 0 || l > 100 {
		v.Add("name", "must be 1..100 characters")
	}
	if _, ok := vwdataact.Brands[in.Brand]; !ok {
		v.Add("brand", "must be one of volkswagen, volkswagen_commercial, audi, skoda, seat, cupra, bentley")
	}
	in.Country, in.Language = strings.ToLower(strings.TrimSpace(in.Country)), strings.ToLower(strings.TrimSpace(in.Language))
	if !countryRE.MatchString(in.Country) {
		v.Add("country", "must be a two-letter country code (e.g. de)")
	}
	if !langRE.MatchString(in.Language) {
		v.Add("language", "must be a two-letter language code (e.g. en)")
	}
	if in.PortalBrandKey != "" && !brandKey.MatchString(in.PortalBrandKey) {
		v.Add("portal_brand_key", "must be an uppercase identifier such as VOLKSWAGEN_PASSENGER_CARS")
	}
	switch in.AuthMode {
	case AuthSessionCookie, AuthPassword:
	default:
		v.Add("auth_mode", "must be session_cookie or password")
	}
	if in.AuthMode == AuthPassword && in.PortalBrandKey == "" && vwdataact.Brands[in.Brand].StateKey == "" {
		v.Add("portal_brand_key", "is required for password login with brand %q (copy the key from the portal's login URL)", in.Brand)
	}
	if in.Secret != nil && *in.Secret != "" && in.SecretRef != "" {
		v.Add("secret_ref", "set either secret or secret_ref, not both")
	}
	if in.Secret != nil && len(*in.Secret) > 16<<10 {
		v.Add("secret", "must be at most 16 KiB")
	}
	if in.SecretRef != "" {
		if err := refs.Validate(in.SecretRef); err != nil {
			v.Add("secret_ref", "%s", err.Error())
		}
	}
	if in.Username != nil && len(*in.Username) > 254 {
		v.Add("username", "must be at most 254 characters")
	}
	if creating && in.AuthMode == AuthPassword && (in.Username == nil || *in.Username == "") {
		v.Add("username", "is required for password login")
	}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 15
	}
	if in.IntervalMinutes < 15 || in.IntervalMinutes > 1440 {
		v.Add("interval_minutes", "must be within 15..1440 (the portal delivers at most every 15 minutes)")
	}
	if in.LiveEventPolicy == "" {
		in.LiveEventPolicy = settings.EventPolicyPublish
	}
	if in.HistoricalEventPolicy == "" {
		in.HistoricalEventPolicy = settings.EventPolicyNone
	}
	if !settings.ValidEventPolicy(in.LiveEventPolicy) {
		v.Add("live_event_policy", "must be none, preview or publish")
	}
	if !settings.ValidEventPolicy(in.HistoricalEventPolicy) {
		v.Add("historical_event_policy", "must be none, preview or publish")
	}
	if !in.CollectContinuous && !in.CollectHistorical && in.Enabled {
		v.Add("collect_continuous", "an enabled source must collect continuous and/or historical deliveries")
	}
	slices.Sort(in.VehicleIDs)
	in.VehicleIDs = slices.Compact(in.VehicleIDs)
	for _, id := range in.VehicleIDs {
		if !ids.Valid(ids.Vehicle, id) {
			v.Add("vehicle_ids", "invalid vehicle id %q", id)
		}
	}
	if len(in.VehicleIDs) > 100 {
		v.Add("vehicle_ids", "at most 100 vehicles per source")
	}
	return v.Err()
}

const cols = `id, name, kind, enabled, brand, country, language, auth_mode, username_enc IS NOT NULL, secret_enc IS NOT NULL OR secret_ref IS NOT NULL,
	coalesce(secret_ref, ''), secret_fingerprint, collect_continuous, collect_historical, interval_minutes, live_event_policy,
	historical_event_policy, revision, created_at, updated_at, portal_brand_key`

func scanSource(r interface{ Scan(...any) error }) (Source, error) {
	var s Source
	var enabled, uset, sset, cc, ch int64
	var created, updated int64
	if err := r.Scan(&s.ID, &s.Name, &s.Kind, &enabled, &s.Brand, &s.Country, &s.Language, &s.AuthMode, &uset, &sset, &s.SecretRef,
		&s.SecretFingerprint, &cc, &ch, &s.IntervalMinutes, &s.LiveEventPolicy, &s.HistoricalEventPolicy, &s.Revision, &created, &updated,
		&s.PortalBrandKey); err != nil {
		return s, err
	}
	s.Enabled, s.UsernameSet, s.SecretSet, s.CollectContinuous, s.CollectHistorical = enabled == 1, uset == 1, sset == 1, cc == 1, ch == 1
	s.CreatedAt, s.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	s.Interface = "unofficial"
	return s, nil
}

// ErrNotFound is returned for unknown sources.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "source not found")

// Get returns a source with its vehicles and status.
func (svc *Service) Get(ctx context.Context, id string) (Source, error) {
	s, err := scanSource(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+cols+` FROM sources WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	return s, svc.decorate(ctx, &s)
}

func (svc *Service) decorate(ctx context.Context, s *Source) error {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT v.id FROM source_vehicles sv JOIN vehicles v ON v.num = sv.vehicle_num WHERE sv.source_id = ? ORDER BY v.id`, s.ID)
	if err != nil {
		return err
	}
	s.VehicleIDs = []string{}
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			s.VehicleIDs = append(s.VehicleIDs, id)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	st, err := svc.status(ctx, s.ID)
	if err != nil {
		return err
	}
	s.Status = st
	return nil
}

func (svc *Service) status(ctx context.Context, id string) (*SourceStatus, error) {
	var st SourceStatus
	var attempt, success, delivery, next, retry sql.NullInt64
	err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT state, state_reason, last_attempt_at, last_success_at, last_delivery_at, next_run_at, retry_after,
		consecutive_failures, last_error_class, last_correlation_id FROM source_status WHERE source_id = ?`, id).Scan(&st.State, &st.Reason, &attempt, &success, &delivery,
		&next, &retry, &st.ConsecutiveFailures, &st.LastErrorClass, &st.CorrelationID)
	if errors.Is(err, sql.ErrNoRows) {
		return &SourceStatus{State: "configured"}, nil
	}
	if err != nil {
		return nil, err
	}
	st.LastAttemptAt, st.LastSuccessAt, st.LastDeliveryAt, st.NextRunAt, st.RetryAfter = tp(attempt), tp(success), tp(delivery), tp(next), tp(retry)
	if st.LastSuccessAt != nil {
		lag := int64(svc.now().Sub(*st.LastSuccessAt).Seconds())
		st.LagSeconds = &lag
	}
	return &st, nil
}

func tp(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMicro(v.Int64).UTC()
	return &t
}

// List returns every source.
func (svc *Service) List(ctx context.Context) ([]Source, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT `+cols+` FROM sources ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	var out []Source
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			s, err := scanSource(rows)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	}(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := svc.decorate(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []Source{}
	}
	return out, nil
}

func (svc *Service) seal(id, column string, v string) ([]byte, error) {
	return svc.kr.Seal(keyring.PurposeField, keyring.FieldAAD("sources", column, id), []byte(v))
}

func (svc *Service) open(id, column string, v []byte) (string, error) {
	if len(v) == 0 {
		return "", nil
	}
	b, err := svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("sources", column, id), v)
	return string(b), err
}

func (svc *Service) fingerprint(secret string) string {
	k, err := svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/fingerprint/v1"))
	if err != nil {
		return ""
	}
	return secretref.Fingerprint(k, []byte(secret))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (svc *Service) bindVehicles(ctx context.Context, tx *store.Tx, id string, vehicleIDs []string) error {
	if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM source_vehicles WHERE source_id = ?`, id); err != nil {
		return err
	}
	for _, vid := range vehicleIDs {
		var num int64
		var archived sql.NullInt64
		err := tx.SQL.QueryRowContext(ctx, `SELECT num, archived_at FROM vehicles WHERE id = ?`, vid).Scan(&num, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.New(apperr.CodeInvalid, "vehicle_ids: unknown vehicle %s", vid)
		}
		if err != nil {
			return err
		}
		if archived.Valid {
			return apperr.New(apperr.CodeInvalid, "vehicle_ids: vehicle %s is archived", vid)
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO source_vehicles (source_id, vehicle_num) VALUES (?, ?)`, id, num); err != nil {
			return err
		}
	}
	return nil
}

// Create adds a source.
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in Input) (Source, error) {
	if err := in.validate(svc.refs, true); err != nil {
		return Source{}, err
	}
	id := ids.New(ids.Source)
	var userEnc, secEnc []byte
	var fp string
	var err error
	if in.Username != nil && *in.Username != "" {
		if userEnc, err = svc.seal(id, "username_enc", strings.TrimSpace(*in.Username)); err != nil {
			return Source{}, err
		}
	}
	if in.Secret != nil && *in.Secret != "" {
		if secEnc, err = svc.seal(id, "secret_enc", *in.Secret); err != nil {
			return Source{}, err
		}
		fp = svc.fingerprint(*in.Secret)
	}
	if in.SecretRef != "" {
		fp = "ref"
	}
	err = svc.s.W().TxNamed(ctx, "sources.create", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO sources (id, name, kind, enabled, brand, portal_brand_key, country, language, auth_mode,
			username_enc, secret_enc, secret_ref, secret_fingerprint, collect_continuous, collect_historical, interval_minutes, live_event_policy,
			historical_event_policy, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.Name, KindPortal, b2i(in.Enabled), in.Brand, in.PortalBrandKey, in.Country, in.Language, in.AuthMode, userEnc, secEnc,
			nullStr(in.SecretRef), fp, b2i(in.CollectContinuous), b2i(in.CollectHistorical), in.IntervalMinutes, in.LiveEventPolicy,
			in.HistoricalEventPolicy, tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
		if err := svc.bindVehicles(ctx, tx, id, in.VehicleIDs); err != nil {
			return err
		}
		if err := setStatus(ctx, tx, id, initialState(in), "", nil); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "source.created", Target: audit.Target{Type: "source", ID: id},
			Details: map[string]any{"name": in.Name, "brand": in.Brand, "auth_mode": in.AuthMode, "vehicles": len(in.VehicleIDs),
				"credentials_set": secEnc != nil || in.SecretRef != ""}})
		return err
	})
	if err != nil {
		return Source{}, err
	}
	svc.wake()
	return svc.Get(ctx, id)
}

func initialState(in Input) string {
	if !in.Enabled {
		return "disabled"
	}
	return "configured"
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (svc *Service) wake() {
	if svc.Wake != nil {
		svc.Wake()
	}
}

// Update replaces a source's configuration. Changing credentials clears the stored portal session.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, id string, ifRevision int64, in Input) (Source, error) {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	if err := in.validate(svc.refs, false); err != nil {
		return Source{}, err
	}
	var userEnc, secEnc []byte
	var curUser, curSec []byte
	var curRef sql.NullString
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT username_enc, secret_enc, secret_ref FROM sources WHERE id = ?`, id).Scan(&curUser, &curSec, &curRef); err != nil {
		return Source{}, err
	}
	userEnc, secEnc = curUser, curSec
	fp, ref := cur.SecretFingerprint, curRef.String
	credsChanged := false
	if in.Username != nil {
		credsChanged = true
		userEnc = nil
		if *in.Username != "" {
			if userEnc, err = svc.seal(id, "username_enc", strings.TrimSpace(*in.Username)); err != nil {
				return Source{}, err
			}
		}
	}
	switch {
	case in.SecretRef != "":
		credsChanged = credsChanged || in.SecretRef != ref
		secEnc, ref, fp = nil, in.SecretRef, "ref"
	case in.Secret != nil:
		credsChanged = true
		secEnc, ref, fp = nil, "", ""
		if *in.Secret != "" {
			if secEnc, err = svc.seal(id, "secret_enc", *in.Secret); err != nil {
				return Source{}, err
			}
			fp = svc.fingerprint(*in.Secret)
		}
	case ref != "":
		// No secret fields sent and the source used a reference: keep it only when the caller still sends it is the
		// documented semantics (secret_ref is a plain configuration field); an omitted reference clears it.
		credsChanged = true
		ref, fp = "", ""
	}
	if in.AuthMode != cur.AuthMode {
		credsChanged = true
	}
	if in.AuthMode == AuthPassword && len(userEnc) == 0 {
		return Source{}, apperr.New(apperr.CodeInvalid, "username: is required for password login")
	}
	err = svc.s.W().TxNamed(ctx, "sources.update", func(tx *store.Tx) error {
		rev := cur.Revision
		if ifRevision != 0 {
			rev = ifRevision
		}
		q := `UPDATE sources SET name = ?, enabled = ?, brand = ?, portal_brand_key = ?, country = ?, language = ?, auth_mode = ?, username_enc = ?,
			secret_enc = ?, secret_ref = ?, secret_fingerprint = ?, collect_continuous = ?, collect_historical = ?, interval_minutes = ?,
			live_event_policy = ?, historical_event_policy = ?, revision = revision + 1, updated_at = ?`
		args := []any{in.Name, b2i(in.Enabled), in.Brand, in.PortalBrandKey, in.Country, in.Language, in.AuthMode, userEnc, secEnc, nullStr(ref), fp,
			b2i(in.CollectContinuous), b2i(in.CollectHistorical), in.IntervalMinutes, in.LiveEventPolicy, in.HistoricalEventPolicy, tx.NowMicros()}
		if credsChanged {
			q += `, session_enc = NULL`
		}
		q += ` WHERE id = ? AND revision = ?`
		args = append(args, id, rev)
		r, err := tx.SQL.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return apperr.New(apperr.CodePrecondition, "the source was changed by someone else; reload and retry")
		}
		if err := svc.bindVehicles(ctx, tx, id, in.VehicleIDs); err != nil {
			return err
		}
		switch {
		case !in.Enabled:
			if err := setStatus(ctx, tx, id, "disabled", "the source is disabled", nil); err != nil {
				return err
			}
		case credsChanged || !cur.Enabled:
			if err := setStatus(ctx, tx, id, "configured", "", nil); err != nil {
				return err
			}
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "source.updated", Target: audit.Target{Type: "source", ID: id},
			Details: map[string]any{"enabled": in.Enabled, "auth_mode": in.AuthMode, "credentials_changed": credsChanged, "vehicles": len(in.VehicleIDs)}})
		return err
	})
	if err != nil {
		return Source{}, err
	}
	svc.wake()
	return svc.Get(ctx, id)
}

// Delete removes a source and its stored credentials; imported data stays (ADR-0023).
func (svc *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	if _, err := svc.Get(ctx, id); err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "sources.delete", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "source.deleted", Target: audit.Target{Type: "source", ID: id}})
		return err
	})
}

// setStatus writes the runtime state.
func setStatus(ctx context.Context, tx *store.Tx, id, state, reason string, next *time.Time) error {
	var n any
	if next != nil {
		n = next.UnixMicro()
	}
	_, err := tx.SQL.ExecContext(ctx, `INSERT INTO source_status (source_id, state, state_reason, next_run_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET state = excluded.state, state_reason = excluded.state_reason,
		  next_run_at = coalesce(excluded.next_run_at, CASE WHEN excluded.state = 'configured' THEN NULL ELSE source_status.next_run_at END),
		  retry_after = CASE WHEN excluded.state = 'configured' THEN NULL ELSE source_status.retry_after END,
		  consecutive_failures = CASE WHEN excluded.state = 'configured' THEN 0 ELSE source_status.consecutive_failures END,
		  last_error_class = CASE WHEN excluded.state = 'configured' THEN '' ELSE source_status.last_error_class END,
		  updated_at = excluded.updated_at`, id, state, reason, n, tx.NowMicros())
	return err
}
