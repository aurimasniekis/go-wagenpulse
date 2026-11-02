package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
	"github.com/aurimasniekis/go-wagenpulse/internal/telemetry"
)

type signalMeta struct {
	num          int64
	kind         dictionary.ValueKind
	unit         string
	observedName string
	origin       string
}

// signalCache resolves upstream keys to signals inside a write transaction, creating 'observed' signals for keys
// that no dictionary describes (docs/CAPABILITIES.md E1: 2 of 64 keys).
type signalCache struct {
	m       map[string]signalMeta
	created int
}

func newSignalCache() *signalCache { return &signalCache{m: map[string]signalMeta{}} }

func (c *signalCache) resolve(ctx context.Context, tx *store.Tx, rec Record) (signalMeta, error) {
	if m, ok := c.m[rec.Key]; ok {
		return m, nil
	}
	var m signalMeta
	var kind string
	err := tx.SQL.QueryRowContext(ctx, `SELECT num, value_kind, unit, observed_name, origin FROM signals WHERE key = ?`, rec.Key).
		Scan(&m.num, &kind, &m.unit, &m.observedName, &m.origin)
	switch err {
	case nil:
		m.kind = dictionary.ValueKind(kind)
		if rec.Name != "" && m.observedName == "" {
			if _, err := tx.SQL.ExecContext(ctx, `UPDATE signals SET observed_name = ? WHERE num = ?`, rec.Name, m.num); err != nil {
				return m, err
			}
			m.observedName = rec.Name
		}
	case sql.ErrNoRows: //nolint:errorlint // database/sql returns the sentinel unwrapped
		name := rec.Name
		if name == "" {
			name = rec.Key
		}
		m.kind = inferKind(rec)
		r, err := tx.SQL.ExecContext(ctx, `INSERT INTO signals (key, name, value_kind, origin, observed_name, updated_at) VALUES (?, ?, ?, 'observed', ?, ?)`,
			rec.Key, name, string(m.kind), rec.Name, tx.NowMicros())
		if err != nil {
			return m, err
		}
		if m.num, err = r.LastInsertId(); err != nil {
			return m, err
		}
		m.origin, m.observedName = "observed", rec.Name
		c.created++
	default:
		return m, err
	}
	c.m[rec.Key] = m
	return m, nil
}

func inferKind(rec Record) dictionary.ValueKind {
	switch {
	case rec.Structured:
		return dictionary.KindJSON
	case rec.Null || rec.Absent || rec.Value == "":
		return dictionary.KindUnknown
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(rec.Value), 64); err == nil {
		return dictionary.KindNumber
	}
	switch strings.ToLower(rec.Value) {
	case "true", "false":
		return dictionary.KindBoolean
	}
	return dictionary.KindUnknown
}

var (
	durationRE = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)s$`)
	tokenRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// sentinels are "not available" markers observed in portal data (unsigned/signed 32-bit extremes).
var sentinels = map[string]bool{"4294967295": true, "2147483647": true, "-2147483648": true}

// normalize types one record's value according to the signal's value kind (ADR-0008). It never invents units: the
// unit is the signal's normalized dictionary unit, or "s" for duration strings like "1800s".
func normalize(rec Record, m signalMeta) telemetry.Value {
	v := telemetry.Value{Quality: telemetry.QualityOK, Unit: m.unit}
	raw := rec.Value
	keepRaw := func() { r := raw; v.Raw = &r }
	switch {
	case rec.Absent, rec.Null:
		v.Quality = telemetry.QualityNull
		return v
	case raw == "" && !rec.Structured:
		v.Quality = telemetry.QualityEmpty
		return v
	}
	trimmed := strings.TrimSpace(raw)
	if sentinels[trimmed] {
		v.Quality = telemetry.QualityUnset
		keepRaw()
		return v
	}
	setText := func(s string) { v.Text = &s }
	setNum := func(f float64) { v.Num = &f }
	switch m.kind {
	case dictionary.KindNumber, dictionary.KindUnknown:
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			setNum(f)
			return v
		}
		if mm := durationRE.FindStringSubmatch(trimmed); mm != nil {
			f, _ := strconv.ParseFloat(mm[1], 64)
			setNum(f)
			if v.Unit == "" || v.Unit == "min" || v.Unit == "s" {
				v.Unit = "s"
			}
			keepRaw()
			return v
		}
		switch strings.ToLower(trimmed) {
		case "true":
			setNum(1)
			setText("true")
			return v
		case "false":
			setNum(0)
			setText("false")
			return v
		}
		if tokenRE.MatchString(trimmed) && len(trimmed) <= 128 {
			// Enumerations are sometimes delivered by name for integer-typed signals: keep the token as text.
			setText(trimmed)
			return v
		}
		if m.kind == dictionary.KindUnknown {
			setText(raw)
			return v
		}
		v.Quality = telemetry.QualityInvalid
		keepRaw()
		return v
	case dictionary.KindBoolean:
		switch strings.ToLower(trimmed) {
		case "true", "1", "on", "yes":
			setNum(1)
			setText("true")
		case "false", "0", "off", "no":
			setNum(0)
			setText("false")
		default:
			v.Quality = telemetry.QualityInvalid
			keepRaw()
		}
		return v
	case dictionary.KindTimestamp:
		if t, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			setText(t.UTC().Format(time.RFC3339Nano))
			setNum(float64(t.UnixMilli()) / 1000)
			return v
		}
		if ms, err := strconv.ParseInt(trimmed, 10, 64); err == nil && ms > 1e11 && ms < 1e14 {
			t := time.UnixMilli(ms).UTC()
			setText(t.Format(time.RFC3339Nano))
			setNum(float64(ms) / 1000)
			keepRaw()
			return v
		}
		setText(raw)
		return v
	case dictionary.KindJSON:
		if rec.Structured || json.Valid([]byte(trimmed)) {
			setText(trimmed)
			return v
		}
		setText(raw)
		return v
	default: // string, enum
		if len(raw) > 4096 {
			v.Quality = telemetry.QualityInvalid
			short := raw[:4096]
			v.Raw = &short
			return v
		}
		setText(raw)
		// Enum values sometimes arrive as their numeric index: keep the number too for charting.
		if m.kind == dictionary.KindEnum {
			if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
				setNum(f)
			}
		}
		return v
	}
}
