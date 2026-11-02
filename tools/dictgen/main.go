// Command dictgen converts the official Volkswagen Group EU Data Act dictionary PDFs ("List of Continuous Data",
// "List of Historical Data") into the normalized JSON bundled with WagenPulse (internal/dictionary/bundled).
//
// It is a development tool: it shells out to Ghostscript (`gs -sDEVICE=txtwrite -dTextFormat=1`) to obtain every
// character with its position, reconstructs table columns from the bold header row of each page, and joins wrapped
// cell lines. It never runs inside the server.
//
//	go run ./tools/dictgen -kind continuous -in "List of Continuous Data.pdf" -out internal/dictionary/bundled/continuous.json
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entry is one dictionary row.
type Entry struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	DataType    string   `json:"data_type,omitempty"`
	Clusters    []string `json:"clusters,omitempty"`
}

// Document is the bundled JSON format (shared with internal/dictionary).
type Document struct {
	Format          string  `json:"format"` // wagenpulse.dictionary.v1
	Kind            string  `json:"kind"`   // continuous | historical
	Title           string  `json:"title"`
	DocumentVersion string  `json:"document_version"`
	DocumentDate    string  `json:"document_date"` // YYYY-MM-DD
	SourceFile      string  `json:"source_file"`
	SourceSHA256    string  `json:"source_sha256"`
	Publisher       string  `json:"publisher"`
	Entries         []Entry `json:"entries"`
}

type char struct {
	x0, x1, y float64
	c         string
	bold      bool
}

func main() {
	kind := flag.String("kind", "", "continuous or historical")
	in := flag.String("in", "", "input PDF")
	out := flag.String("out", "", "output JSON")
	gs := flag.String("gs", "gs", "ghostscript binary")
	flag.Parse()
	if (*kind != "continuous" && *kind != "historical") || *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*kind, *in, *out, *gs); err != nil {
		fmt.Fprintln(os.Stderr, "dictgen:", err)
		os.Exit(1)
	}
}

func run(kind, in, out, gs string) error {
	raw, err := os.ReadFile(in) //nolint:gosec // developer-supplied path
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=txtwrite", "-dTextFormat=1", "-o", "-", in) //nolint:gosec // developer tool
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ghostscript: %w: %s", err, stderr.String())
	}
	pages, err := parsePages(&stdout)
	if err != nil {
		return err
	}
	doc := Document{Format: "wagenpulse.dictionary.v1", Kind: kind, SourceFile: baseName(in), SourceSHA256: hex.EncodeToString(sum[:]),
		Publisher: "Volkswagen AG (EU Data Act portal)"}
	doc.Title, doc.DocumentVersion, doc.DocumentDate = metadata(pages)
	entries, err := extract(pages, kind == "continuous")
	if err != nil {
		return err
	}
	doc.Entries = entries
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil { //nolint:gosec // bundled data file
		return err
	}
	fmt.Fprintf(os.Stderr, "dictgen: %s: %d entries (version %s, %s)\n", kind, len(entries), doc.DocumentVersion, doc.DocumentDate)
	return nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// parsePages reads Ghostscript's TextFormat=1 XML: <page><block><line><span font=…><char bbox="x0 y0 x1 y1" c="…"/>.
func parsePages(r io.Reader) ([][]char, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var pages [][]char
	var cur []char
	bold := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "page":
				cur = nil
			case "span":
				bold = strings.Contains(attr(t, "font"), "Bold")
			case "char":
				f := strings.Fields(attr(t, "bbox"))
				if len(f) != 4 {
					continue
				}
				x0, _ := strconv.ParseFloat(f[0], 64)
				y, _ := strconv.ParseFloat(f[1], 64)
				x1, _ := strconv.ParseFloat(f[2], 64)
				cur = append(cur, char{x0: x0, x1: x1, y: y, c: attr(t, "c"), bold: bold})
			}
		case xml.EndElement:
			if t.Name.Local == "page" {
				pages = append(pages, cur)
			}
		}
	}
	return pages, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// line is a sequence of characters sharing a baseline.
type line struct {
	y     float64
	chars []char
}

func lines(cs []char) []line {
	sort.SliceStable(cs, func(i, j int) bool {
		if abs(cs[i].y-cs[j].y) > 1.5 {
			return cs[i].y < cs[j].y
		}
		return cs[i].x0 < cs[j].x0
	})
	var out []line
	for _, c := range cs {
		if len(out) > 0 && abs(out[len(out)-1].y-c.y) <= 1.5 {
			out[len(out)-1].chars = append(out[len(out)-1].chars, c)
			continue
		}
		out = append(out, line{y: c.y, chars: []char{c}})
	}
	return out
}

func text(cs []char) string {
	var b strings.Builder
	for i, c := range cs {
		if i > 0 && c.x0-cs[i-1].x1 > 2.5 && c.c != " " && !strings.HasSuffix(b.String(), " ") {
			b.WriteByte(' ')
		}
		b.WriteString(c.c)
	}
	return strings.TrimSpace(b.String())
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

var (
	versionRE = regexp.MustCompile(`Version:\s*([0-9][0-9.]*)`)
	dateRE    = regexp.MustCompile(`Date:\s*([0-9]{2})\.([0-9]{2})\.([0-9]{4})`)
	uuidRE    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func metadata(pages [][]char) (title, version, date string) {
	if len(pages) == 0 {
		return
	}
	var all []string
	for _, l := range lines(append([]char(nil), pages[0]...)) {
		all = append(all, text(l.chars))
	}
	joined := strings.Join(all, "\n")
	for _, l := range all {
		if strings.HasPrefix(l, "List of") {
			title = "EU Data Act - " + strings.TrimSpace(l)
			break
		}
	}
	if m := versionRE.FindStringSubmatch(joined); m != nil {
		version = m[1]
	}
	if m := dateRE.FindStringSubmatch(joined); m != nil {
		date = m[3] + "-" + m[2] + "-" + m[1]
	}
	return
}

// column header labels in order; the historical dictionary has no Data Cluster column.
var headerLabels = []string{"Key", "Data Point Name", "Description", "Measurement", "Tech", "Data Cluster"}

// columnStarts finds the x positions of the header labels on a page (bold text on the top lines).
func columnStarts(ls []line, withCluster bool) []float64 {
	want := headerLabels
	if !withCluster {
		want = want[:5]
	}
	for _, l := range ls {
		var bold []char
		for _, c := range l.chars {
			if c.bold {
				bold = append(bold, c)
			}
		}
		if len(bold) == 0 {
			continue
		}
		var concat strings.Builder
		var xs []float64
		for _, c := range bold {
			for range c.c {
				xs = append(xs, c.x0)
			}
			concat.WriteString(c.c)
		}
		str := concat.String()
		starts := make([]float64, len(want))
		found := 0
		for k, label := range want {
			if idx := strings.Index(str, label); idx >= 0 && idx < len(xs) {
				starts[k] = xs[idx]
				found++
			}
		}
		if found == len(want) {
			return starts
		}
	}
	return nil
}

type cell struct{ lines []string }

type row struct {
	cells []cell
}

func extract(pages [][]char, withCluster bool) ([]Entry, error) {
	var starts []float64
	var rows []*row
	ncol := 5
	if withCluster {
		ncol = 6
	}
	for pi, page := range pages {
		ls := lines(append([]char(nil), page...))
		if s := columnStarts(ls, withCluster); s != nil {
			starts = s
		}
		if starts == nil {
			continue // title / explanation pages before the table
		}
		for _, l := range ls {
			// Skip header lines.
			allBold := true
			for _, c := range l.chars {
				allBold = allBold && c.bold
			}
			if allBold {
				continue
			}
			cols := make([][]char, ncol)
			for _, c := range l.chars {
				k := 0
				for j := range starts {
					if c.x0+2 >= starts[j] {
						k = j
					}
				}
				cols[k] = append(cols[k], c)
			}
			key := text(cols[0])
			if uuidRE.MatchString(key) {
				rows = append(rows, &row{cells: make([]cell, ncol)})
			} else if key != "" {
				// Page numbers / footers in the key column are ignored.
				if _, err := strconv.Atoi(key); err == nil {
					continue
				}
			}
			if len(rows) == 0 {
				continue
			}
			r := rows[len(rows)-1]
			for k := range ncol {
				if t := text(cols[k]); t != "" {
					r.cells[k].lines = append(r.cells[k].lines, t)
				}
			}
		}
		_ = pi
	}
	seen := map[string]int{}
	var out []Entry
	for _, r := range rows {
		e := Entry{
			Key:         strings.Join(r.cells[0].lines, ""),
			Name:        clean(joinName(r.cells[1].lines)),
			Description: clean(joinText(r.cells[2].lines)),
			Unit:        cleanUnit(clean(joinText(r.cells[3].lines))),
			DataType:    cleanUnit(clean(joinName(r.cells[4].lines))),
		}
		if withCluster {
			for _, c := range strings.Split(clean(joinText(r.cells[5].lines)), ",") {
				if c = strings.TrimSpace(c); c != "" {
					e.Clusters = append(e.Clusters, c)
				}
			}
		}
		if i, dup := seen[e.Key]; dup {
			// The documents repeat some keys; keep the first, merge clusters.
			for _, c := range e.Clusters {
				if !contains(out[i].Clusters, c) {
					out[i].Clusters = append(out[i].Clusters, c)
				}
			}
			continue
		}
		seen[e.Key] = len(out)
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("no table rows found (is this a dictionary PDF?)")
	}
	return out, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// joinName joins wrapped identifier-like cells without spaces ("registrati" + "onCountry"), and prose names with
// spaces ("Remaining Charge Time" + "Complete").
func joinName(ls []string) string {
	var b strings.Builder
	for i, l := range ls {
		if i > 0 {
			prev := b.String()
			if strings.ContainsRune(prev, ' ') || strings.ContainsRune(l, ' ') || !identLike(prev) || !identLike(l) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(l)
	}
	return strings.TrimSpace(b.String())
}

func identLike(s string) bool {
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_.[]*()-:", r)) { //nolint:staticcheck // readability
			return false
		}
	}
	return s != ""
}

func joinText(ls []string) string { return strings.TrimSpace(strings.Join(ls, " ")) }

// clean undoes Ghostscript's double escaping (&gt;) and UTF-8 bytes emitted as Latin-1 characters (Â°C → °C).
func clean(s string) string {
	s = html.UnescapeString(s)
	latin := true
	high := false
	for _, r := range s {
		if r > 0xFF {
			latin = false
			break
		}
		if r >= 0x80 {
			high = true
		}
	}
	if latin && high {
		b := make([]byte, 0, len(s))
		for _, r := range s {
			b = append(b, byte(r)) //nolint:gosec // r <= 0xFF checked above
		}
		if utf8.Valid(b) {
			s = string(b)
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func cleanUnit(u string) string {
	u = strings.TrimSpace(u)
	switch u {
	case "'-", "-", "'":
		return ""
	}
	return u
}
