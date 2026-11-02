-- 0002_auth: users, local credentials, TOTP, sessions, OIDC providers and identities, throttling, setup tokens,
-- API tokens (ADR-0010, ADR-0011, ADR-0012, ADR-0014).

-- +goose Up
CREATE TABLE users (
  id                  TEXT PRIMARY KEY,
  username            TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(username) BETWEEN 1 AND 64),
  display_name        TEXT NOT NULL,
  email               TEXT,
  role                TEXT NOT NULL CHECK (role IN ('root', 'admin', 'operator', 'viewer')),
  status              TEXT NOT NULL CHECK (status IN ('active', 'suspended')),
  -- Local password login: NULL hash means no local password.
  password_hash       TEXT,
  password_changed_at INTEGER,
  local_login_enabled INTEGER NOT NULL DEFAULT 1 CHECK (local_login_enabled IN (0, 1)),
  totp_secret_enc     BLOB,
  totp_enabled_at     INTEGER,
  totp_last_step      INTEGER NOT NULL DEFAULT 0,
  last_login_at       INTEGER,
  revision            INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Exactly one root user may exist (ADR-0010).
CREATE UNIQUE INDEX ux_users_single_root ON users (role) WHERE role = 'root';

CREATE TABLE user_recovery_codes (
  user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  code_mac   BLOB NOT NULL,
  used_at    INTEGER,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, code_mac)
) STRICT, WITHOUT ROWID;

CREATE TABLE sessions (
  id                  TEXT PRIMARY KEY,
  token_mac           BLOB NOT NULL UNIQUE,
  user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  method              TEXT NOT NULL CHECK (method IN ('password', 'oidc', 'setup')),
  provider_id         TEXT,
  ip                  TEXT,
  user_agent          TEXT,
  created_at          INTEGER NOT NULL,
  last_seen_at        INTEGER NOT NULL,
  idle_expires_at     INTEGER NOT NULL,
  absolute_expires_at INTEGER NOT NULL,
  revoked_at          INTEGER,
  revoked_reason      TEXT
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_sessions_user ON sessions (user_id, created_at);
CREATE INDEX ix_sessions_expiry ON sessions (absolute_expires_at);

-- Second-factor challenge after a correct password when TOTP is enabled (5 minutes, single use).
CREATE TABLE login_challenges (
  id         TEXT PRIMARY KEY,
  token_mac  BLOB NOT NULL UNIQUE,
  user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE login_throttle (
  key             TEXT PRIMARY KEY,
  failures        INTEGER NOT NULL,
  next_allowed_at INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE setup_tokens (
  id         TEXT PRIMARY KEY,
  token_mac  BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER
) STRICT, WITHOUT ROWID;

-- OIDC identity providers are runtime configuration (Porch pattern, ADR-0012).
CREATE TABLE oidc_providers (
  id                TEXT PRIMARY KEY,
  slug              TEXT NOT NULL UNIQUE,
  display_name      TEXT NOT NULL,
  issuer            TEXT NOT NULL,
  client_id         TEXT NOT NULL,
  client_secret_enc BLOB,
  client_secret_ref TEXT,
  secret_fingerprint TEXT NOT NULL DEFAULT '',
  scopes_json       TEXT NOT NULL DEFAULT '["openid","profile","email"]',
  enabled           INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  provisioning      TEXT NOT NULL CHECK (provisioning IN ('preprovisioned', 'jit')),
  default_role      TEXT NOT NULL DEFAULT 'viewer' CHECK (default_role IN ('operator', 'viewer')),
  allowed_domains_json TEXT NOT NULL DEFAULT '[]',
  require_verified_email INTEGER NOT NULL DEFAULT 1 CHECK (require_verified_email IN (0, 1)),
  revision          INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  CHECK (client_secret_enc IS NULL OR client_secret_ref IS NULL)
) STRICT, WITHOUT ROWID;

CREATE TABLE user_identities (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  provider_id   TEXT NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
  subject       TEXT NOT NULL,
  email         TEXT,
  linked_at     INTEGER NOT NULL,
  last_login_at INTEGER,
  UNIQUE (provider_id, subject),
  UNIQUE (provider_id, user_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_identities_user ON user_identities (user_id);

CREATE TABLE oidc_login_states (
  id          TEXT PRIMARY KEY,
  state_mac   BLOB NOT NULL UNIQUE,
  provider_id TEXT NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
  intent      TEXT NOT NULL CHECK (intent IN ('login', 'link')),
  user_id     TEXT REFERENCES users (id) ON DELETE CASCADE,
  secrets_enc BLOB NOT NULL,
  return_to   TEXT NOT NULL DEFAULT '/',
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_oidc_states_expiry ON oidc_login_states (expires_at);

-- API tokens (ADR-0014): only the lookup and an HMAC of the secret are stored.
CREATE TABLE api_tokens (
  id                TEXT PRIMARY KEY,
  lookup            TEXT NOT NULL UNIQUE,
  mac               BLOB NOT NULL,
  name              TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  owner_id          TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  scopes_json       TEXT NOT NULL,
  vehicle_ids_json  TEXT NOT NULL DEFAULT '[]',
  never_expires     INTEGER NOT NULL DEFAULT 0 CHECK (never_expires IN (0, 1)),
  expires_at        INTEGER,
  issued_by_token   TEXT,
  created_by        TEXT NOT NULL,
  created_at        INTEGER NOT NULL,
  last_used_at      INTEGER,
  last_used_ip      TEXT,
  revoked_at        INTEGER,
  revoked_by        TEXT,
  revoked_reason    TEXT,
  CHECK ((never_expires = 1 AND expires_at IS NULL) OR (never_expires = 0 AND expires_at IS NOT NULL))
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_api_tokens_owner ON api_tokens (owner_id, created_at);

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
