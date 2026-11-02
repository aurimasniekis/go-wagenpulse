-- name: GetInstallation :one
SELECT id, installation_id, setup_completed_at, created_at, updated_at FROM installation WHERE id = 1;

-- name: InsertInstallation :exec
INSERT INTO installation (id, installation_id, created_at, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: MarkSetupCompleted :execrows
UPDATE installation SET setup_completed_at = ?, updated_at = ? WHERE id = 1 AND setup_completed_at IS NULL;

-- name: GetSetting :one
SELECT key, value, revision, updated_at, updated_by FROM settings WHERE key = ?;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, revision, updated_at, updated_by) VALUES (?, ?, 1, ?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, revision = settings.revision + 1, updated_at = excluded.updated_at,
  updated_by = excluded.updated_by;

-- name: DeleteSetting :execrows
DELETE FROM settings WHERE key = ?;

-- name: ListSettings :many
SELECT key, value, revision, updated_at, updated_by FROM settings ORDER BY key;

-- name: ListKeyringKeys :many
SELECT id, purpose, version, wrapped, wrap_kek_id, state, created_at, updated_at
FROM keyring_keys ORDER BY purpose, version;

-- name: InsertKeyringKey :exec
INSERT INTO keyring_keys (id, purpose, version, wrapped, wrap_kek_id, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateKeyringKeyState :execrows
UPDATE keyring_keys SET state = ?, updated_at = ? WHERE id = ?;

-- name: UpdateKeyringKeyWrap :execrows
UPDATE keyring_keys SET wrapped = ?, wrap_kek_id = ?, updated_at = ? WHERE id = ?;

-- name: LastAuditEvent :one
SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1;

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, seq, at, request_id, actor_type, actor_id, action, target_type, target_id, outcome,
  reason_code, source_ip, auth_method, details_json, details_enc, prev_hash, hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AuditEventsFrom :many
SELECT id, seq, at, request_id, actor_type, actor_id, action, target_type, target_id, outcome, reason_code,
  source_ip, auth_method, details_json, details_enc, prev_hash, hash
FROM audit_events WHERE seq > ? ORDER BY seq LIMIT ?;

-- name: CountAuditEvents :one
SELECT count(*) FROM audit_events;

-- name: SearchAuditEvents :many
SELECT id, seq, at, request_id, actor_type, actor_id, action, target_type, target_id, outcome, reason_code,
  source_ip, auth_method, details_json, details_enc, prev_hash, hash
FROM audit_events
WHERE seq < sqlc.arg(before_seq)
  AND (sqlc.narg(actor_id) IS NULL OR actor_id = sqlc.narg(actor_id))
  AND (sqlc.narg(target_id) IS NULL OR target_id = sqlc.narg(target_id))
  AND (sqlc.narg(action_prefix) IS NULL OR substr(action, 1, length(sqlc.narg(action_prefix))) = sqlc.narg(action_prefix))
  AND (sqlc.narg(outcome) IS NULL OR outcome = sqlc.narg(outcome))
  AND at >= sqlc.arg(since) AND at < sqlc.arg(until)
ORDER BY seq DESC
LIMIT sqlc.arg(lim);

-- name: GetAuditEvent :one
SELECT id, seq, at, request_id, actor_type, actor_id, action, target_type, target_id, outcome, reason_code,
  source_ip, auth_method, details_json, details_enc, prev_hash, hash
FROM audit_events WHERE id = ?;

-- name: LastAuditSeqBefore :one
SELECT seq, hash FROM audit_events WHERE at < ? ORDER BY seq DESC LIMIT 1;

-- name: PurgeAuditThrough :execrows
DELETE FROM audit_events WHERE seq <= ?;

-- name: InsertJob :execrows
INSERT INTO jobs (id, kind, run_at, attempts, max_attempts, state, payload_json, dedup_key, correlation_id, created_at, updated_at)
VALUES (?, ?, ?, 0, ?, 'queued', ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: DueJobs :many
SELECT id, kind, run_at, attempts, max_attempts, lease_until, state, payload_json, last_error, dedup_key, correlation_id,
  created_at, updated_at
FROM jobs
WHERE (state = 'queued' AND run_at <= sqlc.arg(now)) OR (state = 'running' AND lease_until < sqlc.arg(now))
ORDER BY run_at LIMIT sqlc.arg(lim);

-- name: ClaimJob :execrows
UPDATE jobs SET state = 'running', attempts = attempts + 1, lease_until = sqlc.arg(lease_until), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND ((state = 'queued') OR (state = 'running' AND lease_until < sqlc.arg(now)));

-- name: ExtendJobLease :execrows
UPDATE jobs SET lease_until = ?, updated_at = ? WHERE id = ? AND state = 'running';

-- name: FinishJob :exec
UPDATE jobs SET state = ?, run_at = ?, lease_until = NULL, last_error = ?, updated_at = ? WHERE id = ? AND state = 'running';

-- name: CancelJob :execrows
UPDATE jobs SET state = 'cancelled', lease_until = NULL, updated_at = ? WHERE id = ? AND state IN ('queued', 'running');

-- name: GetJob :one
SELECT id, kind, run_at, attempts, max_attempts, lease_until, state, payload_json, last_error, dedup_key, correlation_id,
  created_at, updated_at
FROM jobs WHERE id = ?;

-- name: CountJobsByState :many
SELECT kind, state, count(*) AS n FROM jobs GROUP BY kind, state;

-- name: PurgeFinishedJobs :execrows
DELETE FROM jobs WHERE state IN ('done', 'dead', 'cancelled') AND updated_at < ?;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE key_mac = ? AND expires_at > ?;

-- name: InsertIdempotencyKey :execrows
INSERT INTO idempotency_keys (key_mac, request_hash, method, path, state, created_at, expires_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)
ON CONFLICT (key_mac) DO UPDATE SET request_hash = excluded.request_hash, method = excluded.method, path = excluded.path, state = 'pending',
  status = 0, content_type = '', body = NULL, secret_withheld = 0, location = '', etag = '', created_at = excluded.created_at, expires_at = excluded.expires_at
WHERE idempotency_keys.expires_at <= excluded.created_at;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET state = 'done', status = ?, content_type = ?, body = ?, secret_withheld = ?, location = ?, etag = ? WHERE key_mac = ?;

-- name: DeleteIdempotencyKey :exec
DELETE FROM idempotency_keys WHERE key_mac = ?;

-- name: PurgeIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < ?;

-- name: InsertOperation :exec
INSERT INTO operations (id, kind, state, target_type, target_id, params_json, progress_total, created_by, created_at, updated_at)
VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?);

-- name: GetOperation :one
SELECT * FROM operations WHERE id = ?;

-- name: StartOperation :execrows
UPDATE operations SET state = 'running', started_at = ?, updated_at = ? WHERE id = ? AND state = 'queued';

-- name: ProgressOperation :exec
UPDATE operations SET progress_done = ?, progress_total = ?, updated_at = ? WHERE id = ?;

-- name: FinishOperation :execrows
UPDATE operations SET state = ?, result_json = ?, error_code = ?, error_message = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND state IN ('queued', 'running');

-- name: ListOperations :many
SELECT * FROM operations
WHERE (sqlc.narg(kind) IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.narg(before_id) IS NULL OR id < sqlc.narg(before_id))
ORDER BY id DESC LIMIT sqlc.arg(lim);

-- name: PurgeFinishedOperations :execrows
DELETE FROM operations WHERE state IN ('succeeded', 'failed', 'cancelled') AND updated_at < ?;
