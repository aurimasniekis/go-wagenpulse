// Package vehicles manages vehicles: identity (VIN), descriptive metadata, archive and explicit telemetry purge
// (ADR-0023). Deleting a vehicle through the API archives it; purging telemetry is a separate, deliberate operation.
package vehicles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aurimasniekis/go-wagenpulse/internal/apperr"
	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/ids"
	"github.com/aurimasniekis/go-wagenpulse/internal/obs"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// Brands supported by the Volkswagen Group EU Data Act portal (plus "other").
var Brands = []string{"volkswagen", "volkswagen_commercial", "audi", "skoda", "seat", "cupra", "bentley", "other"}

// Vehicle is the API representation.
type Vehicle struct {
	ID                string     `json:"id"`
	Slug              string     `json:"slug" doc:"Lowercase identifier used in event subjects (never the VIN)."`
	DisplayName       string     `json:"display_name"`
	VIN               string     `json:"vin"`
	Brand             string     `json:"brand" enum:"volkswagen,volkswagen_commercial,audi,skoda,seat,cupra,bentley,other"`
	Model             string     `json:"model"`
	ModelYear         *int       `json:"model_year,omitempty"`
	LicensePlate      string     `json:"license_plate"`
	Notes             string     `json:"notes"`
	Tags              []string   `json:"tags"`
	StaleAfterMinutes *int       `json:"stale_after_minutes,omitempty" doc:"Override of the system stale threshold for this vehicle."`
	Archived          bool       `json:"archived"`
	ArchivedAt        *time.Time `json:"archived_at,omitempty"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Num               int64      `json:"-"`
}

// Input creates or replaces a vehicle's configuration.
type Input struct {
	Slug              string
	DisplayName       string
	VIN               string
	Brand             string
	Model             string
	ModelYear         *int
	LicensePlate      string
	Notes             string
	Tags              []string
	StaleAfterMinutes *int
}

// Service manages vehicles.
type Service struct {
	s     *store.Store
	audit *audit.Writer
}

// New returns a service.
func New(s *store.Store, a *audit.Writer) *Service { return &Service{s: s, audit: a} }

var (
	vinRE  = regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`)
	slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	tagRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,39}$`)
)

// ValidVIN reports whether s is a syntactically valid VIN (ISO 3779 character set; no I, O, Q).
func ValidVIN(s string) bool { return vinRE.MatchString(s) }

// SlugFrom derives a slug from a display name.
func SlugFrom(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	if out == "" {
		out = "vehicle"
	}
	return out
}

func (in *Input) normalize(creating bool) error {
	v := &apperr.Validation{}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if l := len([]rune(in.DisplayName)); l == 0 || l > 100 {
		v.Add("display_name", "must be 1..100 characters")
	}
	in.VIN = strings.ToUpper(strings.TrimSpace(in.VIN))
	if !ValidVIN(in.VIN) {
		v.Add("vin", "must be a 17-character VIN (letters except I, O, Q and digits)")
	}
	if in.Slug == "" && creating {
		in.Slug = SlugFrom(in.DisplayName)
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !slugRE.MatchString(in.Slug) {
		v.Add("slug", "must be 1..48 lowercase letters, digits, '_' or '-' (used in event subjects)")
	}
	if in.Brand == "" {
		in.Brand = "other"
	}
	if !slices.Contains(Brands, in.Brand) {
		v.Add("brand", "must be one of %s", strings.Join(Brands, ", "))
	}
	in.Model, in.LicensePlate, in.Notes = strings.TrimSpace(in.Model), strings.TrimSpace(in.LicensePlate), strings.TrimSpace(in.Notes)
	if len(in.Model) > 100 {
		v.Add("model", "must be at most 100 characters")
	}
	if len(in.LicensePlate) > 20 {
		v.Add("license_plate", "must be at most 20 characters")
	}
	if len(in.Notes) > 2000 {
		v.Add("notes", "must be at most 2000 characters")
	}
	if in.ModelYear != nil && (*in.ModelYear < 1950 || *in.ModelYear > 2100) {
		v.Add("model_year", "must be within 1950..2100")
	}
	if in.StaleAfterMinutes != nil && (*in.StaleAfterMinutes < 1 || *in.StaleAfterMinutes > 60*24*30) {
		v.Add("stale_after_minutes", "must be within 1..43200")
	}
	tags := make([]string, 0, len(in.Tags))
	for _, t := range in.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if !tagRE.MatchString(t) {
			v.Add("tags", "invalid tag %q (lowercase, 1..40 of a-z 0-9 _ . : -)", t)
			continue
		}
		tags = append(tags, t)
	}
	slices.Sort(tags)
	in.Tags = slices.Compact(tags)
	if len(in.Tags) > 20 {
		v.Add("tags", "at most 20 tags")
	}
	return v.Err()
}

const cols = `num, id, slug, display_name, vin, brand, model, model_year, license_plate, notes, tags_json, stale_after_minutes,
	archived_at, revision, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scan(r scanner) (Vehicle, error) {
	var v Vehicle
	var year, stale, archived sql.NullInt64
	var tags string
	var created, updated int64
	if err := r.Scan(&v.Num, &v.ID, &v.Slug, &v.DisplayName, &v.VIN, &v.Brand, &v.Model, &year, &v.LicensePlate, &v.Notes, &tags, &stale,
		&archived, &v.Revision, &created, &updated); err != nil {
		return v, err
	}
	if year.Valid {
		y := int(year.Int64)
		v.ModelYear = &y
	}
	if stale.Valid {
		s := int(stale.Int64)
		v.StaleAfterMinutes = &s
	}
	if archived.Valid {
		t := time.UnixMicro(archived.Int64).UTC()
		v.Archived, v.ArchivedAt = true, &t
	}
	v.Tags = []string{}
	_ = json.Unmarshal([]byte(tags), &v.Tags)
	v.CreatedAt, v.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	return v, nil
}

// ErrNotFound is returned for unknown vehicles.
var ErrNotFound = apperr.New(apperr.CodeNotFound, "vehicle not found")

// Get returns a vehicle by ID or slug.
func (svc *Service) Get(ctx context.Context, idOrSlug string) (Vehicle, error) {
	return get(ctx, svc.s.ReadDB(), idOrSlug)
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func get(ctx context.Context, q querier, idOrSlug string) (Vehicle, error) {
	col := "slug"
	if ids.Valid(ids.Vehicle, idOrSlug) {
		col = "id"
	}
	v, err := scan(q.QueryRowContext(ctx, `SELECT `+cols+` FROM vehicles WHERE `+col+` = ?`, idOrSlug))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ByNum returns a vehicle by internal number.
func (svc *Service) ByNum(ctx context.Context, num int64) (Vehicle, error) {
	v, err := scan(svc.s.ReadDB().QueryRowContext(ctx, `SELECT `+cols+` FROM vehicles WHERE num = ?`, num))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ByVIN returns the active vehicle with a VIN.
func ByVIN(ctx context.Context, q querier, vin string) (Vehicle, error) {
	v, err := scan(q.QueryRowContext(ctx, `SELECT `+cols+` FROM vehicles WHERE vin = ? AND archived_at IS NULL`, strings.ToUpper(vin)))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ListFilter selects vehicles.
type ListFilter struct {
	IncludeArchived bool
	OnlyIDs         []string // token vehicle restriction (empty = all)
	After           string
	Limit           int
}

// List returns vehicles ordered by ID.
func (svc *Service) List(ctx context.Context, f ListFilter) ([]Vehicle, error) {
	q := `SELECT ` + cols + ` FROM vehicles WHERE id > ?`
	args := []any{f.After}
	if !f.IncludeArchived {
		q += ` AND archived_at IS NULL`
	}
	if len(f.OnlyIDs) > 0 {
		q += ` AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.OnlyIDs)), ",") + `)`
		for _, id := range f.OnlyIDs {
			args = append(args, id)
		}
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, f.Limit)
	rows, err := svc.s.ReadDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Vehicle{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// Create adds a vehicle. Recreating a vehicle with the VIN of an archived one is allowed (history stays with the
// archived record).
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in Input) (Vehicle, error) {
	if err := in.normalize(true); err != nil {
		return Vehicle{}, err
	}
	id := ids.New(ids.Vehicle)
	tags, _ := json.Marshal(in.Tags)
	err := svc.s.W().TxNamed(ctx, "vehicles.create", func(tx *store.Tx) error {
		if _, err := ByVIN(ctx, tx.SQL, in.VIN); err == nil {
			return apperr.New(apperr.CodeAlreadyExists, "an active vehicle with this VIN already exists")
		}
		if _, err := get(ctx, tx.SQL, in.Slug); err == nil {
			return apperr.New(apperr.CodeAlreadyExists, "a vehicle with slug %q already exists", in.Slug)
		}
		if _, err := tx.SQL.ExecContext(ctx, `INSERT INTO vehicles (id, slug, display_name, vin, brand, model, model_year, license_plate, notes,
			tags_json, stale_after_minutes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Slug, in.DisplayName,
			in.VIN, in.Brand, in.Model, nullInt(in.ModelYear), in.LicensePlate, in.Notes, string(tags), nullInt(in.StaleAfterMinutes),
			tx.NowMicros(), tx.NowMicros()); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "vehicle.created", Target: audit.Target{Type: "vehicle", ID: id},
			Details: map[string]any{"slug": in.Slug, "brand": in.Brand, "vin_suffix": obs.MaskVIN(in.VIN)}})
		return err
	})
	if err != nil {
		return Vehicle{}, err
	}
	return svc.Get(ctx, id)
}

// Update replaces a vehicle's configuration (ifRevision 0 = unconditional). The VIN is immutable.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, idOrSlug string, ifRevision int64, in Input) (Vehicle, error) {
	cur, err := svc.Get(ctx, idOrSlug)
	if err != nil {
		return Vehicle{}, err
	}
	if in.VIN == "" {
		in.VIN = cur.VIN
	}
	if err := in.normalize(false); err != nil {
		return Vehicle{}, err
	}
	if in.VIN != cur.VIN {
		return Vehicle{}, apperr.New(apperr.CodeInvalid, "vin: the VIN of a vehicle cannot change; create a new vehicle instead")
	}
	tags, _ := json.Marshal(in.Tags)
	err = svc.s.W().TxNamed(ctx, "vehicles.update", func(tx *store.Tx) error {
		if in.Slug != cur.Slug {
			if _, err := get(ctx, tx.SQL, in.Slug); err == nil {
				return apperr.New(apperr.CodeAlreadyExists, "a vehicle with slug %q already exists", in.Slug)
			}
		}
		rev := cur.Revision
		if ifRevision != 0 {
			rev = ifRevision
		}
		res, err := tx.SQL.ExecContext(ctx, `UPDATE vehicles SET slug = ?, display_name = ?, brand = ?, model = ?, model_year = ?, license_plate = ?,
			notes = ?, tags_json = ?, stale_after_minutes = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`,
			in.Slug, in.DisplayName, in.Brand, in.Model, nullInt(in.ModelYear), in.LicensePlate, in.Notes, string(tags), nullInt(in.StaleAfterMinutes),
			tx.NowMicros(), cur.ID, rev)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return apperr.New(apperr.CodePrecondition, "the vehicle was changed by someone else; reload and retry")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "vehicle.updated", Target: audit.Target{Type: "vehicle", ID: cur.ID},
			Details: map[string]any{"slug": in.Slug, "brand": in.Brand}})
		return err
	})
	if err != nil {
		return Vehicle{}, err
	}
	return svc.Get(ctx, cur.ID)
}

// Archive hides a vehicle and stops collecting for it; telemetry, events and deliveries are retained (ADR-0023).
// Archiving an archived vehicle is a no-op.
func (svc *Service) Archive(ctx context.Context, actor audit.Actor, idOrSlug string) error {
	cur, err := svc.Get(ctx, idOrSlug)
	if err != nil {
		return err
	}
	if cur.Archived {
		return nil
	}
	return svc.s.W().TxNamed(ctx, "vehicles.archive", func(tx *store.Tx) error {
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE vehicles SET archived_at = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			tx.NowMicros(), tx.NowMicros(), cur.ID); err != nil {
			return err
		}
		if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM source_vehicles WHERE vehicle_num = ?`, cur.Num); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "vehicle.archived", Target: audit.Target{Type: "vehicle", ID: cur.ID}})
		return err
	})
}

// Restore un-archives a vehicle (refused when another active vehicle now has its VIN).
func (svc *Service) Restore(ctx context.Context, actor audit.Actor, idOrSlug string) (Vehicle, error) {
	cur, err := svc.Get(ctx, idOrSlug)
	if err != nil {
		return Vehicle{}, err
	}
	err = svc.s.W().TxNamed(ctx, "vehicles.restore", func(tx *store.Tx) error {
		if other, err := ByVIN(ctx, tx.SQL, cur.VIN); err == nil && other.ID != cur.ID {
			return apperr.New(apperr.CodeConflict, "another active vehicle uses this VIN")
		}
		if _, err := tx.SQL.ExecContext(ctx, `UPDATE vehicles SET archived_at = NULL, revision = revision + 1, updated_at = ? WHERE id = ?`,
			tx.NowMicros(), cur.ID); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "vehicle.restored", Target: audit.Target{Type: "vehicle", ID: cur.ID}})
		return err
	})
	if err != nil {
		return Vehicle{}, err
	}
	return svc.Get(ctx, cur.ID)
}

// PurgeResult reports what a purge deleted.
type PurgeResult struct {
	Observations int64 `json:"observations"`
	States       int64 `json:"latest_states"`
	Rollups      int64 `json:"rollups"`
	Deleted      bool  `json:"vehicle_deleted"`
}

// Purge deletes a vehicle's telemetry in bounded batches and, when deleteVehicle is set, the archived vehicle record
// itself. It requires the vehicle to be archived first: an active vehicle could receive new data concurrently.
func (svc *Service) Purge(ctx context.Context, actor audit.Actor, idOrSlug string, deleteVehicle bool) (PurgeResult, error) {
	cur, err := svc.Get(ctx, idOrSlug)
	if err != nil {
		return PurgeResult{}, err
	}
	if !cur.Archived {
		return PurgeResult{}, apperr.New(apperr.CodeConflict, "archive the vehicle before purging its data")
	}
	var res PurgeResult
	for {
		var n int64
		err := svc.s.W().TxNamed(ctx, "vehicles.purge_batch", func(tx *store.Tx) error {
			r, err := tx.SQL.ExecContext(ctx, `DELETE FROM observations WHERE vehicle_num = ? AND (signal_num, observed_at, value_hash) IN
				(SELECT signal_num, observed_at, value_hash FROM observations WHERE vehicle_num = ? LIMIT 5000)`, cur.Num, cur.Num)
			if err != nil {
				return err
			}
			n, _ = r.RowsAffected()
			return nil
		})
		if err != nil {
			return res, err
		}
		res.Observations += n
		if n == 0 {
			break
		}
	}
	err = svc.s.W().TxNamed(ctx, "vehicles.purge", func(tx *store.Tx) error {
		r, err := tx.SQL.ExecContext(ctx, `DELETE FROM latest_state WHERE vehicle_num = ?`, cur.Num)
		if err != nil {
			return err
		}
		res.States, _ = r.RowsAffected()
		if r, err = tx.SQL.ExecContext(ctx, `DELETE FROM observation_rollups WHERE vehicle_num = ?`, cur.Num); err != nil {
			return err
		}
		res.Rollups, _ = r.RowsAffected()
		if deleteVehicle {
			if _, err := tx.SQL.ExecContext(ctx, `DELETE FROM vehicles WHERE num = ?`, cur.Num); err != nil {
				return err
			}
			res.Deleted = true
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "vehicle.purged", Target: audit.Target{Type: "vehicle", ID: cur.ID},
			Details: map[string]any{"observations": res.Observations, "latest_states": res.States, "vehicle_deleted": deleteVehicle}})
		return err
	})
	return res, err
}
