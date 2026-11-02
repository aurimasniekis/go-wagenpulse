// Package events owns durable events (ADR-0015): the versioned envelope, routing of events to adapter instances as
// per-adapter delivery rows (the transactional outbox), and operational actions on deliveries (retry, replay). Events
// and deliveries are created in the same write transaction as the observations they derive from.
package events

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// SchemaVersion is the envelope schema version (bumped on breaking changes; consumers switch on it).
const SchemaVersion = 1

// Event types.
const (
	TypeSignalChanged    = "signal.changed"
	TypeThresholdCrossed = "threshold.crossed"
	TypeThresholdCleared = "threshold.cleared"
	TypeStateTransition  = "state.transition"
	TypeDataStale        = "data.stale"
	TypeDataResumed      = "data.resumed"
	TypeAdapterTest      = "adapter.test"
)

// Types lists every event type.
var Types = []string{TypeSignalChanged, TypeThresholdCrossed, TypeThresholdCleared, TypeStateTransition, TypeDataStale, TypeDataResumed}

// ValidType reports whether t is a known event type.
func ValidType(t string) bool { return slices.Contains(Types, t) }

// VehicleRef identifies the vehicle. VIN is only filled at publish time for adapters that opt in (ADR-0016).
type VehicleRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name,omitempty"`
	VIN  string `json:"vin,omitempty"`
}

// RuleRef identifies the rule revision that produced the event.
type RuleRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

// SignalRef identifies the signal.
type SignalRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Value is a typed value.
type Value struct {
	Num     *float64 `json:"num,omitempty"`
	Text    *string  `json:"text,omitempty"`
	Quality string   `json:"quality"`
	Unit    string   `json:"unit,omitempty"`
}

// Threshold describes a threshold rule.
type Threshold struct {
	Direction  string  `json:"direction"`
	Value      float64 `json:"value"`
	Hysteresis float64 `json:"hysteresis,omitempty"`
}

// Stale describes a stale-data event.
type Stale struct {
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	AfterSeconds   int64      `json:"after_seconds"`
}

// Provenance traces the event to its source data.
type Provenance struct {
	Source         string `json:"source"` // vw_data_act_portal | upload | rule_sweep
	SourceID       string `json:"source_id,omitempty"`
	ImportID       string `json:"import_id,omitempty"`
	DeliveryID     string `json:"delivery_id,omitempty"`
	DeliverySHA256 string `json:"delivery_sha256,omitempty"`
}

// Replay marks a re-delivery.
type Replay struct {
	Seq        int64     `json:"seq"`
	OfDelivery string    `json:"of_delivery,omitempty"`
	At         time.Time `json:"at"`
}

// Envelope is the event body (schema v1). It contains only rule-selected values, never complete vehicle payloads.
type Envelope struct {
	SchemaVersion int        `json:"schema_version"`
	EventID       string     `json:"event_id"`
	Type          string     `json:"type"`
	Severity      string     `json:"severity,omitempty"`
	Vehicle       VehicleRef `json:"vehicle"`
	Rule          *RuleRef   `json:"rule,omitempty"`
	Signal        *SignalRef `json:"signal,omitempty"`
	Value         *Value     `json:"value,omitempty"`
	Previous      *Value     `json:"previous,omitempty"`
	Threshold     *Threshold `json:"threshold,omitempty"`
	Stale         *Stale     `json:"stale,omitempty"`
	ObservedAt    time.Time  `json:"observed_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Provenance    Provenance `json:"provenance"`
	CorrelationID string     `json:"correlation_id"`
	Backfill      bool       `json:"backfill"`
	Replay        *Replay    `json:"replay,omitempty"`
}

// NewInput creates an event.
type NewInput struct {
	Envelope     Envelope
	VehicleNum   int64
	SignalNum    int64
	RuleRevision int64
	ImportID     string
}

// Router caches routes for one transaction batch.
type Router struct {
	routes []route
	loaded bool
}

type route struct {
	id              string
	adapterID       string
	adapterName     string
	types           []string
	rules           map[string]bool
	vehicles        map[int64]bool
	includeBackfill bool
	maxAttempts     int
}

func (r *Router) load(ctx context.Context, tx *store.Tx) error {
	if r.loaded {
		return nil
	}
	out, err := queryRoutes(ctx, tx)
	if err != nil {
		return err
	}
	for i := range out {
		out[i].rules, out[i].vehicles = map[string]bool{}, map[int64]bool{}
		ruleIDs, err := queryStrings(ctx, tx, `SELECT rule_id FROM route_rules WHERE route_id = ?`, out[i].id)
		if err != nil {
			return err
		}
		for _, id := range ruleIDs {
			out[i].rules[id] = true
		}
		nums, err := queryInts(ctx, tx, `SELECT vehicle_num FROM route_vehicles WHERE route_id = ?`, out[i].id)
		if err != nil {
			return err
		}
		for _, n := range nums {
			out[i].vehicles[n] = true
		}
	}
	r.routes, r.loaded = out, true
	return nil
}

func queryRoutes(ctx context.Context, tx *store.Tx) ([]route, error) {
	rows, err := tx.SQL.QueryContext(ctx, `SELECT r.id, r.adapter_id, a.name, r.event_types_json, r.include_backfill, a.config_json
		FROM routes r JOIN adapters a ON a.id = r.adapter_id WHERE r.enabled = 1 AND a.enabled = 1 ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []route
	for rows.Next() {
		var rt route
		var types, cfg string
		var inc int
		if err := rows.Scan(&rt.id, &rt.adapterID, &rt.adapterName, &types, &inc, &cfg); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(types), &rt.types)
		rt.includeBackfill = inc == 1
		var c struct {
			MaxAttempts int `json:"max_attempts"`
		}
		_ = json.Unmarshal([]byte(cfg), &c)
		rt.maxAttempts = c.MaxAttempts
		if rt.maxAttempts <= 0 {
			rt.maxAttempts = 12
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

func queryStrings(ctx context.Context, tx *store.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.SQL.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func queryInts(ctx context.Context, tx *store.Tx, q string, args ...any) ([]int64, error) {
	rows, err := tx.SQL.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (rt route) matches(in NewInput) bool {
	e := in.Envelope
	if e.Backfill && !rt.includeBackfill {
		return false
	}
	if len(rt.types) > 0 && !slices.Contains(rt.types, e.Type) {
		return false
	}
	if len(rt.rules) > 0 && (e.Rule == nil || !rt.rules[e.Rule.ID]) {
		return false
	}
	if len(rt.vehicles) > 0 && !rt.vehicles[in.VehicleNum] {
		return false
	}
	return true
}

// MsgID derives the deterministic Nats-Msg-Id of a delivery (ADR-0016): retries reuse it (JetStream deduplicates
// within its window), replays use a new sequence so they are published again.
func MsgID(eventID, adapterID string, replaySeq int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("wagenpulse/delivery/v1|%s|%s|%d", eventID, adapterID, replaySeq)))
	return "w" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])[:26])
}

// Create stores an event and one pending delivery per matching adapter (several routes to one adapter yield one
// delivery). It must run inside the ingest (or sweep) transaction.
func Create(ctx context.Context, tx *store.Tx, r *Router, in NewInput) (eventNum int64, deliveries int, err error) {
	if err := r.load(ctx, tx); err != nil {
		return 0, 0, err
	}
	e := in.Envelope
	if e.EventID == "" {
		e.EventID = ids.New(ids.Event)
	}
	e.SchemaVersion = SchemaVersion
	e.CreatedAt = tx.Now.UTC()
	if e.CorrelationID == "" {
		e.CorrelationID = ids.New(ids.Correlation)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return 0, 0, err
	}
	var ruleID any
	var ruleRev any
	if e.Rule != nil {
		ruleID, ruleRev = e.Rule.ID, e.Rule.Revision
	}
	var sig any
	if in.SignalNum > 0 {
		sig = in.SignalNum
	}
	res, err := tx.SQL.ExecContext(ctx, `INSERT INTO events (id, type, rule_id, rule_revision, vehicle_num, signal_num, observed_at, created_at, backfill,
		import_id, correlation_id, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.EventID, e.Type, ruleID, ruleRev, in.VehicleNum, sig,
		e.ObservedAt.UnixMicro(), tx.NowMicros(), b2i(e.Backfill), nullStr(in.ImportID), e.CorrelationID, string(body))
	if err != nil {
		return 0, 0, err
	}
	if eventNum, err = res.LastInsertId(); err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, rt := range r.routes {
		if seen[rt.adapterID] || !rt.matches(NewInput{Envelope: e, VehicleNum: in.VehicleNum}) {
			continue
		}
		seen[rt.adapterID] = true
		if err := insertDelivery(ctx, tx, eventNum, e.EventID, rt.adapterID, rt.adapterName, rt.id, 0, "", rt.maxAttempts); err != nil {
			return eventNum, deliveries, err
		}
		deliveries++
	}
	return eventNum, deliveries, nil
}

func insertDelivery(ctx context.Context, tx *store.Tx, eventNum int64, eventID, adapterID, adapterName, routeID string, replaySeq int64, replayOf string, maxAttempts int) error {
	_, err := tx.SQL.ExecContext(ctx, `INSERT INTO deliveries (id, event_num, adapter_id, adapter_name, route_id, replay_seq, replay_of, msg_id, state,
		max_attempts, next_attempt_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		ids.New(ids.Dispatch), eventNum, adapterID, adapterName, nullStr(routeID), replaySeq, nullStr(replayOf), MsgID(eventID, adapterID, replaySeq),
		maxAttempts, tx.NowMicros(), tx.NowMicros(), tx.NowMicros())
	return err
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

var _ = sql.ErrNoRows
