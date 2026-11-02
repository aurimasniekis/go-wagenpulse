package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Formats.
const (
	// FormatContinuousV1 is the verified continuous delivery format of the EU Data Act portal (docs/CAPABILITIES.md E1):
	// {"vin": …, "user_id": …, "Data": [{"key": uuid, "dataFieldName": …, "value": string|null, "timestampUtc": …}]}.
	FormatContinuousV1 = "vwdataact.continuous.v1"
	// FormatUnrecognized marks documents whose structure is not verified; they are kept for later reprocessing.
	FormatUnrecognized = "unrecognized"
)

// MaxRecords bounds records per document.
const MaxRecords = 200000

// Record is one data point of a continuous delivery.
type Record struct {
	Key        string
	Name       string
	Value      string
	Null       bool // JSON null
	Absent     bool // no "value" member
	Structured bool // value was a JSON object/array (kept as JSON text)
	Timestamp  *time.Time
}

// Parsed is a parsed document.
type Parsed struct {
	Format      string
	VIN         string
	Records     []Record
	CapturedAt  *time.Time // from the member name <VIN>_<yyyymmddhhmmss>.json when present
	Fingerprint string     // structural fingerprint (always set)
	Summary     string     // human-readable structure summary for unrecognized documents
	Warnings    []string
}

var (
	memberRE = regexp.MustCompile(`(?:^|/)([A-HJ-NPR-Z0-9]{17})_(\d{14})\.json$`)
	uuidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// MemberTime extracts the VIN and capture time from a portal member name. The time zone of the file name is not
// documented; it is interpreted as UTC (consistent with timestampUtc in the observed samples).
func MemberTime(name string) (vin string, at *time.Time) {
	m := memberRE.FindStringSubmatch(name)
	if m == nil {
		return "", nil
	}
	t, err := time.Parse("20060102150405", m[2])
	if err != nil {
		return m[1], nil
	}
	t = t.UTC()
	return m[1], &t
}

// Parse detects the document format and parses verified formats. Unrecognized structures return a Parsed with
// Format = FormatUnrecognized and a fingerprint; malformed JSON returns a RejectError.
func Parse(doc Document) (*Parsed, error) {
	if !json.Valid(doc.Data) {
		return nil, reject("malformed_json", "the document is not valid JSON")
	}
	fp, summary := fingerprint(doc.Data)
	p := &Parsed{Fingerprint: fp, Summary: summary}
	if vin, at := MemberTime(doc.Name); vin != "" {
		p.CapturedAt = at
		_ = vin
	}
	ok, err := parseContinuous(doc.Data, p)
	if err != nil {
		return nil, err
	}
	if !ok {
		p.Format = FormatUnrecognized
		p.Records = nil
	}
	return p, nil
}

type rawRecord struct {
	Key           *string         `json:"key"`
	DataFieldName *string         `json:"dataFieldName"`
	Value         json.RawMessage `json:"value"`
	TimestampUtc  json.RawMessage `json:"timestampUtc"`
}

// parseContinuous streams the continuous format. It returns false (not an error) when the structure differs: decode
// errors here mean "not this format", not a failure.
//
//nolint:nilerr // see above
func parseContinuous(data []byte, p *Parsed) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false, nil
	}
	sawData := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return false, nil
		}
		key, _ := kt.(string)
		switch key {
		case "vin":
			var vin any
			if err := dec.Decode(&vin); err != nil {
				return false, nil
			}
			if s, ok := vin.(string); ok {
				p.VIN = strings.ToUpper(strings.TrimSpace(s))
			}
		case "Data":
			t, err := dec.Token()
			if err != nil || t != json.Delim('[') {
				return false, nil
			}
			for dec.More() {
				if len(p.Records) >= MaxRecords {
					return false, reject("too_many_records", "a document has more than %d data points", MaxRecords)
				}
				var rr rawRecord
				if err := dec.Decode(&rr); err != nil {
					return false, nil
				}
				rec, ok, warn := convert(rr)
				if warn != "" && len(p.Warnings) < 20 {
					p.Warnings = append(p.Warnings, warn)
				}
				if ok {
					p.Records = append(p.Records, rec)
				}
			}
			if _, err := dec.Token(); err != nil { // closing ]
				return false, nil
			}
			sawData = true
		default:
			// user_id and any other member: skipped without retaining the value.
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return false, nil
			}
		}
	}
	if !sawData || len(p.Records) == 0 && p.VIN == "" {
		return false, nil
	}
	p.Format = FormatContinuousV1
	return true, nil
}

func convert(rr rawRecord) (Record, bool, string) {
	if rr.Key == nil || !uuidRE.MatchString(*rr.Key) {
		return Record{}, false, "a data point without a valid key was skipped"
	}
	r := Record{Key: strings.ToLower(*rr.Key)}
	if rr.DataFieldName != nil {
		r.Name = strings.TrimSpace(*rr.DataFieldName)
		if len(r.Name) > 256 {
			r.Name = r.Name[:256]
		}
	}
	v := bytes.TrimSpace(rr.Value)
	switch {
	case len(v) == 0:
		r.Absent = true
	case bytes.Equal(v, []byte("null")):
		r.Null = true
	case v[0] == '"':
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return Record{}, false, "a data point with an undecodable value was skipped"
		}
		r.Value = s
	case v[0] == '{' || v[0] == '[':
		if len(v) > 64<<10 {
			return Record{}, false, "a data point with an oversized structured value was skipped"
		}
		r.Value, r.Structured = string(v), true
	default:
		r.Value = string(v) // number or boolean literal
	}
	if len(r.Value) > 64<<10 {
		return Record{}, false, "a data point with an oversized value was skipped"
	}
	if ts := bytes.TrimSpace(rr.TimestampUtc); len(ts) > 0 && !bytes.Equal(ts, []byte("null")) {
		t, err := parseTimestamp(ts)
		if err != nil {
			return r, true, "a data point has an unparseable timestampUtc; the delivery time was used"
		}
		r.Timestamp = &t
	}
	return r, true, ""
}

func parseTimestamp(raw []byte) (time.Time, error) {
	var s string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return time.Time{}, err
		}
	} else {
		s = string(raw)
	}
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 1e11 && ms < 1e14 {
		return time.UnixMilli(ms).UTC(), nil
	}
	return time.Time{}, errors.New("unsupported timestamp")
}

// fingerprint summarizes a document's structure to depth 3 (keys and JSON types, never values) and hashes it.
func fingerprint(data []byte) (string, string) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		return "", "unparseable"
	}
	s := shape(v, 0)
	sum := sha256.Sum256([]byte(s))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return hex.EncodeToString(sum[:8]), s
}

func shape(v any, depth int) string {
	switch x := v.(type) {
	case map[string]any:
		if depth >= 3 {
			return "{…}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for i, k := range keys {
			if i == 25 {
				parts = append(parts, fmt.Sprintf("…+%d", len(keys)-25))
				break
			}
			parts = append(parts, safeKey(k)+":"+shape(x[k], depth+1))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(x) == 0 {
			return "[]"
		}
		if depth >= 3 {
			return "[…]"
		}
		return "[" + shape(x[0], depth+1) + "×" + strconv.Itoa(len(x)) + "]"
	case string:
		return "str"
	case json.Number:
		return "num"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return "?"
}

// safeKey bounds and sanitizes object keys in summaries (keys are untrusted).
func safeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if b.Len() >= 40 {
			b.WriteString("…")
			break
		}
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}
