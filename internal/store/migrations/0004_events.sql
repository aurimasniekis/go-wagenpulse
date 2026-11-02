-- 0004_events: event rules and revisions, rule state, events, adapters, routes, per-adapter deliveries (the outbox)
-- and delivery attempts (ADR-0015, ADR-0016, ADR-0017, ADR-0026).

-- +goose Up
CREATE TABLE rules (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  kind             TEXT NOT NULL CHECK (kind IN ('signal_change', 'threshold', 'transition', 'stale')),
  current_revision INTEGER NOT NULL CHECK (current_revision > 0),
  deleted_at       INTEGER,
  revision         INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Immutable rule definitions; events reference (rule_id, revision).
CREATE TABLE rule_revisions (
  rule_id         TEXT NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
  revision        INTEGER NOT NULL,
  definition_json TEXT NOT NULL,
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  PRIMARY KEY (rule_id, revision)
) STRICT, WITHOUT ROWID;

-- Lookup index from signal key to the rules that watch it (current revisions of enabled rules).
CREATE TABLE rule_signals (
  rule_id    TEXT NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
  signal_key TEXT NOT NULL,
  PRIMARY KEY (rule_id, signal_key)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_rule_signals_key ON rule_signals (signal_key);

-- Persisted evaluation state per (rule, vehicle, signal). signal_num 0 is the vehicle-level state of stale rules.
CREATE TABLE rule_state (
  rule_id          TEXT NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
  vehicle_num      INTEGER NOT NULL,
  signal_num       INTEGER NOT NULL,
  revision         INTEGER NOT NULL,
  ref_num          REAL,
  ref_text         TEXT,
  last_observed_at INTEGER,
  active           INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
  pending          INTEGER NOT NULL DEFAULT 0,
  last_fired_at    INTEGER,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (rule_id, vehicle_num, signal_num)
) STRICT, WITHOUT ROWID;

-- Durable events (ADR-0015). payload_json is the versioned envelope body.
CREATE TABLE events (
  num            INTEGER PRIMARY KEY,
  id             TEXT NOT NULL UNIQUE,
  type           TEXT NOT NULL,
  rule_id        TEXT,
  rule_revision  INTEGER,
  vehicle_num    INTEGER,
  signal_num     INTEGER,
  observed_at    INTEGER NOT NULL,
  created_at     INTEGER NOT NULL,
  backfill       INTEGER NOT NULL DEFAULT 0 CHECK (backfill IN (0, 1)),
  import_id      TEXT,
  correlation_id TEXT NOT NULL,
  payload_json   TEXT NOT NULL
) STRICT;

CREATE INDEX ix_events_created ON events (created_at);
CREATE INDEX ix_events_vehicle ON events (vehicle_num, observed_at);
CREATE INDEX ix_events_rule ON events (rule_id, created_at);

CREATE TABLE adapters (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL UNIQUE,
  kind               TEXT NOT NULL CHECK (kind IN ('pulse_nats')),
  enabled            INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  config_json        TEXT NOT NULL,
  creds_enc          BLOB,
  creds_ref          TEXT,
  secret_fingerprint TEXT NOT NULL DEFAULT '',
  revision           INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  CHECK (creds_enc IS NULL OR creds_ref IS NULL)
) STRICT, WITHOUT ROWID;

-- Runtime health of an adapter (not configuration).
CREATE TABLE adapter_status (
  adapter_id       TEXT PRIMARY KEY REFERENCES adapters (id) ON DELETE CASCADE,
  state            TEXT NOT NULL CHECK (state IN ('connecting', 'connected', 'disconnected', 'error', 'disabled')),
  reason           TEXT NOT NULL DEFAULT '',
  last_ok_at       INTEGER,
  last_error_at    INTEGER,
  last_error_class TEXT NOT NULL DEFAULT '',
  last_test_at     INTEGER,
  last_test_result TEXT NOT NULL DEFAULT '',
  updated_at       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE routes (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  adapter_id       TEXT NOT NULL REFERENCES adapters (id) ON DELETE CASCADE,
  event_types_json TEXT NOT NULL DEFAULT '[]',
  include_backfill INTEGER NOT NULL DEFAULT 0 CHECK (include_backfill IN (0, 1)),
  revision         INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_routes_adapter ON routes (adapter_id);

CREATE TABLE route_rules (
  route_id TEXT NOT NULL REFERENCES routes (id) ON DELETE CASCADE,
  rule_id  TEXT NOT NULL,
  PRIMARY KEY (route_id, rule_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE route_vehicles (
  route_id    TEXT NOT NULL REFERENCES routes (id) ON DELETE CASCADE,
  vehicle_num INTEGER NOT NULL,
  PRIMARY KEY (route_id, vehicle_num)
) STRICT, WITHOUT ROWID;

-- Per-adapter deliveries: the transactional outbox (ADR-0015). adapter_id is kept (SET NULL) for history when an
-- adapter is deleted; adapter_name snapshots its name.
CREATE TABLE deliveries (
  num               INTEGER PRIMARY KEY,
  id                TEXT NOT NULL UNIQUE,
  event_num         INTEGER NOT NULL REFERENCES events (num) ON DELETE CASCADE,
  adapter_id        TEXT REFERENCES adapters (id) ON DELETE SET NULL,
  adapter_name      TEXT NOT NULL,
  route_id          TEXT,
  replay_seq        INTEGER NOT NULL DEFAULT 0,
  replay_of         TEXT,
  msg_id            TEXT NOT NULL,
  subject           TEXT NOT NULL DEFAULT '',
  state             TEXT NOT NULL CHECK (state IN ('pending', 'processing', 'succeeded', 'retrying', 'dead')),
  attempts          INTEGER NOT NULL DEFAULT 0,
  max_attempts      INTEGER NOT NULL,
  next_attempt_at   INTEGER NOT NULL,
  lease_until       INTEGER,
  last_error_class  TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',
  succeeded_at      INTEGER,
  ack_stream        TEXT,
  ack_seq           INTEGER,
  ack_duplicate     INTEGER,
  dead_reason       TEXT NOT NULL DEFAULT '',
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  UNIQUE (event_num, adapter_id, replay_seq)
) STRICT;

CREATE INDEX ix_deliveries_due ON deliveries (adapter_id, state, next_attempt_at);
CREATE INDEX ix_deliveries_event ON deliveries (event_num);
CREATE INDEX ix_deliveries_created ON deliveries (created_at);

CREATE TABLE delivery_attempts (
  delivery_num  INTEGER NOT NULL REFERENCES deliveries (num) ON DELETE CASCADE,
  attempt       INTEGER NOT NULL,
  started_at    INTEGER NOT NULL,
  finished_at   INTEGER NOT NULL,
  outcome       TEXT NOT NULL CHECK (outcome IN ('succeeded', 'retry', 'dead', 'interrupted')),
  error_class   TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  ack_seq       INTEGER,
  ack_duplicate INTEGER,
  duration_ms   INTEGER NOT NULL,
  PRIMARY KEY (delivery_num, attempt)
) STRICT, WITHOUT ROWID;

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
