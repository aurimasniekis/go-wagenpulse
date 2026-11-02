package dictionary

import (
	"database/sql"
	"strings"
)

var errNoRows = sql.ErrNoRows

// ValueKind is how a signal's values are typed.
type ValueKind string

// Value kinds.
const (
	KindNumber    ValueKind = "number"
	KindBoolean   ValueKind = "boolean"
	KindEnum      ValueKind = "enum"
	KindString    ValueKind = "string"
	KindTimestamp ValueKind = "timestamp"
	KindJSON      ValueKind = "json"
	KindUnknown   ValueKind = "unknown"
)

// KindOf maps the dictionaries' free-text technical data types to a value kind. Unknown types map to KindUnknown and
// are typed from the observed values instead.
func KindOf(dataType string) ValueKind {
	t := strings.ToLower(strings.TrimSpace(dataType))
	switch {
	case t == "":
		return KindUnknown
	case strings.Contains(t, "bool"):
		return KindBoolean
	case t == "enum" || strings.HasPrefix(t, "enum"):
		return KindEnum
	case strings.Contains(t, "timestamp") || strings.Contains(t, "datetime") || t == "date" || t == "time":
		return KindTimestamp
	case t == "struct" || t == "object" || t == "json" || t == "jsonb" || strings.HasPrefix(t, "array"):
		return KindJSON
	case strings.Contains(t, "int") || strings.Contains(t, "float") || strings.Contains(t, "double") || strings.Contains(t, "numeric") ||
		strings.Contains(t, "decimal") || strings.Contains(t, "floating point") || t == "real" || t == "long" || t == "short" || t == "number":
		return KindNumber
	case strings.Contains(t, "string") || strings.Contains(t, "char") || strings.Contains(t, "text") || t == "hex" || t == "uuid":
		return KindString
	}
	return KindUnknown
}

// unitAliases maps raw unit spellings seen in the dictionaries to a canonical unit. Units not listed keep their raw
// text: WagenPulse never invents or converts units without evidence (ADR-0008).
var unitAliases = map[string]string{
	"min": "min", "minute": "min", "minutes": "min", "(min)": "min",
	"s": "s", "sec": "s", "second": "s", "seconds": "s", "(s)": "s",
	"h": "h", "hour": "h", "hours": "h", "(h)": "h",
	"day": "d", "days": "d", "(days)": "d", "(day)": "d",
	"km": "km", "(km)": "km",
	"m": "m", "(m)": "m",
	"%": "%", "(%)": "%", "percentage": "%", "percent": "%",
	"°c": "°C", "(°c)": "°C", "degc": "°C", "celsius": "°C",
	"k": "K", "kelvin": "K",
	"kwh": "kWh", "(kwh)": "kWh",
	"kw": "kW", "(kw)": "kW",
	"w": "W", "watt": "W", "watts": "W",
	"v": "V", "(v)": "V", "volt": "V", "volts": "V",
	"mv": "mV",
	"a":  "A", "(a)": "A", "ampere": "A",
	"km/h": "km/h", "(km/h)": "km/h", "kmperhour": "km/h",
	"l": "L", "(l)": "L", "liter": "L", "litre": "L",
	"bar": "bar", "(bar)": "bar",
	"kpa":   "kPa",
	"1/min": "rpm", "(1/min)": "rpm", "rpm": "rpm",
	"kg": "kg",
}

// NormalizeUnit returns the canonical unit for raw dictionary unit text, or the trimmed raw text when unknown.
func NormalizeUnit(raw string) string {
	r := strings.TrimSpace(raw)
	if r == "" || r == "'-" || r == "-" {
		return ""
	}
	if c, ok := unitAliases[strings.ToLower(r)]; ok {
		return c
	}
	return r
}
