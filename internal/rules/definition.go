// Package rules defines event rules, their immutable revisions and their evaluation (ADR-0026): signal changes with a
// deadband, threshold crossings with hysteresis, boolean/enum transitions and stale data (durable scheduled sweep),
// with vehicle and signal filters, debounce and cooldown. Evaluation runs inside the ingest transaction and creates
// durable events through internal/events.
package rules

import (
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
)

// Kinds.
const (
	KindSignalChange = "signal_change"
	KindThreshold    = "threshold"
	KindTransition   = "transition"
	KindStale        = "stale"
)

// Definition is a rule revision's configuration.
type Definition struct {
	Kind       string   `json:"kind" enum:"signal_change,threshold,transition,stale"`
	SignalKeys []string `json:"signal_keys" doc:"Signals the rule watches (1..50; for stale rules empty = any signal of the vehicle)."`
	VehicleIDs []string `json:"vehicle_ids" doc:"Vehicles the rule applies to (empty = all vehicles)."`
	Severity   string   `json:"severity" enum:"info,warning,critical"`

	// signal_change: numeric changes smaller than the deadband are ignored (0 = any change). Non-numeric values fire on
	// any difference.
	Deadband float64 `json:"deadband,omitempty"`

	// threshold: fires when the value goes above (or below) Threshold; re-arms when it returns past
	// Threshold ∓ Hysteresis. NotifyClear also emits threshold.cleared on re-arm.
	Direction   string  `json:"direction,omitempty" enum:"above,below,"`
	Threshold   float64 `json:"threshold,omitempty"`
	Hysteresis  float64 `json:"hysteresis,omitempty"`
	NotifyClear bool    `json:"notify_clear,omitempty"`

	// transition: fires when the text value changes from one of From (empty = any) to one of To (empty = any).
	From []string `json:"from,omitempty"`
	To   []string `json:"to,omitempty"`

	// stale: fires once when no observation of the watched signals arrived for StaleAfterSeconds; NotifyClear also
	// emits data.resumed when data returns.
	StaleAfterSeconds int64 `json:"stale_after_seconds,omitempty"`

	// DebounceCount requires the condition to hold for this many consecutive observations (default 1).
	DebounceCount int `json:"debounce_count,omitempty"`
	// CooldownSeconds suppresses repeated events for a (vehicle, signal) within this time (observation time).
	CooldownSeconds int64 `json:"cooldown_seconds,omitempty"`
}

var keyRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Normalize validates and canonicalizes a definition (sorted, deduplicated lists; defaults).
func (d *Definition) Normalize() error {
	v := &apperr.Validation{}
	switch d.Kind {
	case KindSignalChange, KindThreshold, KindTransition, KindStale:
	default:
		v.Add("kind", "must be signal_change, threshold, transition or stale")
	}
	for i, k := range d.SignalKeys {
		d.SignalKeys[i] = strings.ToLower(strings.TrimSpace(k))
		if !keyRE.MatchString(d.SignalKeys[i]) {
			v.Add("signal_keys", "invalid signal key %q", k)
		}
	}
	d.SignalKeys = sortedUnique(d.SignalKeys)
	if d.Kind != KindStale && len(d.SignalKeys) == 0 {
		v.Add("signal_keys", "at least one signal is required")
	}
	if len(d.SignalKeys) > 50 {
		v.Add("signal_keys", "at most 50 signals")
	}
	d.VehicleIDs = sortedUnique(d.VehicleIDs)
	for _, id := range d.VehicleIDs {
		if !ids.Valid(ids.Vehicle, id) {
			v.Add("vehicle_ids", "invalid vehicle id %q", id)
		}
	}
	if d.Severity == "" {
		d.Severity = "info"
	}
	if d.Severity != "info" && d.Severity != "warning" && d.Severity != "critical" {
		v.Add("severity", "must be info, warning or critical")
	}
	if d.DebounceCount == 0 {
		d.DebounceCount = 1
	}
	if d.DebounceCount < 1 || d.DebounceCount > 100 {
		v.Add("debounce_count", "must be within 1..100")
	}
	if d.CooldownSeconds < 0 || d.CooldownSeconds > 30*24*3600 {
		v.Add("cooldown_seconds", "must be within 0..2592000")
	}
	finite := func(name string, f float64) {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			v.Add(name, "must be a finite number")
		}
	}
	finite("deadband", d.Deadband)
	finite("threshold", d.Threshold)
	finite("hysteresis", d.Hysteresis)
	switch d.Kind {
	case KindSignalChange:
		if d.Deadband < 0 {
			v.Add("deadband", "must not be negative")
		}
		d.Direction, d.Threshold, d.Hysteresis, d.From, d.To, d.StaleAfterSeconds, d.NotifyClear = "", 0, 0, nil, nil, 0, false
	case KindThreshold:
		if d.Direction != "above" && d.Direction != "below" {
			v.Add("direction", "must be above or below")
		}
		if d.Hysteresis < 0 {
			v.Add("hysteresis", "must not be negative")
		}
		d.Deadband, d.From, d.To, d.StaleAfterSeconds = 0, nil, nil, 0
	case KindTransition:
		d.From, d.To = sortedUnique(d.From), sortedUnique(d.To)
		for _, s := range append(append([]string{}, d.From...), d.To...) {
			if s == "" || len(s) > 128 {
				v.Add("from", "values must be 1..128 characters")
				break
			}
		}
		if len(d.From) > 50 || len(d.To) > 50 {
			v.Add("to", "at most 50 values")
		}
		d.Deadband, d.Direction, d.Threshold, d.Hysteresis, d.StaleAfterSeconds, d.NotifyClear = 0, "", 0, 0, 0, false
	case KindStale:
		if d.StaleAfterSeconds < 300 || d.StaleAfterSeconds > 90*24*3600 {
			v.Add("stale_after_seconds", "must be within 300..7776000 (portal data arrives at most every 15 minutes)")
		}
		d.Deadband, d.Direction, d.Threshold, d.Hysteresis, d.From, d.To, d.DebounceCount = 0, "", 0, 0, nil, nil, 1
	}
	if d.From == nil {
		d.From = []string{}
	}
	if d.To == nil {
		d.To = []string{}
	}
	return v.Err()
}

func sortedUnique(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
