// Package dictionary owns signal metadata (ADR-0006): the bundled official dictionaries (converted from the
// Volkswagen Group EU Data Act PDFs by tools/dictgen), operator-imported newer versions, the unit registry and the
// mapping from free-text data types to value kinds. A dictionary describes what *may* be delivered; it is not an API
// contract and says nothing about what a given vehicle sends.
package dictionary

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

//go:embed bundled/*.json
var bundled embed.FS

// Format is the JSON format produced by tools/dictgen.
const Format = "wagenpulse.dictionary.v1"

// Entry is one dictionary row.
type Entry struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	DataType    string   `json:"data_type,omitempty"`
	Clusters    []string `json:"clusters,omitempty"`
}

// Document is a dictionary version.
type Document struct {
	Format          string  `json:"format"`
	Kind            string  `json:"kind"`
	Title           string  `json:"title"`
	DocumentVersion string  `json:"document_version"`
	DocumentDate    string  `json:"document_date"`
	SourceFile      string  `json:"source_file"`
	SourceSHA256    string  `json:"source_sha256"`
	Publisher       string  `json:"publisher"`
	Entries         []Entry `json:"entries"`
}

var (
	keyRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// MaxImportBytes bounds an imported dictionary file.
const MaxImportBytes = 16 << 20

// Parse reads and validates a dictionary document.
func Parse(r io.Reader) (*Document, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxImportBytes {
		return nil, apperr.New(apperr.CodeInvalid, "dictionary file is larger than %d MiB", MaxImportBytes>>20)
	}
	var d Document
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, apperr.New(apperr.CodeInvalid, "dictionary file is not valid %s JSON: %s", Format, err.Error())
	}
	v := &apperr.Validation{}
	if d.Format != Format {
		v.Add("format", "must be %q (convert the official PDF with tools/dictgen)", Format)
	}
	if d.Kind != "continuous" && d.Kind != "historical" {
		v.Add("kind", "must be continuous or historical")
	}
	if d.DocumentVersion == "" || len(d.DocumentVersion) > 32 {
		v.Add("document_version", "is required")
	}
	if !dateRE.MatchString(d.DocumentDate) {
		v.Add("document_date", "must be YYYY-MM-DD")
	}
	if len(d.Entries) == 0 || len(d.Entries) > 100000 {
		v.Add("entries", "must contain 1..100000 entries")
	}
	seen := map[string]bool{}
	for i, e := range d.Entries {
		if !keyRE.MatchString(e.Key) {
			v.Add(fmt.Sprintf("entries[%d].key", i), "must be a lowercase UUID")
			break
		}
		if seen[e.Key] {
			v.Add(fmt.Sprintf("entries[%d].key", i), "duplicate key %s", e.Key)
			break
		}
		seen[e.Key] = true
		if e.Name == "" || len(e.Name) > 512 || len(e.Description) > 4096 || len(e.Unit) > 128 || len(e.DataType) > 128 {
			v.Add(fmt.Sprintf("entries[%d]", i), "name is required and fields must be within size limits")
			break
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if d.SourceSHA256 == "" {
		sum := sha256.Sum256(b)
		d.SourceSHA256 = hex.EncodeToString(sum[:])
	}
	return &d, nil
}

// Bundled returns the dictionaries compiled into the binary.
func Bundled() ([]*Document, error) {
	entries, err := bundled.ReadDir("bundled")
	if err != nil {
		return nil, err
	}
	var out []*Document
	for _, e := range entries {
		f, err := bundled.Open("bundled/" + e.Name())
		if err != nil {
			return nil, err
		}
		d, err := Parse(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("bundled dictionary %s: %w", e.Name(), err)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// Version is a loaded dictionary version.
type Version struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	DocumentVersion string `json:"document_version"`
	DocumentDate    string `json:"document_date"`
	SourceFile      string `json:"source_file"`
	SourceSHA256    string `json:"source_sha256"`
	Origin          string `json:"origin" enum:"bundled,imported"`
	EntryCount      int64  `json:"entry_count"`
	LoadedAt        int64  `json:"loaded_at"`
}

// Service loads dictionaries and serves signal metadata.
type Service struct {
	s     *store.Store
	audit *audit.Writer
}

// New returns a dictionary service.
func New(s *store.Store, a *audit.Writer) *Service { return &Service{s: s, audit: a} }

// batchSize bounds the entries written per transaction (ADR-0002). Each entry costs two upserts; at ~5 ms per entry under
// -race on a loaded runner, 100 entries took 500-840 ms, so 20 keeps every transaction near 100 ms even there.
const batchSize = 20

// LoadBundled loads every bundled dictionary that is not loaded yet (idempotent; called at startup).
func (svc *Service) LoadBundled(ctx context.Context) error {
	docs, err := Bundled()
	if err != nil {
		return err
	}
	for _, d := range docs {
		if _, _, err := svc.load(ctx, d, "bundled", audit.System); err != nil {
			return err
		}
	}
	return nil
}

// Import loads an operator-supplied dictionary version.
func (svc *Service) Import(ctx context.Context, actor audit.Actor, d *Document) (Version, bool, error) {
	return svc.load(ctx, d, "imported", actor)
}

// load stores a version (unless its source hash is already loaded) and refreshes signal metadata in batches. The
// newest document (by document date, then load order) of each kind describes a signal.
func (svc *Service) load(ctx context.Context, d *Document, origin string, actor audit.Actor) (Version, bool, error) {
	var existing Version
	err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT id FROM dictionary_versions WHERE source_sha256 = ?`, d.SourceSHA256).Scan(&existing.ID)
	if err == nil {
		v, err := svc.GetVersion(ctx, existing.ID)
		return v, false, err
	}
	id := ids.New(ids.Dictionary)
	err = svc.s.W().TxNamed(ctx, "dictionary.version", func(tx *store.Tx) error {
		// Re-check inside the write transaction: another process (a server starting while an administrative command
		// attaches, ADR-0027) may have loaded the same document since the read above.
		var other string
		switch err := tx.SQL.QueryRowContext(ctx, `SELECT id FROM dictionary_versions WHERE source_sha256 = ?`, d.SourceSHA256).Scan(&other); {
		case err == nil:
			existing.ID = other
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO dictionary_versions (id, kind, title, document_version, document_date, source_sha256,
			source_file, origin, entry_count, loaded_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, d.Kind, d.Title, d.DocumentVersion,
			d.DocumentDate, d.SourceSHA256, d.SourceFile, origin, len(d.Entries), tx.NowMicros()); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "dictionary.loaded", Target: audit.Target{Type: "dictionary", ID: id},
			Details: map[string]any{"kind": d.Kind, "document_version": d.DocumentVersion, "document_date": d.DocumentDate, "entries": len(d.Entries), "origin": origin}})
		return err
	})
	if err != nil {
		return Version{}, false, err
	}
	if existing.ID != "" {
		v, err := svc.GetVersion(ctx, existing.ID)
		return v, false, err
	}
	for start := 0; start < len(d.Entries); start += batchSize {
		end := min(start+batchSize, len(d.Entries))
		chunk := d.Entries[start:end]
		err := svc.s.W().TxNamed(ctx, "dictionary.entries", func(tx *store.Tx) error {
			for _, e := range chunk {
				cj, _ := json.Marshal(nonNil(e.Clusters))
				if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO dictionary_entries (version_id, key, name, description, unit, data_type, clusters_json)
					VALUES (?, ?, ?, ?, ?, ?, ?)`, id, e.Key, e.Name, e.Description, e.Unit, e.DataType, string(cj)); err != nil {
					return err
				}
				if err := upsertSignal(ctx, tx, id, d.Kind, d.DocumentDate, e, string(cj)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return Version{}, false, err
		}
	}
	v, err := svc.GetVersion(ctx, id)
	return v, true, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// upsertSignal describes a signal from this version unless a version with a newer document date already does: each
// key is described by the newest document that contains it; observed-only signals gain metadata.
func upsertSignal(ctx context.Context, tx *store.Tx, versionID, kind, date string, e Entry, clusters string) error {
	_, err := tx.SQL.ExecContext(ctx, `INSERT INTO signals (key, name, description, unit_raw, unit, data_type_raw, value_kind, clusters_json, origin,
		dictionary_version_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET name = excluded.name, description = excluded.description, unit_raw = excluded.unit_raw,
		  unit = excluded.unit, data_type_raw = excluded.data_type_raw, value_kind = excluded.value_kind, clusters_json = excluded.clusters_json,
		  origin = excluded.origin, dictionary_version_id = excluded.dictionary_version_id, updated_at = excluded.updated_at
		WHERE signals.dictionary_version_id IS NULL
		   OR (SELECT document_date FROM dictionary_versions WHERE id = signals.dictionary_version_id) <= ?`,
		e.Key, e.Name, e.Description, e.Unit, NormalizeUnit(e.Unit), e.DataType, string(KindOf(e.DataType)), clusters, kind, versionID, tx.NowMicros(), date)
	return err
}

// GetVersion returns a loaded version.
func (svc *Service) GetVersion(ctx context.Context, id string) (Version, error) {
	var v Version
	err := svc.s.ReadDB().QueryRowContext(ctx, `SELECT id, kind, title, document_version, document_date, source_file, source_sha256, origin,
		entry_count, loaded_at FROM dictionary_versions WHERE id = ?`, id).Scan(&v.ID, &v.Kind, &v.Title, &v.DocumentVersion, &v.DocumentDate,
		&v.SourceFile, &v.SourceSHA256, &v.Origin, &v.EntryCount, &v.LoadedAt)
	if errors.Is(err, errNoRows) {
		return v, apperr.New(apperr.CodeNotFound, "dictionary version not found")
	}
	return v, err
}

// Versions lists loaded versions, newest first.
func (svc *Service) Versions(ctx context.Context) ([]Version, error) {
	rows, err := svc.s.ReadDB().QueryContext(ctx, `SELECT id, kind, title, document_version, document_date, source_file, source_sha256, origin,
		entry_count, loaded_at FROM dictionary_versions ORDER BY kind, document_date DESC, loaded_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.Kind, &v.Title, &v.DocumentVersion, &v.DocumentDate, &v.SourceFile, &v.SourceSHA256, &v.Origin,
			&v.EntryCount, &v.LoadedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
