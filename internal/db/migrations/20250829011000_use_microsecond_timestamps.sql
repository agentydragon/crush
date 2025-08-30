-- +goose Up
-- Switch all trigger-updated timestamps to microseconds (INT)
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_files_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;

CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
  UPDATE sessions SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS update_files_updated_at
AFTER UPDATE ON files
BEGIN
  UPDATE files SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
  UPDATE messages SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = new.id;
END;
-- +goose StatementEnd

-- +goose Down
-- Revert triggers to second resolution
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_files_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;

CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
  UPDATE sessions SET updated_at = strftime('%s', 'now')
  WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS update_files_updated_at
AFTER UPDATE ON files
BEGIN
  UPDATE files SET updated_at = strftime('%s', 'now')
  WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
  UPDATE messages SET updated_at = strftime('%s', 'now')
  WHERE id = new.id;
END;
-- +goose StatementEnd
