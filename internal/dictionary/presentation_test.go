package dictionary

import (
	"reflect"
	"testing"
)

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"fuel_level_current_level":            "Fuel level",
		"locked_state__rear_left_door":        "Rear left door lock",
		"locked_state_front_engine_bonnet":    "Front engine bonnet lock",
		"open_state_tailgate":                 "Tailgate open",
		"safe_state_rear_right_door":          "Rear right door safety",
		"state_front_left_door_window_lifter": "Front left window",
		"state_sunroof_motor_hood_1":          "Sunroof motor hood 1",
		"tyre_pressure_actual_front_left":     "Front left tyre pressure",
		"tyre_pressure_required_spare_tyre":   "Spare tyre pressure (required)",
		"bem_level":                           "12 V battery level",
		"some_unknown_signal":                 "Some unknown signal",
		"hv_battery.temperature":              "HV battery temperature",
		"Remaining Charge Time Complete":      "Remaining Charge Time Complete",
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnum(t *testing.T) {
	cases := map[string]map[string]string{
		"Indicates if the rear left door is locked (2) or unlocked (3)": {"2": "locked", "3": "unlocked"},
		"Indicates if the engine bonnet is unsupported (0) or invalid (1) or locked (2) or unlocked (3)": {
			"0": "unsupported", "1": "invalid", "2": "locked", "3": "unlocked"},
		"Indicates if parking lights are unsupported (0), invalid (1), off (2), left (3), right (4) or both (5)": {
			"0": "unsupported", "1": "invalid", "2": "off", "3": "left", "4": "right", "5": "both"},
		"Indicates if the rear left door is open(2) or closed (3)": {"2": "open", "3": "closed"},
		"Indicates the state of the sunroof motor hood 1 unsupported (0) or invalid(1) or open (2) or closed (3)": {
			"0": "unsupported", "1": "invalid", "2": "open", "3": "closed"},
		"Indicates if fuel level is measured (0) or calculated (1)": {"0": "measured", "1": "calculated"},
		// Not enumerations: a single code, or "(int)".
		"Indicates if the actual front left tyre pressure is unsupported (0) or invalid (1) or valid (int)": {"0": "unsupported", "1": "invalid"},
		"Temperature outside the vehicle in deci-Kelvin":                                                    nil,
		"Mileage of the vehicle (1) as W3C/#unsignedInt":                                                    nil,
	}
	for in, want := range cases {
		if got := Enum(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Enum(%q) = %v, want %v", in, got, want)
		}
	}
}
