// Package adapters manages outbound adapter instances (ADR-0015, ADR-0016) and delivers events to them. The only
// adapter kind is Pulse over NATS JetStream; the Publisher interface keeps the boundary open for future kinds without
// speculative implementations.
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/keyring"
	"github.com/aurimasniekis/go-wagenpulse/internal/secretref"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// KindPulse is the Pulse/NATS adapter kind.
const KindPulse = "pulse_nats"

// DefaultSubjectTemplate maps an event to a Pulse subject: evt.<domain>.vehicle.<event>.<vehicle slug>.
const DefaultSubjectTemplate = "evt.{domain}.vehicle.{event}.{vehicle}"

// PulseConfig is the non-secret configuration of a Pulse adapter.
type PulseConfig struct {
	URL                    string `json:"url" doc:"nats:// or tls:// URL of the Pulse NATS endpoint."`
	SubjectDomain          string `json:"subject_domain" doc:"Second subject token (default wagenpulse); the Pulse client needs publish:evt.<domain>.>."`
	SubjectTemplate        string `json:"subject_template" doc:"Placeholders {domain}, {event}, {vehicle}, {rule}, {severity}; must render a valid Pulse subject."`
	IncludeVIN             bool   `json:"include_vin" doc:"Add the VIN to published envelopes (off by default: VINs are personal data)."`
	PublishTimeoutMS       int    `json:"publish_timeout_ms" doc:"Wait for the JetStream acknowledgement (1000..60000)."`
	MaxAttempts            int    `json:"max_attempts" doc:"Attempts before a delivery becomes dead (1..100)."`
	CAPEM                  string `json:"ca_pem,omitempty" doc:"Extra PEM CA trusted for the NATS TLS certificate."`
	AllowInsecureTransport bool   `json:"allow_insecure_transport" doc:"Permit plaintext nats:// to non-loopback hosts in production."`
	Source                 string `json:"source,omitempty" doc:"CloudEvents source (ce-source); default wagenpulse/<installation id>."`
}

// Adapter is the API view. The NATS credentials are never returned.
type Adapter struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind" enum:"pulse_nats"`
	Enabled           bool           `json:"enabled"`
	Config            PulseConfig    `json:"config"`
	CredsSet          bool           `json:"creds_set"`
	CredsRef          string         `json:"creds_ref,omitempty"`
	SecretFingerprint string         `json:"secret_fingerprint,omitempty"`
	Revision          int64          `json:"revision"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Status            *AdapterStatus `json:"status,omitempty"`
	Queue             *AdapterQueue  `json:"queue,omitempty"`
}

// AdapterStatus is the runtime health of an adapter.
type AdapterStatus struct {
	State          string     `json:"state" enum:"connecting,connected,disconnected,error,disabled"`
	Reason         string     `json:"reason,omitempty"`
	LastOKAt       *time.Time `json:"last_ok_at,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	LastErrorClass string     `json:"last_error_class,omitempty"`
	LastTestAt     *time.Time `json:"last_test_at,omitempty"`
	LastTestResult string     `json:"last_test_result,omitempty"`
}

// AdapterQueue counts the adapter's deliveries.
type AdapterQueue struct {
	Pending   int64 `json:"pending"`
	Retrying  int64 `json:"retrying"`
	Dead      int64 `json:"dead"`
	Succeeded int64 `json:"succeeded_24h"`
}

// Input creates or replaces an adapter. Creds is write-only (nil = keep); CredsRef replaces stored creds.
type Input struct {
	Name     string
	Enabled  bool
	Config   PulseConfig
	Creds    *string
	CredsRef string
}

// Service manages adapters.
type Service struct {
	s     *store.Store
	kr    *keyring.Keyring
	audit *audit.Writer
	refs  *secretref.Resolver
	// Production refuses plaintext transport to non-loopback hosts unless explicitly allowed.
	Production bool
	// Changed is called after configuration changes (the dispatcher reconciles).
	Changed func()
	now     func() time.Time
}

// NewService returns a service.
func NewService(s *store.Store, kr *keyring.Keyring, a *audit.Writer, refs *secretref.Resolver) *Service {
	return &Service{s: s, kr: kr, audit: a, refs: refs, now: time.Now}
}

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	domainRE = regexp.MustCompile(`^[a-z0-9_-]{1,48}$`)
)

// RenderSubject renders a subject template.
func RenderSubject(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// SubjectToken encodes an event type or identifier as a Pulse subject token ([a-z0-9_-]).
func SubjectToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

func (svc *Service) validate(in *Input) error {
	v := &apperr.Validation{}
	in.Name = strings.TrimSpace(in.Name)
	if !nameRE.MatchString(in.Name) {
		v.Add("name", "must be 1..64 letters, digits, spaces, '.', '_' or '-'")
	}
	c := &in.Config
	c.URL = strings.TrimSpace(c.URL)
	u, err := url.Parse(c.URL)
	switch {
	case err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil:
		v.Add("config.url", "must be a nats:// or tls:// URL without credentials, path or query (credentials go in creds)")
	case u.Scheme != "nats" && u.Scheme != "tls":
		v.Add("config.url", "scheme must be nats or tls")
	case u.Scheme == "nats" && svc.Production && !c.AllowInsecureTransport && !isLoopback(u.Hostname()):
		v.Add("config.url", "plaintext nats:// to a non-loopback host is refused in production; use tls:// or set allow_insecure_transport")
	}
	if c.SubjectDomain == "" {
		c.SubjectDomain = "wagenpulse"
	}
	if !domainRE.MatchString(c.SubjectDomain) {
		v.Add("config.subject_domain", "must be 1..48 of a-z, 0-9, '_' or '-'")
	}
	if c.SubjectTemplate == "" {
		c.SubjectTemplate = DefaultSubjectTemplate
	}
	sample := RenderSubject(c.SubjectTemplate, map[string]string{"domain": c.SubjectDomain, "event": "threshold_crossed", "vehicle": "my-car",
		"rule": "rul_01abc", "severity": "warning"})
	switch {
	case !strings.HasPrefix(c.SubjectTemplate, "evt."):
		v.Add("config.subject_template", "must start with evt. (Pulse stores only evt.> subjects)")
	case strings.Contains(sample, "{") || strings.Contains(sample, "}"):
		v.Add("config.subject_template", "uses an unknown placeholder (allowed: {domain}, {event}, {vehicle}, {rule}, {severity})")
	default:
		if err := envelope.ValidateSubject(sample); err != nil {
			v.Add("config.subject_template", "does not render a valid Pulse subject: %s", err.Error())
		}
	}
	if c.PublishTimeoutMS == 0 {
		c.PublishTimeoutMS = 10000
	}
	if c.PublishTimeoutMS < 1000 || c.PublishTimeoutMS > 60000 {
		v.Add("config.publish_timeout_ms", "must be within 1000..60000")
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 12
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 100 {
		v.Add("config.max_attempts", "must be within 1..100")
	}
	if c.CAPEM != "" && (!strings.Contains(c.CAPEM, "BEGIN CERTIFICATE") || len(c.CAPEM) > 64<<10) {
		v.Add("config.ca_pem", "must be PEM certificates (at most 64 KiB)")
	}
	if len(c.Source) > 200 {
		v.Add("config.source", "must be at most 200 characters")
	}
	if in.Creds != nil && *in.Creds != "" {
		if in.CredsRef != "" {
			v.Add("creds_ref", "set either creds or creds_ref, not both")
		}
		if !strings.Contains(*in.Creds, "BEGIN NATS USER JWT") || !strings.Contains(*in.Creds, "BEGIN USER NKEY SEED") {
			v.Add("creds", "must be a Pulse client .creds file (user JWT and NKey seed)")
		}
		if len(*in.Creds) > 16<<10 {
			v.Add("creds", "must be at most 16 KiB")
		}
	}
	if in.CredsRef != "" {
		if err := svc.refs.Validate(in.CredsRef); err != nil {
			v.Add("creds_ref", "%s", err.Error())
		}
	}
	return v.Err()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const cols = `id, name, kind, enabled, config_json, creds_enc IS NOT NULL OR creds_ref IS NOT NULL, coalesce(creds_ref, ''), secret_fingerprint,
	revision, created_at, updated_at`

func scan(r interface{ Scan(...any) error }) (Adapter, error) {
	var a Adapter
	var enabled, set int
	var cfg string
	var created, updated int64
	if err := r.Scan(&a.ID, &a.Name, &a.Kind, &enabled, &cfg, &set, &a.CredsRef, &a.SecretFingerprint, &a.Revision, &created, &updated); err != nil {
		return a, err
	}
	a.Enabled, a.CredsSet = enabled == 1, set == 1
	_ = json.Unmarshal([]byte(cfg), &a.Config)
	a.CreatedAt, a.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	return a, nil
}

// ErrNotFound is returned for unknown adapters.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "adapter not found")

// Get returns an adapter with status and queue counts.
func (svc *Service) Get(ctx context.Context, id string) (Adapter, error) {
	a, err := scan(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+cols+` FROM adapters WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	return a, svc.decorate(ctx, &a)
}

func (svc *Service) decorate(ctx context.Context, a *Adapter) error {
	st := &AdapterStatus{State: "connecting"}
	if !a.Enabled {
		st.State = "disabled"
	}
	var ok, errAt, test sql.NullInt64
	err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT state, reason, last_ok_at, last_error_at, last_error_class, last_test_at, last_test_result
		FROM adapter_status WHERE adapter_id = ?`, a.ID).Scan(&st.State, &st.Reason, &ok, &errAt, &st.LastErrorClass, &test, &st.LastTestResult)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	st.LastOKAt, st.LastErrorAt, st.LastTestAt = tp(ok), tp(errAt), tp(test)
	if !a.Enabled {
		st.State = "disabled"
	}
	a.Status = st
	q := &AdapterQueue{}
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT coalesce(sum(state IN ('pending', 'processing')), 0), coalesce(sum(state = 'retrying'), 0),
		coalesce(sum(state = 'dead'), 0), coalesce(sum(state = 'succeeded' AND succeeded_at > ?), 0) FROM deliveries WHERE adapter_id = ?`,
		svc.now().Add(-24*time.Hour).UnixMicro(), a.ID).Scan(&q.Pending, &q.Retrying, &q.Dead, &q.Succeeded); err != nil {
		return err
	}
	a.Queue = q
	return nil
}

func tp(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMicro(v.Int64).UTC()
	return &t
}

// List returns every adapter.
func (svc *Service) List(ctx context.Context) ([]Adapter, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT `+cols+` FROM adapters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []Adapter
	if err := func() error {
		defer rows.Close()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
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
		out = []Adapter{}
	}
	return out, nil
}

func (svc *Service) seal(id, creds string) ([]byte, string, error) {
	sealed, err := svc.kr.Seal(keyring.PurposeField, keyring.FieldAAD("adapters", "creds_enc", id), []byte(creds))
	if err != nil {
		return nil, "", err
	}
	k, err := svc.kr.MAC(keyring.PurposeToken, []byte("wagenpulse/fingerprint/v1"))
	if err != nil {
		return nil, "", err
	}
	return sealed, secretref.Fingerprint(k, []byte(creds)), nil
}

// Creds returns the adapter's .creds content (sealed column or external reference).
func (svc *Service) Creds(ctx context.Context, id string) ([]byte, error) {
	var enc []byte
	var ref sql.NullString
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT creds_enc, creds_ref FROM adapters WHERE id = ?`, id).Scan(&enc, &ref); err != nil {
		return nil, err
	}
	if ref.Valid {
		return svc.refs.Resolve(ref.String)
	}
	if len(enc) == 0 {
		return nil, apperr.New(apperr.CodeInvalid, "the adapter has no NATS credentials")
	}
	return svc.kr.Open(keyring.PurposeField, keyring.FieldAAD("adapters", "creds_enc", id), enc)
}

// Create adds an adapter.
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in Input) (Adapter, error) {
	if err := svc.validate(&in); err != nil {
		return Adapter{}, err
	}
	if (in.Creds == nil || *in.Creds == "") && in.CredsRef == "" {
		return Adapter{}, apperr.New(apperr.CodeInvalid, "creds: a Pulse client .creds file (or creds_ref) is required")
	}
	id := ids.New(ids.Adapter)
	var enc []byte
	fp := "ref"
	if in.Creds != nil && *in.Creds != "" {
		var err error
		if enc, fp, err = svc.seal(id, *in.Creds); err != nil {
			return Adapter{}, err
		}
	}
	cfg, _ := json.Marshal(in.Config)
	err := svc.s.W().TxNamed(ctx, "adapters.create", func(tx *store.Tx) error {
		var n int
		if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM adapters WHERE name = ?`, in.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.New(apperr.CodeAlreadyExists, "an adapter named %q already exists", in.Name)
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO adapters (id, name, kind, enabled, config_json, creds_enc, creds_ref, secret_fingerprint, created_at,
			updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, KindPulse, b2i(in.Enabled), string(cfg), enc, nullStr(in.CredsRef), fp,
			tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "adapter.created", Target: audit.Target{Type: "adapter", ID: id},
			Details: map[string]any{"name": in.Name, "url_host": hostOf(in.Config.URL), "subject_domain": in.Config.SubjectDomain, "enabled": in.Enabled,
				"include_vin": in.Config.IncludeVIN}})
		return err
	})
	if err != nil {
		return Adapter{}, err
	}
	svc.changed()
	return svc.Get(ctx, id)
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Host
}

// Update replaces an adapter's configuration.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, id string, ifRevision int64, in Input) (Adapter, error) {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return Adapter{}, err
	}
	if err := svc.validate(&in); err != nil {
		return Adapter{}, err
	}
	var enc []byte
	var ref sql.NullString
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT creds_enc, creds_ref FROM adapters WHERE id = ?`, id).Scan(&enc, &ref); err != nil {
		return Adapter{}, err
	}
	fp := cur.SecretFingerprint
	refOut := ref.String
	switch {
	case in.CredsRef != "":
		enc, refOut, fp = nil, in.CredsRef, "ref"
	case in.Creds != nil && *in.Creds != "":
		if enc, fp, err = svc.seal(id, *in.Creds); err != nil {
			return Adapter{}, err
		}
		refOut = ""
	case ref.Valid:
		return Adapter{}, apperr.New(apperr.CodeInvalid, "creds_ref: send the current reference (or new creds) when updating an adapter that uses a reference")
	}
	cfg, _ := json.Marshal(in.Config)
	err = svc.s.W().TxNamed(ctx, "adapters.update", func(tx *store.Tx) error {
		if in.Name != cur.Name {
			var n int
			if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM adapters WHERE name = ? AND id != ?`, in.Name, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return apperr.New(apperr.CodeAlreadyExists, "an adapter named %q already exists", in.Name)
			}
		}
		rev := cur.Revision
		if ifRevision != 0 {
			rev = ifRevision
		}
		r, err := tx.SQL.ExecContext(ctx, `UPDATE adapters SET name = ?, enabled = ?, config_json = ?, creds_enc = ?, creds_ref = ?, secret_fingerprint = ?,
			revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`, in.Name, b2i(in.Enabled), string(cfg), enc, nullStr(refOut), fp,
			tx.NowMicros(), id, rev)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return apperr.New(apperr.CodePrecondition, "the adapter was changed by someone else; reload and retry")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "adapter.updated", Target: audit.Target{Type: "adapter", ID: id},
			Details: map[string]any{"enabled": in.Enabled, "url_host": hostOf(in.Config.URL), "creds_changed": fp != cur.SecretFingerprint,
				"include_vin": in.Config.IncludeVIN}})
		return err
	})
	if err != nil {
		return Adapter{}, err
	}
	svc.changed()
	return svc.Get(ctx, id)
}

// Delete removes an adapter. Pending or retrying deliveries block deletion unless abandonPending is set, in which
// case they become dead with reason adapter_deleted; delivery history is kept (ADR-0023).
func (svc *Service) Delete(ctx context.Context, actor audit.Actor, id string, abandonPending bool) error {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return err
	}
	open := cur.Queue.Pending + cur.Queue.Retrying
	if open > 0 && !abandonPending {
		return apperr.New(apperr.CodeConflict, "the adapter has %d undelivered events; pass abandon_pending=true to delete it anyway", open)
	}
	err = svc.s.W().TxNamed(ctx, "adapters.delete", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE deliveries SET state = 'dead', dead_reason = 'adapter_deleted', lease_until = NULL, updated_at = ?
			WHERE adapter_id = ? AND state IN ('pending', 'processing', 'retrying')`, tx.NowMicros(), id); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM adapters WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "adapter.deleted", Target: audit.Target{Type: "adapter", ID: id},
			Details: map[string]any{"name": cur.Name, "abandoned": open}})
		return err
	})
	if err == nil {
		svc.changed()
	}
	return err
}

func (svc *Service) changed() {
	if svc.Changed != nil {
		svc.Changed()
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// setStatus records adapter health.
func setStatus(ctx context.Context, s *store.Store, id, state, reason, errClass string, ok bool) error {
	return s.W().TxNamed(ctx, "adapters.status", func(tx *store.Tx) error {
		var okAt, errAt any
		if ok {
			okAt = tx.NowMicros()
		}
		if errClass != "" {
			errAt = tx.NowMicros()
		}
		_, err := tx.SQL.ExecContext(ctx, `INSERT INTO adapter_status (adapter_id, state, reason, last_ok_at, last_error_at, last_error_class, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (adapter_id) DO UPDATE SET state = excluded.state, reason = excluded.reason,
			last_ok_at = coalesce(excluded.last_ok_at, adapter_status.last_ok_at), last_error_at = coalesce(excluded.last_error_at, adapter_status.last_error_at),
			last_error_class = CASE WHEN excluded.last_error_class != '' THEN excluded.last_error_class ELSE adapter_status.last_error_class END,
			updated_at = excluded.updated_at`, id, state, reason, okAt, errAt, errClass, tx.NowMicros())
		if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
			return nil // the adapter was deleted meanwhile
		}
		return err
	})
}

var _ = fmt.Sprintf
