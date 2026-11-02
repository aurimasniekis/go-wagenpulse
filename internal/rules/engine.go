package rules

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/events"
	"github.com/aurimasniekis/go-wagenpulse/internal/ingest"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs/metrics"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
)

// Engine evaluates rules. It implements ingest.EventSink.
type Engine struct {
	s       *store.Store
	metrics *metrics.Metrics
	// OnEvents is called after commit when events were created (wakes dispatchers).
	OnEvents func()
}

// NewEngine returns an engine.
func NewEngine(s *store.Store, m *metrics.Metrics) *Engine { return &Engine{s: s, metrics: m} }

type loadedRule struct {
	id         string
	name       string
	revision   int64
	def        Definition
	vehicleSet map[string]bool
	signalSet  map[string]bool
}

func (r loadedRule) appliesTo(vehicleID string) bool {
	return len(r.vehicleSet) == 0 || r.vehicleSet[vehicleID]
}

func (r loadedRule) watches(key string) bool {
	return len(r.signalSet) == 0 && r.def.Kind == KindStale || r.signalSet[key]
}

// loadRules returns the enabled rules (current revisions).
func loadRules(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, kind string) ([]loadedRule, error) {
	query := `SELECT r.id, r.name, r.current_revision, v.definition_json FROM rules r JOIN rule_revisions v ON v.rule_id = r.id AND v.revision = r.current_revision
		WHERE r.enabled = 1 AND r.deleted_at IS NULL`
	var args []any
	if kind != "" {
		query += ` AND r.kind = ?`
		args = append(args, kind)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []loadedRule
	for rows.Next() {
		var lr loadedRule
		var def string
		if err := rows.Scan(&lr.id, &lr.name, &lr.revision, &def); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(def), &lr.def); err != nil {
			continue
		}
		lr.vehicleSet, lr.signalSet = map[string]bool{}, map[string]bool{}
		for _, v := range lr.def.VehicleIDs {
			lr.vehicleSet[v] = true
		}
		for _, k := range lr.def.SignalKeys {
			lr.signalSet[k] = true
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

type stateKey struct {
	rule   string
	signal int64
}

// state is one rule_state row.
type state struct {
	revision     int64
	refNum       *float64
	refText      *string
	lastObserved *time.Time
	active       bool
	pending      int
	lastFired    *time.Time
	dirty        bool
	exists       bool
}

type sigInfo struct {
	key, name string
}

// evaluation holds one batch's working set.
type evaluation struct {
	ctx     context.Context
	tx      *store.Tx
	in      ingest.EvalInput
	states  map[stateKey]*state
	signals map[int64]sigInfo
	router  events.Router
	persist bool
	result  ingest.EvalResult
	counts  map[string]map[string]int
}

// Evaluate runs the matching rules over newly accepted observations inside the ingest transaction. With policy
// "preview" it counts would-be events without changing rule state or creating events (ADR-0017).
func (e *Engine) Evaluate(ctx context.Context, tx *store.Tx, in ingest.EvalInput) (ingest.EvalResult, error) {
	if in.Policy == "none" || len(in.Accepted) == 0 {
		return ingest.EvalResult{}, nil
	}
	rules, err := loadRules(ctx, tx.SQL, "")
	if err != nil {
		return ingest.EvalResult{}, err
	}
	ev := &evaluation{ctx: ctx, tx: tx, in: in, states: map[stateKey]*state{}, signals: map[int64]sigInfo{}, persist: in.Policy == "publish",
		counts: map[string]map[string]int{}}
	if err := ev.loadSignals(); err != nil {
		return ingest.EvalResult{}, err
	}
	for _, r := range rules {
		if !r.appliesTo(in.VehicleID) {
			continue
		}
		for _, a := range in.Accepted {
			si := ev.signals[a.SignalNum]
			if !r.watches(si.key) {
				continue
			}
			if err := ev.apply(r, a, si); err != nil {
				return ev.result, err
			}
		}
	}
	if ev.persist {
		if err := ev.saveStates(); err != nil {
			return ev.result, err
		}
		if ev.result.Created > 0 && e.OnEvents != nil {
			tx.AfterCommit(e.OnEvents)
		}
	}
	for kind, m := range ev.counts {
		for res, n := range m {
			e.metrics.RuleEval(kind, res, n)
		}
	}
	return ev.result, nil
}

func (ev *evaluation) count(kind, result string) {
	if ev.counts[kind] == nil {
		ev.counts[kind] = map[string]int{}
	}
	ev.counts[kind][result]++
}

func (ev *evaluation) loadSignals() error {
	for _, a := range ev.in.Accepted {
		if _, ok := ev.signals[a.SignalNum]; ok {
			continue
		}
		var si sigInfo
		if err := ev.tx.SQL.QueryRowContext(ev.ctx, `SELECT key, name FROM signals WHERE num = ?`, a.SignalNum).Scan(&si.key, &si.name); err != nil {
			return err
		}
		ev.signals[a.SignalNum] = si
	}
	return nil
}

func (ev *evaluation) state(rule loadedRule, signal int64) (*state, error) {
	k := stateKey{rule.id, signal}
	if s, ok := ev.states[k]; ok {
		return s, nil
	}
	s := &state{revision: rule.revision}
	var rev int64
	var refNum sql.NullFloat64
	var refText sql.NullString
	var last, fired sql.NullInt64
	var active int
	err := ev.tx.SQL.QueryRowContext(ev.ctx, `SELECT revision, ref_num, ref_text, last_observed_at, active, pending, last_fired_at FROM rule_state
		WHERE rule_id = ? AND vehicle_num = ? AND signal_num = ?`, rule.id, ev.in.VehicleNum, signal).Scan(&rev, &refNum, &refText, &last, &active, &s.pending, &fired)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return nil, err
	case rev != rule.revision:
		s.exists = true // editing a rule resets its state (ADR-0026)
	default:
		s.exists = true
		if refNum.Valid {
			s.refNum = &refNum.Float64
		}
		if refText.Valid {
			s.refText = &refText.String
		}
		if last.Valid {
			t := time.UnixMicro(last.Int64).UTC()
			s.lastObserved = &t
		}
		if fired.Valid {
			t := time.UnixMicro(fired.Int64).UTC()
			s.lastFired = &t
		}
		s.active = active == 1
	}
	ev.states[k] = s
	return s, nil
}

func (ev *evaluation) saveStates() error {
	for k, s := range ev.states {
		if !s.dirty {
			continue
		}
		var last, fired any
		if s.lastObserved != nil {
			last = s.lastObserved.UnixMicro()
		}
		if s.lastFired != nil {
			fired = s.lastFired.UnixMicro()
		}
		active := 0
		if s.active {
			active = 1
		}
		if _, err := ev.tx.SQL.ExecContext(ev.ctx, `INSERT INTO rule_state (rule_id, vehicle_num, signal_num, revision, ref_num, ref_text, last_observed_at,
			active, pending, last_fired_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (rule_id, vehicle_num, signal_num) DO UPDATE SET revision = excluded.revision, ref_num = excluded.ref_num, ref_text = excluded.ref_text,
			  last_observed_at = excluded.last_observed_at, active = excluded.active, pending = excluded.pending, last_fired_at = excluded.last_fired_at,
			  updated_at = excluded.updated_at`, k.rule, ev.in.VehicleNum, k.signal, s.revision, s.refNum, s.refText, last, active, s.pending, fired,
			ev.tx.NowMicros()); err != nil {
			return err
		}
	}
	return nil
}

func textOf(v telemetry.Value) (string, bool) {
	switch {
	case v.Text != nil:
		return *v.Text, true
	case v.Num != nil:
		return strconv.FormatFloat(*v.Num, 'f', -1, 64), true
	}
	return "", false
}

func toEventValue(v telemetry.Value) *events.Value {
	return &events.Value{Num: v.Num, Text: v.Text, Quality: v.Quality, Unit: v.Unit}
}

func refValue(s *state, unit string) *events.Value {
	if s.refNum == nil && s.refText == nil {
		return nil
	}
	return &events.Value{Num: s.refNum, Text: s.refText, Quality: telemetry.QualityOK, Unit: unit}
}

// apply evaluates one observation for one rule.
func (ev *evaluation) apply(r loadedRule, a telemetry.Accepted, si sigInfo) error {
	at := a.ObservedAt
	if r.def.Kind == KindStale {
		return ev.applyStale(r, a)
	}
	s, err := ev.state(r, a.SignalNum)
	if err != nil {
		return err
	}
	if s.lastObserved != nil && (at.Before(*s.lastObserved) || at.Equal(*s.lastObserved) && !a.BecameLast) {
		ev.result.LateSkipped++
		ev.count(r.def.Kind, "late_skipped")
		return nil
	}
	if a.Quality != telemetry.QualityOK {
		return nil // null/unset/invalid values neither change nor fire
	}
	s.lastObserved, s.dirty, s.revision = &at, true, r.revision
	switch r.def.Kind {
	case KindSignalChange:
		cur, ok := textOf(a.Value)
		if !ok {
			return nil
		}
		if s.refNum == nil && s.refText == nil {
			s.refNum, s.refText = a.Num, &cur
			return nil
		}
		differs := false
		switch {
		case a.Num != nil && s.refNum != nil:
			diff := math.Abs(*a.Num - *s.refNum)
			differs = diff > 0 && (r.def.Deadband == 0 || diff >= r.def.Deadband)
		default:
			differs = s.refText == nil || *s.refText != cur
		}
		if !differs {
			s.pending = 0
			return nil
		}
		s.pending++
		if s.pending < r.def.DebounceCount {
			ev.count(r.def.Kind, "debounced")
			return nil
		}
		prev := refValue(s, a.Unit)
		s.refNum, s.refText, s.pending = a.Num, &cur, 0
		return ev.fire(r, s, a, si, events.TypeSignalChanged, prev, nil)
	case KindThreshold:
		if a.Num == nil {
			return nil
		}
		v, t, h := *a.Num, r.def.Threshold, r.def.Hysteresis
		beyond := v > t
		clear := v <= t-h
		if r.def.Direction == "below" {
			beyond, clear = v < t, v >= t+h
		}
		prev := refValue(s, a.Unit)
		s.refNum = a.Num
		if !s.active {
			if !beyond {
				s.pending = 0
				return nil
			}
			s.pending++
			if s.pending < r.def.DebounceCount {
				ev.count(r.def.Kind, "debounced")
				return nil
			}
			s.active, s.pending = true, 0
			return ev.fire(r, s, a, si, events.TypeThresholdCrossed, prev, &events.Threshold{Direction: r.def.Direction, Value: t, Hysteresis: h})
		}
		if clear {
			s.active, s.pending = false, 0
			if r.def.NotifyClear {
				return ev.fire(r, s, a, si, events.TypeThresholdCleared, prev, &events.Threshold{Direction: r.def.Direction, Value: t, Hysteresis: h})
			}
		}
		return nil
	case KindTransition:
		cur, ok := textOf(a.Value)
		if !ok {
			return nil
		}
		if s.refText == nil {
			s.refText = &cur
			return nil
		}
		if cur == *s.refText {
			s.pending = 0
			return nil
		}
		from := *s.refText
		interesting := (len(r.def.From) == 0 || slices.Contains(r.def.From, from)) && (len(r.def.To) == 0 || slices.Contains(r.def.To, cur))
		if !interesting {
			s.refText, s.pending = &cur, 0
			return nil
		}
		s.pending++
		if s.pending < r.def.DebounceCount {
			ev.count(r.def.Kind, "debounced")
			return nil
		}
		prev := &events.Value{Text: &from, Quality: telemetry.QualityOK, Unit: a.Unit}
		s.refText, s.pending = &cur, 0
		return ev.fire(r, s, a, si, events.TypeStateTransition, prev, nil)
	}
	return nil
}

// applyStale re-arms stale rules when fresh data arrives (and optionally emits data.resumed).
func (ev *evaluation) applyStale(r loadedRule, a telemetry.Accepted) error {
	if a.Quality != telemetry.QualityOK {
		return nil
	}
	s, err := ev.state(r, 0)
	if err != nil {
		return err
	}
	at := a.ObservedAt
	if s.lastObserved != nil && !at.After(*s.lastObserved) {
		return nil
	}
	wasStale := s.active
	s.lastObserved, s.active, s.dirty, s.revision = &at, false, true, r.revision
	if wasStale && r.def.NotifyClear {
		si := ev.signals[a.SignalNum]
		return ev.fire(r, s, a, si, events.TypeDataResumed, nil, nil)
	}
	return nil
}

func (ev *evaluation) fire(r loadedRule, s *state, a telemetry.Accepted, si sigInfo, typ string, prev *events.Value, th *events.Threshold) error {
	at := a.ObservedAt
	if s.lastFired != nil && r.def.CooldownSeconds > 0 && at.Sub(*s.lastFired) < time.Duration(r.def.CooldownSeconds)*time.Second {
		ev.count(r.def.Kind, "suppressed")
		return nil
	}
	s.lastFired = &at
	ev.count(r.def.Kind, "fired")
	if !ev.persist {
		ev.result.Previewed++
		return nil
	}
	source := "upload"
	if ev.in.SourceID != "" {
		source = "vw_data_act_portal"
	}
	env := events.Envelope{Type: typ, Severity: r.def.Severity, Vehicle: events.VehicleRef{ID: ev.in.VehicleID, Slug: ev.in.VehicleSlug},
		Rule: &events.RuleRef{ID: r.id, Name: r.name, Revision: r.revision}, Signal: &events.SignalRef{Key: si.key, Name: si.name},
		Value: toEventValue(a.Value), Previous: prev, Threshold: th, ObservedAt: at.UTC(), CorrelationID: ev.in.CorrelationID, Backfill: ev.in.Backfill,
		Provenance: events.Provenance{Source: source, SourceID: ev.in.SourceID, ImportID: ev.in.ImportID, DeliveryID: ev.in.DeliveryID,
			DeliverySHA256: ev.in.DeliverySHA}}
	if _, _, err := events.Create(ev.ctx, ev.tx, &ev.router, events.NewInput{Envelope: env, VehicleNum: ev.in.VehicleNum, SignalNum: a.SignalNum,
		RuleRevision: r.revision, ImportID: ev.in.ImportID}); err != nil {
		return err
	}
	ev.result.Created++
	return nil
}
