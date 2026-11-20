-- 0005_source_correlation: the correlation ID of a source's last collection run, so the UI and the server log refer
-- to the same run.

-- +goose Up
ALTER TABLE source_status ADD COLUMN last_correlation_id TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
