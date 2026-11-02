package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
)

// OpenFileReadOnly opens a WagenPulse database file (a backup copy, for example) read-only, without the data-directory
// lock and without migrating. The caller closes it.
func OpenFileReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	q := baseDSN(DefaultBusyTimeout)
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	d, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// FileSchemaVersion reports the schema version of an opened database file and the latest version this binary knows.
func FileSchemaVersion(ctx context.Context, d *sql.DB) (current, latest int64, err error) {
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return 0, 0, err
	}
	return SchemaVersions(ctx, d, sub)
}

// FileIntegrityCheck runs PRAGMA integrity_check on an opened database file.
func FileIntegrityCheck(ctx context.Context, d *sql.DB) error {
	if err := check(ctx, d, "integrity_check"); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	return nil
}
