package ingest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/dictionary"
	"github.com/aurimasniekis/go-wagenpulse/internal/vehicles"
)

// TestRealSamples imports real portal deliveries from WAGENPULSE_REAL_SAMPLES (a directory or *.zip glob; git-ignored,
// never committed). It logs counts only — never VINs or values — for docs/CAPABILITIES.md.
func TestRealSamples(t *testing.T) {
	dir := os.Getenv("WAGENPULSE_REAL_SAMPLES")
	if dir == "" {
		t.Skip("set WAGENPULSE_REAL_SAMPLES to a directory of real portal ZIP deliveries")
	}
	pattern := filepath.Join(dir, "*.zip")
	if filepath.Ext(dir) == ".zip" {
		pattern = dir // a glob such as ~/Downloads/2026*_*.zip
	}
	files, _ := filepath.Glob(pattern)
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no ZIP files")
	}
	e := newEnv(t)
	ctx := context.Background()
	if err := dictionary.New(e.s, audit.NewWriter(nil)).LoadBundled(ctx); err != nil {
		t.Fatal(err)
	}
	// Find the VIN of the first delivery and create its vehicle.
	f, _ := os.Open(files[0])
	st, _ := f.Stat()
	docs, err := ReadDelivery(f, st.Size(), DefaultLimits)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(docs[0])
	if err != nil || p.Format != FormatContinuousV1 {
		t.Fatalf("format %v %v", p, err)
	}
	if _, err := e.vs.Create(ctx, audit.System, vehicles.Input{DisplayName: "Real sample", VIN: p.VIN, Brand: "cupra"}); err != nil {
		t.Fatal(err)
	}
	var datas [][]byte
	for _, fn := range files {
		b, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		datas = append(datas, b)
	}
	var ds []StoredDelivery
	dups := 0
	for i, b := range datas {
		d, err := e.svc.StoreRaw(ctx, bytes.NewReader(b), RawMeta{Origin: "upload", OriginalName: filepath.Base(files[i]), Limit: 50 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if d.Duplicate {
			dups++
		}
		ds = append(ds, d)
	}
	imp, err := e.svc.Create(ctx, CreateInput{Kind: KindUpload, EventPolicy: "none", Backfill: true, Deliveries: ds, Actor: audit.System})
	if err != nil {
		t.Fatal(err)
	}
	e.run(t, imp.ID)
	imp, _ = e.svc.Get(ctx, imp.ID)
	var known, observed int
	_ = e.s.ReadDB().QueryRow(`SELECT count(*) FROM latest_state l JOIN signals s ON s.num = l.signal_num WHERE s.origin = 'continuous'`).Scan(&known)
	_ = e.s.ReadDB().QueryRow(`SELECT count(*) FROM latest_state l JOIN signals s ON s.num = l.signal_num WHERE s.origin = 'observed'`).Scan(&observed)
	t.Logf("files=%d byte-identical-duplicates-at-store=%d state=%s accepted=%d duplicate-deliveries=%d unknown-signals=%d corrections=%d",
		len(files), dups, imp.State, imp.ObservationsAccepted, imp.DeliveriesDuplicate, imp.UnknownSignals, imp.Corrections)
	t.Logf("latest-state signals: dictionary=%d observed=%d warnings=%d", known, observed, len(imp.Warnings))
}
