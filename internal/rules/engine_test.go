package rules

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
)

// run evaluates a definition over values in memory (the preview path) and returns how many events fired.
func run(t *testing.T, d Definition, obs ...telemetry.Accepted) int {
	t.Helper()
	if err := d.Normalize(); err != nil {
		t.Fatal(err)
	}
	r := loadedRule{id: "r", revision: 1, def: d, signalSet: map[string]bool{"11111111-1111-3111-8111-111111111111": true}}
	ev := &evaluation{ctx: context.Background(), states: map[stateKey]*state{}, signals: map[int64]sigInfo{1: {key: "11111111-1111-3111-8111-111111111111"}},
		counts: map[string]map[string]int{}}
	for _, a := range obs {
		if err := ev.previewApply(r, a); err != nil {
			t.Fatal(err)
		}
	}
	return ev.result.Previewed
}

var t0 = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

func num(minute int, v float64) telemetry.Accepted {
	return telemetry.Accepted{BecameLast: true, Observation: telemetry.Observation{SignalNum: 1, ObservedAt: t0.Add(time.Duration(minute) * time.Minute),
		Value: telemetry.Value{Num: &v, Quality: telemetry.QualityOK}}}
}

func txt(minute int, s string) telemetry.Accepted {
	return telemetry.Accepted{BecameLast: true, Observation: telemetry.Observation{SignalNum: 1, ObservedAt: t0.Add(time.Duration(minute) * time.Minute),
		Value: telemetry.Value{Text: &s, Quality: telemetry.QualityOK}}}
}

func null(minute int) telemetry.Accepted {
	return telemetry.Accepted{BecameLast: true, Observation: telemetry.Observation{SignalNum: 1, ObservedAt: t0.Add(time.Duration(minute) * time.Minute),
		Value: telemetry.Value{Quality: telemetry.QualityNull}}}
}

const k = "11111111-1111-3111-8111-111111111111"

func TestSignalChangeDeadbandAndBaseline(t *testing.T) {
	d := Definition{Kind: KindSignalChange, SignalKeys: []string{k}, Deadband: 5}
	if n := run(t, d, num(0, 50), num(1, 52), num(2, 54), num(3, 56), num(4, 56), null(5), num(6, 60)); n != 1 {
		t.Fatalf("deadband: %d events (want 1: 50→56)", n)
	}
	if n := run(t, Definition{Kind: KindSignalChange, SignalKeys: []string{k}}, num(0, 1), num(1, 1), num(2, 2)); n != 1 {
		t.Fatalf("any change: %d", n)
	}
}

func TestThresholdHysteresisDebounceCooldown(t *testing.T) {
	d := Definition{Kind: KindThreshold, SignalKeys: []string{k}, Direction: "above", Threshold: 80, Hysteresis: 5}
	// 85 fires; 82 inside hysteresis keeps it armed; 74 clears; 90 fires again.
	if n := run(t, d, num(0, 70), num(1, 85), num(2, 82), num(3, 79), num(4, 74), num(5, 90)); n != 2 {
		t.Fatalf("hysteresis: %d", n)
	}
	d.NotifyClear = true
	if n := run(t, d, num(0, 85), num(1, 70)); n != 2 {
		t.Fatalf("notify clear: %d", n)
	}
	d.NotifyClear = false
	d.DebounceCount = 3
	if n := run(t, d, num(0, 85), num(1, 86), num(2, 70), num(3, 85), num(4, 86), num(5, 87)); n != 1 {
		t.Fatalf("debounce: %d", n)
	}
	d.DebounceCount, d.CooldownSeconds = 1, 3600
	if n := run(t, d, num(0, 85), num(1, 70), num(2, 85), num(120, 70), num(121, 85)); n != 2 {
		t.Fatalf("cooldown: %d", n)
	}
	below := Definition{Kind: KindThreshold, SignalKeys: []string{k}, Direction: "below", Threshold: 20, Hysteresis: 2}
	if n := run(t, below, num(0, 50), num(1, 19), num(2, 21), num(3, 23), num(4, 10)); n != 2 {
		t.Fatalf("below: %d", n)
	}
}

func TestTransition(t *testing.T) {
	d := Definition{Kind: KindTransition, SignalKeys: []string{k}, From: []string{"CHARGING"}, To: []string{"READY", "ERROR"}}
	if n := run(t, d, txt(0, "READY"), txt(1, "CHARGING"), txt(2, "READY"), txt(3, "OFF"), txt(4, "CHARGING"), txt(5, "ERROR")); n != 2 {
		t.Fatalf("transition: %d", n)
	}
	any := Definition{Kind: KindTransition, SignalKeys: []string{k}}
	if n := run(t, any, txt(0, "true"), txt(1, "false"), txt(2, "false"), txt(3, "true")); n != 2 {
		t.Fatalf("any transition: %d", n)
	}
}

func TestLateObservationsDoNotFire(t *testing.T) {
	d := Definition{Kind: KindSignalChange, SignalKeys: []string{k}}
	r := loadedRule{id: "r", revision: 1, def: d, signalSet: map[string]bool{k: true}}
	_ = r.def.Normalize()
	ev := &evaluation{ctx: context.Background(), states: map[stateKey]*state{}, signals: map[int64]sigInfo{1: {key: k}}, counts: map[string]map[string]int{}}
	for _, a := range []telemetry.Accepted{num(10, 1), num(5, 99), num(11, 1)} {
		if err := ev.previewApply(r, a); err != nil {
			t.Fatal(err)
		}
	}
	if ev.result.Previewed != 0 || ev.result.LateSkipped != 1 {
		t.Fatalf("late: %+v", ev.result)
	}
}

func TestDefinitionValidation(t *testing.T) {
	bad := []Definition{
		{Kind: "nope", SignalKeys: []string{k}},
		{Kind: KindThreshold, SignalKeys: []string{k}, Direction: "sideways"},
		{Kind: KindSignalChange},
		{Kind: KindSignalChange, SignalKeys: []string{"not-a-key"}},
		{Kind: KindStale, StaleAfterSeconds: 10},
		{Kind: KindSignalChange, SignalKeys: []string{k}, Deadband: -1},
		{Kind: KindSignalChange, SignalKeys: []string{k}, VehicleIDs: []string{"car"}},
	}
	for i, d := range bad {
		if err := d.Normalize(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
