package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/audit"
	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// RetentionResult reports what a retention run deleted (ADR-0024).
type RetentionResult struct {
	Observations int64 `json:"observations"`
	Rollups      int64 `json:"rollups"`
	RawBlobs     int64 `json:"raw_blobs"`
	Events       int64 `json:"events"`
	AuditEvents  int64 `json:"audit_events"`
	Imports      int64 `json:"imports"`
	Jobs         int64 `json:"jobs"`
	Operations   int64 `json:"operations"`
}

const retentionBatch = 5000

// runRetention applies the retention settings in bounded batches (short write transactions; blob files are removed
// outside transactions).
func (a *App) runRetention(ctx context.Context) (RetentionResult, error) {
	doc, err := a.Settings.Retention(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	r := doc.Value
	now := a.Clock.Now()
	var res RetentionResult
	cutoff := func(days int) int64 { return now.Add(-time.Duration(days) * 24 * time.Hour).UnixMicro() }
	batched := func(name, q string, arg int64, out *int64) error {
		for {
			var n int64
			err := a.Store.W().TxNamed(ctx, "retention."+name, func(tx *store.Tx) error {
				r, err := tx.SQL.ExecContext(ctx, q, arg)
				if err != nil {
					return err
				}
				n, _ = r.RowsAffected()
				return nil
			})
			if err != nil {
				return err
			}
			*out += n
			if n < retentionBatch {
				return nil
			}
		}
	}
	if r.ObservationsDays > 0 {
		c := cutoff(r.ObservationsDays)
		if err := batched("observations", `DELETE FROM observations WHERE (vehicle_num, signal_num, observed_at, value_hash) IN
			(SELECT vehicle_num, signal_num, observed_at, value_hash FROM observations WHERE observed_at < ? LIMIT 5000)`, c, &res.Observations); err != nil {
			return res, err
		}
		if err := batched("rollups", `DELETE FROM observation_rollups WHERE (vehicle_num, signal_num, bucket_start) IN
			(SELECT vehicle_num, signal_num, bucket_start FROM observation_rollups WHERE bucket_start < ? LIMIT 5000)`, c, &res.Rollups); err != nil {
			return res, err
		}
	}
	// Raw delivery blobs past their retention date.
	for {
		rows, err := a.Store.ReadDB().QueryContext(ctx, `SELECT num, sha256 FROM raw_deliveries WHERE blob_present = 1 AND retention_until IS NOT NULL
			AND retention_until < ? LIMIT 500`, now.UnixMicro())
		if err != nil {
			return res, err
		}
		type blob struct {
			num int64
			sha []byte
		}
		var bs []blob
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var b blob
				if err := rows.Scan(&b.num, &b.sha); err != nil {
					return err
				}
				bs = append(bs, b)
			}
			return rows.Err()
		}(); err != nil {
			return res, err
		}
		if len(bs) == 0 {
			break
		}
		for _, b := range bs {
			if err := a.Domain.Ingest.Blobs().Delete(b.sha); err != nil {
				return res, err
			}
			if err := a.Store.W().TxNamed(ctx, "retention.blob", func(tx *store.Tx) error {
				_, err := tx.SQL.ExecContext(ctx, `UPDATE raw_deliveries SET blob_present = 0 WHERE num = ?`, b.num)
				return err
			}); err != nil {
				return res, err
			}
			res.RawBlobs++
		}
	}
	if r.EventsDays > 0 {
		// Events whose deliveries are all terminal (or none) are deleted with their deliveries and attempts.
		if err := batched("events", `DELETE FROM events WHERE num IN (SELECT e.num FROM events e WHERE e.created_at < ? AND NOT EXISTS
			(SELECT 1 FROM deliveries d WHERE d.event_num = e.num AND d.state IN ('pending', 'processing', 'retrying')) LIMIT 5000)`,
			cutoff(r.EventsDays), &res.Events); err != nil {
			return res, err
		}
	}
	if r.AuditDays > 0 {
		if err := a.purgeAudit(ctx, cutoff(r.AuditDays), &res); err != nil {
			return res, err
		}
	}
	fc := cutoff(r.FinishedJobsDays)
	err = a.Store.W().TxNamed(ctx, "retention.finished", func(tx *store.Tx) error {
		r1, err := tx.SQL.ExecContext(ctx, `DELETE FROM imports WHERE state IN ('succeeded', 'partial', 'failed', 'cancelled') AND updated_at < ?`, fc)
		if err != nil {
			return err
		}
		res.Imports, _ = r1.RowsAffected()
		if res.Jobs, err = tx.Q.PurgeFinishedJobs(ctx, fc); err != nil {
			return err
		}
		if res.Operations, err = tx.Q.PurgeFinishedOperations(ctx, fc); err != nil {
			return err
		}
		_, err = a.Audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "retention.applied", Target: audit.Target{Type: "settings", ID: "retention"},
			Details: map[string]any{"observations": res.Observations, "raw_blobs": res.RawBlobs, "events": res.Events, "audit_events": res.AuditEvents,
				"imports": res.Imports}})
		return err
	})
	if err != nil {
		return res, err
	}
	_ = a.Store.W().Exec(ctx, "PRAGMA incremental_vacuum(2000)")
	return res, nil
}

// purgeAudit deletes audit events older than cutoff and records the chain anchor so verification still works.
func (a *App) purgeAudit(ctx context.Context, cutoff int64, res *RetentionResult) error {
	return a.Store.W().TxNamed(ctx, "retention.audit", func(tx *store.Tx) error {
		row, err := tx.Q.LastAuditSeqBefore(ctx, cutoff)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		anchor, _ := json.Marshal(audit.Anchor{Seq: row.Seq, Hash: row.Hash})
		n, err := tx.Q.PurgeAuditThrough(ctx, row.Seq)
		if err != nil {
			return err
		}
		res.AuditEvents = n
		_, err = tx.SQL.ExecContext(ctx, `INSERT INTO settings (key, value, revision, updated_at, updated_by) VALUES (?, ?, 1, ?, 'retention')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, revision = settings.revision + 1, updated_at = excluded.updated_at`,
			audit.AnchorSettingKey, string(anchor), tx.NowMicros())
		return err
	})
}
