-- +goose Up
-- Ensure created_at/updated_at are set centrally in microseconds on INSERT
-- This avoids duplicating the julianday→microseconds expression across SQL files.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS set_sessions_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_files_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_messages_timestamps_on_insert;

CREATE TRIGGER IF NOT EXISTS set_sessions_timestamps_on_insert
AFTER INSERT ON sessions
BEGIN
  UPDATE sessions SET
    created_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER),
    updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS set_files_timestamps_on_insert
AFTER INSERT ON files
BEGIN
  UPDATE files SET
    created_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER),
    updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS set_messages_timestamps_on_insert
AFTER INSERT ON messages
BEGIN
  UPDATE messages SET
    created_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER),
    updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
-- Drop INSERT timestamp triggers
-- +goose StatementBegin
DROP TRIGGER IF EXISTS set_sessions_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_files_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_messages_timestamps_on_insert;
-- +goose StatementEnd
