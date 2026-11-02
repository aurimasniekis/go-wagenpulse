package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/backup"
	"github.com/aurimasniekis/go-wagenpulse/internal/buildinfo"
)

// runBackupCreate writes a consistent backup while the server runs (or not); ADR-0025, ADR-0027.
func runBackupCreate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup create", stderr)
	cfgPath := configFlag(fs)
	out := fs.String("out", "", "directory to create for the backup (must not exist; keep it off the data volume)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return &exitError{code: 2, err: errors.New("--out is required")}
	}
	a, err := openAppOnline(ctx, *cfgPath, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	start := time.Now()
	m, err := backup.Create(ctx, a.Store, *out, buildinfo.Get().Version)
	if err != nil {
		return err
	}
	var size int64
	for _, f := range m.Files {
		size += f.Size
	}
	abs, _ := filepath.Abs(*out)
	if _, err := fmt.Fprintf(stdout, "Backup written to %s\n  schema version %d, %d files, %d bytes, %s\n", abs, m.SchemaVersion,
		len(m.Files), size, time.Since(start).Round(time.Millisecond)); err != nil {
		return err
	}
	if len(m.MissingBlobs) > 0 {
		_, err = fmt.Fprintf(stdout, "  %d raw delivery file(s) were removed by retention while the backup ran; their data is intact but they cannot be reprocessed\n", len(m.MissingBlobs))
	}
	if err == nil {
		_, err = fmt.Fprintln(stdout, "The KEK is not part of the backup. Keep it separately: the backup cannot be opened without it.")
	}
	return err
}

func runBackupVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("backup verify", stderr)
	dir := fs.String("dir", "", "backup directory to verify")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return &exitError{code: 2, err: errors.New("--dir is required")}
	}
	rep, err := backup.Verify(ctx, *dir)
	if err != nil {
		return &exitError{code: 3, err: fmt.Errorf("backup verification FAILED: %w", err)}
	}
	m := rep.Manifest
	_, err = fmt.Fprintf(stdout, "Backup OK: created %s by %s, schema %d (this binary: up to %d), %d files, installation %s\n",
		m.CreatedAt.Format(time.RFC3339), m.AppVersion, m.SchemaVersion, rep.LatestSchema, len(m.Files), m.InstallationID)
	return err
}

// runRestore restores into the configured (empty) data directory. The server must be stopped.
func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("restore", stderr)
	cfgPath := configFlag(fs)
	from := fs.String("from", "", "backup directory to restore")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *from == "" {
		return &exitError{code: 2, err: errors.New("--from is required")}
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	rep, err := backup.Restore(ctx, *from, cfg.DataDir)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Restored %d files into %s (schema %d). Start the server with the same KEK as the backed-up installation;\n"+
		"pending migrations are applied on start.\n", len(rep.Manifest.Files), cfg.DataDir, rep.Manifest.SchemaVersion)
	return err
}
