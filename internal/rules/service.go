package rules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
)

// Rule is the API view of a rule (its current revision's definition).
type Rule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	Revision    int64      `json:"revision" doc:"Current definition revision; events reference it."`
	Definition  Definition `json:"definition"`
	ETagRev     int64      `json:"etag_revision"`
	Deleted     bool       `json:"deleted"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Stats       *RuleStats `json:"stats,omitempty"`
}

// RuleStats summarizes execution history.
type RuleStats struct {
	Events       int64      `json:"events"`
	Events24h    int64      `json:"events_24h"`
	LastEventAt  *time.Time `json:"last_event_at,omitempty"`
	TrackedPairs int64      `json:"tracked_states"`
}

// Input creates or replaces a rule.
type Input struct {
	Name        string
	Description string
	Enabled     bool
	Definition  Definition
}

// Service manages rules.
type Service struct {
	s     *store.Store
	audit *audit.Writer
	now   func() time.Time
}

// NewService returns a service.
func NewService(s *store.Store, a *audit.Writer, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{s: s, audit: a, now: now}
}

func (in *Input) validate() error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	v := &apperr.Validation{}
	if l := len([]rune(in.Name)); l == 0 || l > 100 {
		v.Add("name", "must be 1..100 characters")
	}
	if len(in.Description) > 2000 {
		v.Add("description", "must be at most 2000 characters")
	}
	if err := v.Err(); err != nil {
		return err
	}
	return in.Definition.Normalize()
}

const ruleCols = `r.id, r.name, r.description, r.enabled, r.current_revision, r.revision, r.deleted_at, r.created_at, r.updated_at, v.definition_json`

func scanRule(row interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var enabled int
	var deleted sql.NullInt64
	var created, updated int64
	var def string
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &enabled, &r.Revision, &r.ETagRev, &deleted, &created, &updated, &def); err != nil {
		return r, err
	}
	r.Enabled, r.Deleted = enabled == 1, deleted.Valid
	r.CreatedAt, r.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	_ = json.Unmarshal([]byte(def), &r.Definition)
	if r.Definition.SignalKeys == nil {
		r.Definition.SignalKeys = []string{}
	}
	if r.Definition.VehicleIDs == nil {
		r.Definition.VehicleIDs = []string{}
	}
	if r.Definition.From == nil {
		r.Definition.From = []string{}
	}
	if r.Definition.To == nil {
		r.Definition.To = []string{}
	}
	return r, nil
}

const ruleFrom = ` FROM rules r JOIN rule_revisions v ON v.rule_id = r.id AND v.revision = r.current_revision`

// ErrNotFound is returned for unknown or deleted rules.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "rule not found")

// Get returns a rule with statistics.
func (svc *Service) Get(ctx context.Context, id string) (Rule, error) {
	r, err := scanRule(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+ruleCols+ruleFrom+` WHERE r.id = ? AND r.deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	st := &RuleStats{}
	var last sql.NullInt64
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT count(*), coalesce(sum(created_at > ?), 0), max(created_at) FROM events WHERE rule_id = ?`,
		svc.now().Add(-24*time.Hour).UnixMicro(), id).Scan(&st.Events, &st.Events24h, &last); err != nil {
		return r, err
	}
	if last.Valid {
		t := time.UnixMicro(last.Int64).UTC()
		st.LastEventAt = &t
	}
	_ = svc.s.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM rule_state WHERE rule_id = ?`, id).Scan(&st.TrackedPairs)
	r.Stats = st
	return r, nil
}

// List returns active rules.
func (svc *Service) List(ctx context.Context) ([]Rule, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT `+ruleCols+ruleFrom+` WHERE r.deleted_at IS NULL ORDER BY r.name, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Revisions returns every revision of a rule, newest first.
func (svc *Service) Revisions(ctx context.Context, id string) ([]map[string]any, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT revision, definition_json, created_by, created_at FROM rule_revisions WHERE rule_id = ? ORDER BY revision DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var rev, at int64
		var def, by string
		if err := rows.Scan(&rev, &def, &by, &at); err != nil {
			return nil, err
		}
		var d Definition
		_ = json.Unmarshal([]byte(def), &d)
		out = append(out, map[string]any{"revision": rev, "definition": d, "created_by": by, "created_at": time.UnixMicro(at).UTC()})
	}
	return out, rows.Err()
}

func (svc *Service) checkVehicles(ctx context.Context, tx *store.Tx, ids []string) error {
	for _, id := range ids {
		var n int
		if err := tx.SQL.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeInvalid, "definition.vehicle_ids: unknown vehicle %s", id)
		}
	}
	return nil
}

func writeSignals(ctx context.Context, tx *store.Tx, id string, keys []string) error {
	if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM rule_signals WHERE rule_id = ?`, id); err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO rule_signals (rule_id, signal_key) VALUES (?, ?)`, id, k); err != nil {
			return err
		}
	}
	return nil
}

// Create adds a rule with revision 1.
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in Input) (Rule, error) {
	if err := in.validate(); err != nil {
		return Rule{}, err
	}
	id := ids.New(ids.Rule)
	def, _ := json.Marshal(in.Definition)
	err := svc.s.W().TxNamed(ctx, "rules.create", func(tx *store.Tx) error {
		if err := svc.checkVehicles(ctx, tx, in.Definition.VehicleIDs); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO rules (id, name, description, enabled, kind, current_revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, id, in.Name, in.Description, b2i(in.Enabled), in.Definition.Kind, tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO rule_revisions (rule_id, revision, definition_json, created_by, created_at) VALUES (?, 1, ?, ?, ?)`,
			id, string(def), actor.ID, tx.NowMicros()); err != nil {
			return err
		}
		if err := writeSignals(ctx, tx, id, in.Definition.SignalKeys); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "rule.created", Target: audit.Target{Type: "rule", ID: id},
			Details: map[string]any{"name": in.Name, "kind": in.Definition.Kind, "enabled": in.Enabled}})
		return err
	})
	if err != nil {
		return Rule{}, err
	}
	return svc.Get(ctx, id)
}

// Update replaces a rule. A changed definition becomes a new immutable revision and resets the rule's state; name,
// description and enabled changes keep the revision.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, id string, ifRevision int64, in Input) (Rule, error) {
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	if err := in.validate(); err != nil {
		return Rule{}, err
	}
	if in.Definition.Kind != cur.Definition.Kind {
		return Rule{}, apperr.New(apperr.CodeInvalid, "definition.kind: the kind of a rule cannot change; create a new rule")
	}
	newDef, _ := json.Marshal(in.Definition)
	oldDef, _ := json.Marshal(cur.Definition)
	defChanged := string(newDef) != string(oldDef)
	err = svc.s.W().TxNamed(ctx, "rules.update", func(tx *store.Tx) error {
		if err := svc.checkVehicles(ctx, tx, in.Definition.VehicleIDs); err != nil {
			return err
		}
		rev := cur.Revision
		if defChanged {
			rev++
			if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO rule_revisions (rule_id, revision, definition_json, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
				id, rev, string(newDef), actor.ID, tx.NowMicros()); err != nil {
				return err
			}
			if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM rule_state WHERE rule_id = ?`, id); err != nil {
				return err
			}
			if err := writeSignals(ctx, tx, id, in.Definition.SignalKeys); err != nil {
				return err
			}
		}
		etag := cur.ETagRev
		if ifRevision != 0 {
			etag = ifRevision
		}
		r, err := tx.SQL.ExecContext(ctx, `UPDATE rules SET name = ?, description = ?, enabled = ?, current_revision = ?, revision = revision + 1, updated_at = ?
			WHERE id = ? AND revision = ? AND deleted_at IS NULL`, in.Name, in.Description, b2i(in.Enabled), rev, tx.NowMicros(), id, etag)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return apperr.New(apperr.CodePrecondition, "the rule was changed by someone else; reload and retry")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "rule.updated", Target: audit.Target{Type: "rule", ID: id},
			Details: map[string]any{"enabled": in.Enabled, "definition_revision": rev, "definition_changed": defChanged}})
		return err
	})
	if err != nil {
		return Rule{}, err
	}
	return svc.Get(ctx, id)
}

// Delete soft-deletes a rule: it stops evaluating; revisions and events stay for history (ADR-0023).
func (svc *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	if _, err := svc.Get(ctx, id); err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "rules.delete", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE rules SET deleted_at = ?, enabled = 0, updated_at = ? WHERE id = ?`, tx.NowMicros(), tx.NowMicros(), id); err != nil {
			return err
		}
		for _, q := range []string{`DELETE FROM rule_signals WHERE rule_id = ?`, `DELETE FROM rule_state WHERE rule_id = ?`, `DELETE FROM route_rules WHERE rule_id = ?`} {
			if _, err := tx.SQL.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "rule.deleted", Target: audit.Target{Type: "rule", ID: id}})
		return err
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PreviewRequest evaluates a definition against stored history without side effects.
type PreviewRequest struct {
	Definition Definition
	VehicleID  string
	From, To   time.Time
}

// PreviewEvent is a hypothetical event.
type PreviewEvent struct {
	Type       string        `json:"type"`
	SignalKey  string        `json:"signal_key"`
	ObservedAt time.Time     `json:"observed_at"`
	Value      *events.Value `json:"value,omitempty"`
	Previous   *events.Value `json:"previous,omitempty"`
}

// PreviewResult reports a preview.
type PreviewResult struct {
	Observations int            `json:"observations_evaluated"`
	Events       []PreviewEvent `json:"events"`
	Truncated    bool           `json:"truncated"`
	Suppressed   int            `json:"suppressed_by_cooldown"`
}

// MaxPreviewObservations bounds a preview.
const MaxPreviewObservations = 50000

// Preview replays stored observations of one vehicle through a definition in memory (stale rules are not previewed:
// they depend on wall-clock time).
func (svc *Service) Preview(ctx context.Context, req PreviewRequest) (PreviewResult, error) {
	d := req.Definition
	if err := d.Normalize(); err != nil {
		return PreviewResult{}, err
	}
	if d.Kind == KindStale {
		return PreviewResult{}, apperr.New(apperr.CodeInvalid, "stale rules cannot be previewed against history")
	}
	if !req.To.After(req.From) || req.To.Sub(req.From) > 366*24*time.Hour {
		return PreviewResult{}, apperr.New(apperr.CodeInvalid, "the time range must be positive and at most 366 days")
	}
	var vnum int64
	if err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT num FROM vehicles WHERE id = ?`, req.VehicleID).Scan(&vnum); err != nil {
		return PreviewResult{}, apperr.New(apperr.CodeNotFound, "vehicle not found")
	}
	q := `SELECT o.signal_num, s.key, s.name, o.observed_at, o.num, o.text, o.quality, o.unit FROM observations o JOIN signals s ON s.num = o.signal_num
		WHERE o.vehicle_num = ? AND o.observed_at >= ? AND o.observed_at < ? AND s.key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(d.SignalKeys)), ",") + `)
		ORDER BY o.observed_at, o.received_at LIMIT ?`
	args := []any{vnum, req.From.UnixMicro(), req.To.UnixMicro()}
	for _, k := range d.SignalKeys {
		args = append(args, k)
	}
	args = append(args, MaxPreviewObservations+1)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return PreviewResult{}, err
	}
	defer rows.Close()
	r := loadedRule{id: "preview", name: "preview", revision: 1, def: d, signalSet: map[string]bool{}}
	for _, k := range d.SignalKeys {
		r.signalSet[k] = true
	}
	ev := &evaluation{ctx: ctx, states: map[stateKey]*state{}, signals: map[int64]sigInfo{}, counts: map[string]map[string]int{}}
	res := PreviewResult{Events: []PreviewEvent{}}
	for rows.Next() {
		var num, at int64
		var key, name, quality, unit string
		var n sql.NullFloat64
		var t sql.NullString
		if err := rows.Scan(&num, &key, &name, &at, &n, &t, &quality, &unit); err != nil {
			return res, err
		}
		if res.Observations == MaxPreviewObservations {
			res.Truncated = true
			break
		}
		res.Observations++
		a := telemetry.Accepted{BecameLast: true, Observation: telemetry.Observation{SignalNum: num, ObservedAt: time.UnixMicro(at).UTC(),
			Value: telemetry.Value{Quality: quality, Unit: unit}}}
		if n.Valid {
			a.Num = &n.Float64
		}
		if t.Valid {
			a.Text = &t.String
		}
		ev.signals[num] = sigInfo{key: key, name: name}
		before := ev.result.Previewed
		prevState := ev.states[stateKey{r.id, num}]
		var prevRef *events.Value
		if prevState != nil {
			prevRef = refValue(prevState, unit)
		}
		if err := ev.previewApply(r, a); err != nil {
			return res, err
		}
		if ev.result.Previewed > before && len(res.Events) < 500 {
			res.Events = append(res.Events, PreviewEvent{Type: typeFor(d), SignalKey: key, ObservedAt: a.ObservedAt, Value: toEventValue(a.Value), Previous: prevRef})
		} else if ev.result.Previewed > before {
			res.Truncated = true
		}
	}
	res.Suppressed = ev.counts[d.Kind]["suppressed"]
	return res, rows.Err()
}

func typeFor(d Definition) string {
	switch d.Kind {
	case KindThreshold:
		return events.TypeThresholdCrossed
	case KindTransition:
		return events.TypeStateTransition
	}
	return events.TypeSignalChanged
}

// previewApply runs apply without a transaction (states start empty; nothing persists).
func (ev *evaluation) previewApply(r loadedRule, a telemetry.Accepted) error {
	k := stateKey{r.id, a.SignalNum}
	if _, ok := ev.states[k]; !ok {
		ev.states[k] = &state{revision: r.revision}
	}
	return ev.apply(r, a, ev.signals[a.SignalNum])
}

// Sweep evaluates stale rules against latest state (durable scheduled evaluation; restart-safe because the fired
// state is persisted). One transaction per rule.
func (e *Engine) Sweep(ctx context.Context, now time.Time) (int, error) {
	rules, err := loadRules(ctx, e.s.ReadDB(), KindStale)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, r := range rules {
		err := e.s.W().TxNamed(ctx, "rules.sweep", func(tx *store.Tx) error {
			vq := `SELECT num, id, slug FROM vehicles WHERE archived_at IS NULL`
			rows, err := tx.SQL.QueryContext(ctx, vq)
			if err != nil {
				return err
			}
			type veh struct {
				num      int64
				id, slug string
			}
			var vehs []veh
			if err := func() error {
				defer rows.Close()
				for rows.Next() {
					var v veh
					if err := rows.Scan(&v.num, &v.id, &v.slug); err != nil {
						return err
					}
					if r.appliesTo(v.id) {
						vehs = append(vehs, v)
					}
				}
				return rows.Err()
			}(); err != nil {
				return err
			}
			router := &events.Router{}
			for _, v := range vehs {
				q := `SELECT max(l.observed_at) FROM latest_state l JOIN signals s ON s.num = l.signal_num WHERE l.vehicle_num = ? AND l.quality = 'ok'`
				args := []any{v.num}
				if len(r.def.SignalKeys) > 0 {
					q += ` AND s.key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(r.def.SignalKeys)), ",") + `)`
					for _, k := range r.def.SignalKeys {
						args = append(args, k)
					}
				}
				var last sql.NullInt64
				if err := tx.SQL.QueryRowContext(ctx, q, args...).Scan(&last); err != nil {
					return err
				}
				if !last.Valid {
					continue // never had data: not stale, unknown
				}
				lastAt := time.UnixMicro(last.Int64).UTC()
				if now.Sub(lastAt) < time.Duration(r.def.StaleAfterSeconds)*time.Second {
					continue
				}
				ev := &evaluation{ctx: ctx, tx: tx, in: ingest.EvalInput{VehicleNum: v.num, VehicleID: v.id, VehicleSlug: v.slug}, states: map[stateKey]*state{},
					persist: true}
				s, err := ev.state(r, 0)
				if err != nil {
					return err
				}
				if s.active && s.lastObserved != nil && !lastAt.After(*s.lastObserved) {
					continue // already fired for this stale episode
				}
				s.active, s.lastObserved, s.lastFired, s.dirty, s.revision = true, &lastAt, &now, true, r.revision
				env := events.Envelope{Type: events.TypeDataStale, Severity: r.def.Severity, Vehicle: events.VehicleRef{ID: v.id, Slug: v.slug},
					Rule: &events.RuleRef{ID: r.id, Name: r.name, Revision: r.revision}, ObservedAt: now.UTC(),
					Stale: &events.Stale{LastObservedAt: &lastAt, AfterSeconds: r.def.StaleAfterSeconds}, Provenance: events.Provenance{Source: "rule_sweep"}}
				if _, _, err := events.Create(ctx, tx, router, events.NewInput{Envelope: env, VehicleNum: v.num, RuleRevision: r.revision}); err != nil {
					return err
				}
				if err := ev.saveStates(); err != nil {
					return err
				}
				fired++
			}
			return nil
		})
		if err != nil {
			return fired, err
		}
	}
	if fired > 0 && e.OnEvents != nil {
		e.OnEvents()
	}
	return fired, nil
}
