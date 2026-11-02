-- name: InsertUser :exec
INSERT INTO users (id, username, display_name, email, role, status, password_hash, password_changed_at, local_login_enabled,
  created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ?;

-- name: GetRootUser :one
SELECT * FROM users WHERE role = 'root';

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: ListUsers :many
SELECT * FROM users WHERE (sqlc.arg(after) = '' OR id > sqlc.arg(after)) ORDER BY id LIMIT sqlc.arg(lim);

-- name: UpdateUserProfile :execrows
UPDATE users SET display_name = ?, email = ?, role = ?, status = ?, local_login_enabled = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND revision = ?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?, password_changed_at = ?, revision = revision + 1, updated_at = ? WHERE id = ?;

-- name: RehashUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = ?;

-- name: SetUserTOTP :exec
UPDATE users SET totp_secret_enc = ?, totp_enabled_at = ?, totp_last_step = 0, revision = revision + 1, updated_at = ? WHERE id = ?;

-- name: SetUserTOTPStep :execrows
UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = ? WHERE id = ?;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = ? AND role != 'root';

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_mac, created_at) VALUES (?, ?, ?);

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = ?;

-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes SET used_at = ? WHERE user_id = ? AND code_mac = ? AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL;

-- name: InsertSession :exec
INSERT INTO sessions (id, token_mac, user_id, method, provider_id, ip, user_agent, created_at, last_seen_at, idle_expires_at,
  absolute_expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSessionByMAC :one
SELECT * FROM sessions WHERE token_mac = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeSession :execrows
UPDATE sessions SET revoked_at = ?, revoked_reason = ? WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = ?, revoked_reason = ? WHERE user_id = ? AND revoked_at IS NULL AND id != sqlc.arg(except_id);

-- name: ListUserSessions :many
SELECT * FROM sessions WHERE user_id = ? AND revoked_at IS NULL AND absolute_expires_at > ? AND idle_expires_at > ?
ORDER BY created_at DESC;

-- name: PurgeSessions :execrows
DELETE FROM sessions WHERE absolute_expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?) OR idle_expires_at < ?;

-- name: InsertChallenge :exec
INSERT INTO login_challenges (id, token_mac, user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?);

-- name: GetChallengeByMAC :one
SELECT * FROM login_challenges WHERE token_mac = ?;

-- name: BumpChallenge :exec
UPDATE login_challenges SET attempts = attempts + 1 WHERE id = ?;

-- name: DeleteChallenge :exec
DELETE FROM login_challenges WHERE id = ?;

-- name: PurgeChallenges :execrows
DELETE FROM login_challenges WHERE expires_at < ?;

-- name: GetThrottle :one
SELECT * FROM login_throttle WHERE key = ?;

-- name: UpsertThrottle :exec
INSERT INTO login_throttle (key, failures, next_allowed_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET failures = excluded.failures, next_allowed_at = excluded.next_allowed_at, updated_at = excluded.updated_at;

-- name: DeleteThrottle :exec
DELETE FROM login_throttle WHERE key = ?;

-- name: PurgeThrottle :execrows
DELETE FROM login_throttle WHERE updated_at < ?;

-- name: InsertSetupToken :exec
INSERT INTO setup_tokens (id, token_mac, created_at, expires_at) VALUES (?, ?, ?, ?);

-- name: GetSetupTokenByMAC :one
SELECT * FROM setup_tokens WHERE token_mac = ?;

-- name: UseSetupToken :execrows
UPDATE setup_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?;

-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, lookup, mac, name, description, owner_id, scopes_json, vehicle_ids_json, never_expires, expires_at,
  issued_by_token, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAPIToken :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: GetAPITokenByLookup :one
SELECT * FROM api_tokens WHERE lookup = ?;

-- name: ListAPITokens :many
SELECT * FROM api_tokens
WHERE (sqlc.narg(owner_id) IS NULL OR owner_id = sqlc.narg(owner_id))
  AND (sqlc.arg(include_revoked) = 1 OR revoked_at IS NULL)
  AND (sqlc.arg(after) = '' OR id > sqlc.arg(after))
ORDER BY id LIMIT sqlc.arg(lim);

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = ?, revoked_by = ?, revoked_reason = ? WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeUserAPITokens :execrows
UPDATE api_tokens SET revoked_at = ?, revoked_by = ?, revoked_reason = ? WHERE owner_id = ? AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?);

-- name: PurgeAPITokens :execrows
DELETE FROM api_tokens WHERE (revoked_at IS NOT NULL AND revoked_at < ?) OR (expires_at IS NOT NULL AND expires_at < ?);

-- name: InsertOIDCProvider :exec
INSERT INTO oidc_providers (id, slug, display_name, issuer, client_id, client_secret_enc, client_secret_ref, secret_fingerprint,
  scopes_json, enabled, provisioning, default_role, allowed_domains_json, require_verified_email, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOIDCProvider :one
SELECT * FROM oidc_providers WHERE id = ?;

-- name: GetOIDCProviderBySlug :one
SELECT * FROM oidc_providers WHERE slug = ?;

-- name: ListOIDCProviders :many
SELECT * FROM oidc_providers ORDER BY display_name, id;

-- name: UpdateOIDCProvider :execrows
UPDATE oidc_providers SET display_name = ?, issuer = ?, client_id = ?, client_secret_enc = ?, client_secret_ref = ?,
  secret_fingerprint = ?, scopes_json = ?, enabled = ?, provisioning = ?, default_role = ?, allowed_domains_json = ?,
  require_verified_email = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND revision = ?;

-- name: DeleteOIDCProvider :execrows
DELETE FROM oidc_providers WHERE id = ?;

-- name: InsertIdentity :exec
INSERT INTO user_identities (id, user_id, provider_id, subject, email, linked_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetIdentityBySubject :one
SELECT * FROM user_identities WHERE provider_id = ? AND subject = ?;

-- name: GetIdentity :one
SELECT * FROM user_identities WHERE id = ?;

-- name: ListUserIdentities :many
SELECT i.*, p.slug AS provider_slug, p.display_name AS provider_name, p.enabled AS provider_enabled
FROM user_identities i JOIN oidc_providers p ON p.id = i.provider_id
WHERE i.user_id = ? ORDER BY i.linked_at;

-- name: TouchIdentity :exec
UPDATE user_identities SET last_login_at = ?, email = ? WHERE id = ?;

-- name: DeleteIdentity :execrows
DELETE FROM user_identities WHERE id = ? AND user_id = ?;

-- name: CountEnabledIdentities :one
SELECT count(*) FROM user_identities i JOIN oidc_providers p ON p.id = i.provider_id WHERE i.user_id = ? AND p.enabled = 1;

-- name: InsertOIDCState :exec
INSERT INTO oidc_login_states (id, state_mac, provider_id, intent, user_id, secrets_enc, return_to, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: TakeOIDCState :one
DELETE FROM oidc_login_states WHERE state_mac = ? RETURNING *;

-- name: PurgeOIDCStates :execrows
DELETE FROM oidc_login_states WHERE expires_at < ?;
