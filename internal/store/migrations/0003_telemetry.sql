-- 0003_telemetry: vehicles, sources, raw deliveries, imports, dictionaries, signals, observations, latest state and
-- rollups (ADR-0003, ADR-0005, ADR-0008, ADR-0009, ADR-0013, docs/STORAGE.md).

-- +goose Up
CREATE TABLE vehicles (
  num                 INTEGER PRIMARY KEY,
  id                  TEXT NOT NULL UNIQUE,
  slug                TEXT NOT NULL UNIQUE,
  display_name        TEXT NOT NULL,
  vin                 TEXT NOT NULL CHECK (length(vin) = 17),
  brand               TEXT NOT NULL CHECK (brand IN ('volkswagen', 'volkswagen_commercial', 'audi', 'skoda', 'seat', 'cupra', 'bentley', 'other')),
  model               TEXT NOT NULL DEFAULT '',
  model_year          INTEGER,
  license_plate       TEXT NOT NULL DEFAULT '',
  notes               TEXT NOT NULL DEFAULT '',
  tags_json           TEXT NOT NULL DEFAULT '[]',
  stale_after_minutes INTEGER CHECK (stale_after_minutes IS NULL OR stale_after_minutes > 0),
  archived_at         INTEGER,
  revision            INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT;

-- A VIN belongs to at most one active vehicle; archived vehicles keep their VIN for history.
CREATE UNIQUE INDEX ux_vehicles_active_vin ON vehicles (vin) WHERE archived_at IS NULL;

-- Data sources (ADR-0005). Credentials are sealed (keyring field purpose) or external references.
CREATE TABLE sources (
  id                    TEXT PRIMARY KEY,
  name                  TEXT NOT NULL,
  kind                  TEXT NOT NULL CHECK (kind IN ('vw_data_act_portal')),
  enabled               INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  brand                 TEXT NOT NULL,
  portal_brand_key      TEXT NOT NULL DEFAULT '',
  country               TEXT NOT NULL,
  language              TEXT NOT NULL,
  auth_mode             TEXT NOT NULL CHECK (auth_mode IN ('session_cookie', 'password')),
  username_enc          BLOB,
  secret_enc            BLOB,
  secret_ref            TEXT,
  secret_fingerprint    TEXT NOT NULL DEFAULT '',
  session_enc           BLOB,
  collect_continuous    INTEGER NOT NULL DEFAULT 1 CHECK (collect_continuous IN (0, 1)),
  collect_historical    INTEGER NOT NULL DEFAULT 0 CHECK (collect_historical IN (0, 1)),
  interval_minutes      INTEGER NOT NULL DEFAULT 15 CHECK (interval_minutes BETWEEN 15 AND 1440),
  live_event_policy     TEXT NOT NULL DEFAULT 'publish' CHECK (live_event_policy IN ('none', 'preview', 'publish')),
  historical_event_policy TEXT NOT NULL DEFAULT 'none' CHECK (historical_event_policy IN ('none', 'preview', 'publish')),
  revision              INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  CHECK (secret_enc IS NULL OR secret_ref IS NULL)
) STRICT, WITHOUT ROWID;

CREATE TABLE source_vehicles (
  source_id   TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  vehicle_num INTEGER NOT NULL REFERENCES vehicles (num) ON DELETE CASCADE,
  PRIMARY KEY (source_id, vehicle_num)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_source_vehicles_vehicle ON source_vehicles (vehicle_num);

-- Runtime state of a source (not configuration; never managed by Terraform).
CREATE TABLE source_status (
  source_id            TEXT PRIMARY KEY REFERENCES sources (id) ON DELETE CASCADE,
  state                TEXT NOT NULL CHECK (state IN ('configured', 'authorized', 'reauthorization_required', 'no_active_request',
                         'rate_limited', 'unavailable', 'unsupported', 'disabled', 'error')),
  state_reason         TEXT NOT NULL DEFAULT '',
  last_attempt_at      INTEGER,
  last_success_at      INTEGER,
  last_delivery_at     INTEGER,
  next_run_at          INTEGER,
  retry_after          INTEGER,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_error_class     TEXT NOT NULL DEFAULT '',
  updated_at           INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Files seen on the portal per (source, VIN, request type): the collection checkpoint.
CREATE TABLE remote_files (
  source_id    TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  vin          TEXT NOT NULL,
  request_type TEXT NOT NULL CHECK (request_type IN ('partial', 'all')),
  name         TEXT NOT NULL,
  created_on   INTEGER,
  size         INTEGER,
  outcome      TEXT NOT NULL CHECK (outcome IN ('stored', 'duplicate', 'no_content', 'failed', 'too_large')),
  delivery_num INTEGER,
  fetched_at   INTEGER NOT NULL,
  PRIMARY KEY (source_id, vin, request_type, name)
) STRICT, WITHOUT ROWID;

-- Raw source material, content-addressed (blob under data_dir/blobs/sha256/…) with an explicit retention date.
CREATE TABLE raw_deliveries (
  num               INTEGER PRIMARY KEY,
  id                TEXT NOT NULL UNIQUE,
  sha256            BLOB NOT NULL UNIQUE,
  size              INTEGER NOT NULL,
  origin            TEXT NOT NULL CHECK (origin IN ('upload', 'collector')),
  source_id         TEXT REFERENCES sources (id) ON DELETE SET NULL,
  vehicle_num       INTEGER REFERENCES vehicles (num) ON DELETE SET NULL,
  original_name     TEXT NOT NULL DEFAULT '',
  request_type      TEXT,
  published_at      INTEGER,
  received_at       INTEGER NOT NULL,
  format            TEXT NOT NULL DEFAULT 'pending',
  fingerprint       TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL CHECK (status IN ('stored', 'processed', 'rejected', 'unrecognized', 'purged')),
  observation_count INTEGER NOT NULL DEFAULT 0,
  time_from         INTEGER,
  time_to           INTEGER,
  error             TEXT NOT NULL DEFAULT '',
  blob_present      INTEGER NOT NULL DEFAULT 1 CHECK (blob_present IN (0, 1)),
  retention_until   INTEGER,
  created_by        TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX ix_deliveries_vehicle ON raw_deliveries (vehicle_num, received_at);
CREATE INDEX ix_deliveries_retention ON raw_deliveries (retention_until) WHERE blob_present = 1;

-- Imports group deliveries processed together (uploads, collection runs, reprocessing) with progress and counts.
CREATE TABLE imports (
  id                     TEXT PRIMARY KEY,
  kind                   TEXT NOT NULL CHECK (kind IN ('upload', 'collection', 'reprocess')),
  source_id              TEXT REFERENCES sources (id) ON DELETE SET NULL,
  vehicle_num            INTEGER REFERENCES vehicles (num) ON DELETE SET NULL,
  state                  TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'partial', 'failed', 'cancelled')),
  event_policy           TEXT NOT NULL CHECK (event_policy IN ('none', 'preview', 'publish')),
  backfill               INTEGER NOT NULL DEFAULT 1 CHECK (backfill IN (0, 1)),
  files_total            INTEGER NOT NULL DEFAULT 0,
  files_done             INTEGER NOT NULL DEFAULT 0,
  deliveries_duplicate   INTEGER NOT NULL DEFAULT 0,
  deliveries_rejected    INTEGER NOT NULL DEFAULT 0,
  deliveries_unrecognized INTEGER NOT NULL DEFAULT 0,
  observations_accepted  INTEGER NOT NULL DEFAULT 0,
  observations_duplicate INTEGER NOT NULL DEFAULT 0,
  observations_rejected  INTEGER NOT NULL DEFAULT 0,
  corrections            INTEGER NOT NULL DEFAULT 0,
  unknown_signals        INTEGER NOT NULL DEFAULT 0,
  late_skipped           INTEGER NOT NULL DEFAULT 0,
  events_created         INTEGER NOT NULL DEFAULT 0,
  events_previewed       INTEGER NOT NULL DEFAULT 0,
  coverage_from          INTEGER,
  coverage_to            INTEGER,
  warnings_json          TEXT NOT NULL DEFAULT '[]',
  error_code             TEXT NOT NULL DEFAULT '',
  error_message          TEXT NOT NULL DEFAULT '',
  created_by             TEXT NOT NULL DEFAULT '',
  correlation_id         TEXT NOT NULL,
  created_at             INTEGER NOT NULL,
  started_at             INTEGER,
  finished_at            INTEGER,
  updated_at             INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_imports_created ON imports (created_at);
CREATE INDEX ix_imports_vehicle ON imports (vehicle_num, created_at);

-- Per-delivery checkpoint of an import: restart skips deliveries that are done.
CREATE TABLE import_deliveries (
  import_id    TEXT NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
  delivery_num INTEGER NOT NULL REFERENCES raw_deliveries (num) ON DELETE CASCADE,
  position     INTEGER NOT NULL,
  outcome      TEXT NOT NULL CHECK (outcome IN ('pending', 'processed', 'duplicate', 'rejected', 'unrecognized')),
  accepted     INTEGER NOT NULL DEFAULT 0,
  duplicates   INTEGER NOT NULL DEFAULT 0,
  message      TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (import_id, delivery_num)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_import_deliveries_delivery ON import_deliveries (delivery_num);

-- Dictionary versions (bundled or imported) and their entries (ADR-0006).
CREATE TABLE dictionary_versions (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL CHECK (kind IN ('continuous', 'historical')),
  title            TEXT NOT NULL,
  document_version TEXT NOT NULL,
  document_date    TEXT NOT NULL,
  source_sha256    TEXT NOT NULL UNIQUE,
  source_file      TEXT NOT NULL,
  origin           TEXT NOT NULL CHECK (origin IN ('bundled', 'imported')),
  entry_count      INTEGER NOT NULL,
  loaded_at        INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE dictionary_entries (
  version_id  TEXT NOT NULL REFERENCES dictionary_versions (id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  unit        TEXT NOT NULL DEFAULT '',
  data_type   TEXT NOT NULL DEFAULT '',
  clusters_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (version_id, key)
) STRICT, WITHOUT ROWID;

-- Signals: the current metadata per upstream key. Unknown keys seen in deliveries get origin 'observed'.
CREATE TABLE signals (
  num                   INTEGER PRIMARY KEY,
  key                   TEXT NOT NULL UNIQUE,
  name                  TEXT NOT NULL,
  description           TEXT NOT NULL DEFAULT '',
  unit_raw              TEXT NOT NULL DEFAULT '',
  unit                  TEXT NOT NULL DEFAULT '',
  data_type_raw         TEXT NOT NULL DEFAULT '',
  value_kind            TEXT NOT NULL CHECK (value_kind IN ('number', 'boolean', 'enum', 'string', 'timestamp', 'json', 'unknown')),
  clusters_json         TEXT NOT NULL DEFAULT '[]',
  origin                TEXT NOT NULL CHECK (origin IN ('continuous', 'historical', 'observed')),
  dictionary_version_id TEXT REFERENCES dictionary_versions (id) ON DELETE SET NULL,
  observed_name         TEXT NOT NULL DEFAULT '',
  first_seen_at         INTEGER,
  last_seen_at          INTEGER,
  updated_at            INTEGER NOT NULL
) STRICT;

CREATE INDEX ix_signals_name ON signals (name);

-- Typed observations (ADR-0008). The primary key is the clustered index used by history queries.
CREATE TABLE observations (
  vehicle_num  INTEGER NOT NULL,
  signal_num   INTEGER NOT NULL,
  observed_at  INTEGER NOT NULL,
  value_hash   INTEGER NOT NULL,
  received_at  INTEGER NOT NULL,
  num          REAL,
  text         TEXT,
  quality      TEXT NOT NULL CHECK (quality IN ('ok', 'null', 'empty', 'unset', 'invalid')),
  unit         TEXT NOT NULL DEFAULT '',
  raw          TEXT,
  delivery_num INTEGER NOT NULL,
  time_basis   TEXT NOT NULL CHECK (time_basis IN ('observed', 'delivery')),
  PRIMARY KEY (vehicle_num, signal_num, observed_at, value_hash)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_observations_time ON observations (observed_at);

-- Latest known value per vehicle and signal (ADR-0009).
CREATE TABLE latest_state (
  vehicle_num  INTEGER NOT NULL,
  signal_num   INTEGER NOT NULL,
  observed_at  INTEGER NOT NULL,
  received_at  INTEGER NOT NULL,
  num          REAL,
  text         TEXT,
  quality      TEXT NOT NULL,
  unit         TEXT NOT NULL DEFAULT '',
  delivery_num INTEGER NOT NULL,
  time_basis   TEXT NOT NULL,
  PRIMARY KEY (vehicle_num, signal_num)
) STRICT, WITHOUT ROWID;

-- Hourly rollups of numeric observations for long-range charts.
CREATE TABLE observation_rollups (
  vehicle_num  INTEGER NOT NULL,
  signal_num   INTEGER NOT NULL,
  bucket_start INTEGER NOT NULL,
  n            INTEGER NOT NULL,
  min          REAL,
  max          REAL,
  sum          REAL,
  last_num     REAL,
  last_at      INTEGER NOT NULL,
  PRIMARY KEY (vehicle_num, signal_num, bucket_start)
) STRICT, WITHOUT ROWID;

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
