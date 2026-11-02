package dictionary

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Presentation helpers derive human-friendly metadata from the dictionary at read time. Nothing here changes stored
// values: labels and enum decodings are views, and anything not stated by the dictionary is left as reported.

// curatedLabels names frequent signals better than a mechanical transformation can. Keys are dictionary names (flat
// layout) as they appear in deliveries.
var curatedLabels = map[string]string{
	"bem_level":                                      "12 V battery level",
	"climacontrol":                                   "Climate control",
	"cruising_range_combined":                        "Range (combined)",
	"cruising_range_primary_engine":                  "Range (primary engine)",
	"cruising_range_secondary_engine":                "Range (secondary engine)",
	"cycle_data_mileage":                             "Cycle mileage",
	"fuel_level__accuracy":                           "Fuel level source",
	"fuel_level_current_level":                       "Fuel level",
	"hv_soc":                                         "High-voltage battery charge",
	"maintenance_interval__time_until_inspection":    "Inspection due in",
	"maintenance_interval__time_until_oil_change":    "Oil change due in",
	"maintenance_interval_distance_until_inspection": "Inspection due in (distance)",
	"maintenance_interval_distance_until_oil_change": "Oil change due in (distance)",
	"mileage":                               "Odometer",
	"oil_level_actual_level":                "Oil level",
	"oil_level_additional_oil_level":        "Oil level (corrected)",
	"oil_level_dipstick_indicator_function": "Electronic dipstick",
	"oil_level_total_max":                   "Oil capacity",
	"outside_temperature":                   "Outside temperature",
	"parking_brake":                         "Parking brake",
	"parking_lights":                        "Parking lights",
	"scr_range":                             "AdBlue range",
	"state_of_hood":                         "Hood",
	"state_service_hatch":                   "Service hatch",
	"state_spoiler":                         "Spoiler",
	"trueness":                              "Position accuracy",
}

// acronyms keep their capitalisation in generated labels.
var acronyms = map[string]string{"bem": "BEM", "scr": "SCR", "hv": "HV", "soc": "SoC", "obd": "OBD", "tss": "TSS", "gps": "GPS", "ac": "AC",
	"dc": "DC", "lv": "LV", "id": "ID", "vin": "VIN", "eco": "eco", "abs": "ABS", "esp": "ESP"}

// statePrefixes turn "<kind>_state_<part>" names into "<Part> <kind>" ("locked_state_front_left_door" → "Front left
// door lock").
var statePrefixes = []struct{ prefix, suffix string }{
	{"locked_state_", "lock"},
	{"open_state_", ""},
	{"safe_state_", "safety"},
	{"tyre_pressure_actual_", "tyre pressure"},
	{"tyre_pressure_required_", "tyre pressure (required)"},
	{"tyre_pressure_differential_", "tyre pressure (difference)"},
}

// Label returns a human-friendly title for a dictionary name: a curated label, a reordered "<part> <state>" label,
// or the name with separators turned into spaces and acronyms preserved. Dictionary names that already read as
// titles (contain spaces) are returned unchanged.
func Label(name string) string {
	if l, ok := curatedLabels[name]; ok {
		return l
	}
	if name == "" || strings.Contains(name, " ") {
		return name
	}
	n := strings.ReplaceAll(name, "__", "_")
	for _, p := range statePrefixes {
		if part, ok := strings.CutPrefix(n, p.prefix); ok {
			if strings.HasPrefix(p.suffix, "tyre") {
				part = strings.TrimSuffix(part, "_tyre") // "spare_tyre" + "tyre pressure" → "Spare tyre pressure"
			}
			return sentence(strings.TrimSpace(words(part) + " " + p.suffix))
		}
	}
	if part, ok := strings.CutPrefix(n, "state_"); ok {
		part = strings.TrimSuffix(part, "_door_window_lifter")
		if part != strings.TrimPrefix(n, "state_") {
			return sentence(words(part) + " window")
		}
		return sentence(words(part))
	}
	return sentence(words(n))
}

func words(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '.' || r == '-' })
	for i, p := range parts {
		if a, ok := acronyms[strings.ToLower(p)]; ok {
			parts[i] = a
		} else {
			parts[i] = strings.ToLower(p)
		}
	}
	return strings.Join(parts, " ")
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	for _, a := range acronyms {
		if strings.HasPrefix(s, a+" ") || s == a {
			return s
		}
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// enumRe finds "label (n)" pairs in dictionary descriptions, e.g. "locked (2) or unlocked (3)".
var enumRe = regexp.MustCompile(`([A-Za-z][A-Za-z-]*(?: [A-Za-z-]+)?)\s*\((\d{1,3})\)`)

// enumStop are words that precede the state name in descriptions ("door is locked (2)") and are dropped.
var enumStop = map[string]bool{"is": true, "if": true, "or": true, "and": true, "are": true, "the": true, "be": true, "as": true, "either": true}

// Enum decodes the integer codes a description documents ("locked (2) or unlocked (3)" → {"2": "locked",
// "3": "unlocked"}). It returns nil unless at least two distinct codes are documented, so a single parenthesised
// number in prose is never mistaken for an enumeration.
func Enum(description string) map[string]string {
	out := map[string]string{}
	for _, m := range enumRe.FindAllStringSubmatch(description, -1) {
		ws := strings.Fields(m[1])
		for len(ws) > 1 && enumStop[strings.ToLower(ws[0])] {
			ws = ws[1:]
		}
		if len(ws) > 1 && !enumStop[strings.ToLower(ws[0])] {
			// Two content words: keep the one right before the code ("door locked (2)" → "locked").
			ws = ws[len(ws)-1:]
		}
		if len(ws) == 0 || enumStop[strings.ToLower(ws[0])] {
			continue
		}
		code, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		key := strconv.Itoa(code)
		if _, dup := out[key]; !dup {
			out[key] = strings.ToLower(strings.Join(ws, " "))
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// EnumFor decodes a signal's codes and applies documented errata of the published dictionary (v1.0.0, 2025-09-11):
//   - open_state_* signals whose description was copied from the lock signal ("locked (2) or unlocked (3)") use the
//     open/closed codes of every other open_state_* signal;
//   - safe_state_* signals whose description omits "safe (2)" get it from their siblings.
//
// Only these sibling-consistent corrections are made; see docs/CAPABILITIES.md (dictionary errata).
func EnumFor(name, description string) map[string]string {
	e := Enum(description)
	switch {
	case e == nil:
		return nil
	case strings.HasPrefix(name, "open_state_") && e["2"] == "locked" && e["3"] == "unlocked":
		e["2"], e["3"] = "open", "closed"
	case strings.HasPrefix(name, "safe_state_") && e["3"] == "unsafe" && e["2"] == "":
		e["2"] = "safe"
	}
	return e
}

// EnumCodes returns the codes of an enum in numeric order (for stable display).
func EnumCodes(e map[string]string) []string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(keys[i])
		b, _ := strconv.Atoi(keys[j])
		return a < b
	})
	return keys
}
