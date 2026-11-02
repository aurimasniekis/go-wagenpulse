-- 0001_platform: installation singleton, settings, keyring, audit chain, durable jobs, idempotency, operations.
-- Forward-only: rollback = restore the pre-migration snapshot (docs/operator/upgrades.md).

-- +goose Up
CREATE TABLE installation (
  id                 INTEGER PRIMARY KEY CHECK (id = 1),
  installation_id    TEXT NOT NULL,
  setup_completed_at INTEGER,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
) STRICT;

-- Runtime settings documents (JSON) with a revision for optimistic concurrency (ETag).
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  revision   INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at INTEGER NOT NULL,
  updated_by TEXT
) STRICT, WITHOUT ROWID;

CREATE TABLE keyring_keys (
  id          TEXT PRIMARY KEY,
  purpose     TEXT NOT NULL CHECK (purpose IN ('field', 'token', 'signing', 'backup-meta')),
  version     INTEGER NOT NULL CHECK (version > 0),
  wrapped     BLOB NOT NULL,
  wrap_kek_id TEXT NOT NULL,
  state       TEXT NOT NULL CHECK (state IN ('active', 'decrypt_only', 'retired')),
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE (purpose, version)
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX ux_keyring_active ON keyring_keys (purpose) WHERE state = 'active';

-- Hash-chained audit log (ADR-0010 of HallPass, adapted). Rows outlive the rows they describe: no foreign keys.
CREATE TABLE audit_events (
  id           TEXT PRIMARY KEY,
  seq          INTEGER NOT NULL UNIQUE CHECK (seq > 0),
  at           INTEGER NOT NULL,
  request_id   TEXT,
  actor_type   TEXT NOT NULL CHECK (actor_type IN ('user', 'token', 'system', 'cli')),
  actor_id     TEXT,
  action       TEXT NOT NULL,
  target_type  TEXT,
  target_id    TEXT,
  outcome      TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
  reason_code  TEXT,
  source_ip    TEXT,
  auth_method  TEXT,
  details_json TEXT NOT NULL DEFAULT '{}',
  details_enc  BLOB,
  prev_hash    BLOB NOT NULL,
  hash         BLOB NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_audit_at ON audit_events (at);
CREATE INDEX ix_audit_target ON audit_events (target_type, target_id, at);
CREATE INDEX ix_audit_actor ON audit_events (actor_type, actor_id, at);
CREATE INDEX ix_audit_action ON audit_events (action, at);

-- Durable background jobs (HallPass internal/jobs, extended with cancellation and correlation).
CREATE TABLE jobs (
  id             TEXT PRIMARY KEY,
  kind           TEXT NOT NULL,
  run_at         INTEGER NOT NULL,
  attempts       INTEGER NOT NULL DEFAULT 0,
  max_attempts   INTEGER NOT NULL CHECK (max_attempts > 0),
  lease_until    INTEGER,
  state          TEXT NOT NULL CHECK (state IN ('queued', 'running', 'done', 'dead', 'cancelled')),
  payload_json   TEXT NOT NULL DEFAULT '{}',
  last_error     TEXT,
  dedup_key      TEXT,
  correlation_id TEXT,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_jobs_due ON jobs (state, run_at);
CREATE UNIQUE INDEX ux_jobs_dedup ON jobs (dedup_key) WHERE dedup_key IS NOT NULL AND state IN ('queued', 'running');

-- Idempotency-Key replay cache for POST requests (ADR-0018, Porch pattern). key_mac = MAC(credential, key).
CREATE TABLE idempotency_keys (
  key_mac         BLOB NOT NULL PRIMARY KEY,
  request_hash    BLOB NOT NULL,
  method          TEXT NOT NULL,
  path            TEXT NOT NULL,
  state           TEXT NOT NULL CHECK (state IN ('pending', 'done')),
  status          INTEGER NOT NULL DEFAULT 0,
  content_type    TEXT NOT NULL DEFAULT '',
  body            BLOB,
  secret_withheld INTEGER NOT NULL DEFAULT 0 CHECK (secret_withheld IN (0, 1)),
  location        TEXT NOT NULL DEFAULT '',
  etag            TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_idempotency_expiry ON idempotency_keys (expires_at);

-- Asynchronous operations other than imports (replay, purge, reprocess, retention runs).
CREATE TABLE operations (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  target_type   TEXT,
  target_id     TEXT,
  params_json   TEXT NOT NULL DEFAULT '{}',
  progress_done INTEGER NOT NULL DEFAULT 0,
  progress_total INTEGER NOT NULL DEFAULT 0,
  result_json   TEXT NOT NULL DEFAULT '{}',
  error_code    TEXT,
  error_message TEXT,
  created_by    TEXT,
  created_at    INTEGER NOT NULL,
  started_at    INTEGER,
  finished_at   INTEGER,
  updated_at    INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_operations_created ON operations (created_at);

-- Disk-full recovery probe: a degraded "disk full" state clears only after a page-allocating write commits.
CREATE TABLE store_space_probe (
  id   INTEGER PRIMARY KEY CHECK (id = 1),
  data BLOB NOT NULL
) STRICT;

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
