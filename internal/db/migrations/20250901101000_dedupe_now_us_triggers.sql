-- +goose Up
-- Deduplicate microsecond timestamp expression via a helper view and re-create triggers to use it.
-- +goose StatementBegin
-- Single-row view that exposes the current time in microseconds as column v
DROP VIEW IF EXISTS now_us;
CREATE VIEW now_us AS
  SELECT CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER) AS v;

-- Recreate UPDATE triggers to use (SELECT v FROM now_us)
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_files_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;

CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
  UPDATE sessions SET updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_files_updated_at
AFTER UPDATE ON files
BEGIN
  UPDATE files SET updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
  UPDATE messages SET updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;

-- Recreate INSERT triggers to set created_at/updated_at using (SELECT v FROM now_us)
DROP TRIGGER IF EXISTS set_sessions_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_files_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_messages_timestamps_on_insert;

CREATE TRIGGER IF NOT EXISTS set_sessions_timestamps_on_insert
AFTER INSERT ON sessions
BEGIN
  UPDATE sessions SET
    created_at = (SELECT v FROM now_us),
    updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS set_files_timestamps_on_insert
AFTER INSERT ON files
BEGIN
  UPDATE files SET
    created_at = (SELECT v FROM now_us),
    updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS set_messages_timestamps_on_insert
AFTER INSERT ON messages
BEGIN
  UPDATE messages SET
    created_at = (SELECT v FROM now_us),
    updated_at = (SELECT v FROM now_us)
  WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
-- Drop dedup view and restore triggers with inline expressions
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_files_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;
DROP TRIGGER IF EXISTS set_sessions_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_files_timestamps_on_insert;
DROP TRIGGER IF EXISTS set_messages_timestamps_on_insert;
DROP VIEW IF EXISTS now_us;

CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
  UPDATE sessions SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_files_updated_at
AFTER UPDATE ON files
BEGIN
  UPDATE files SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
  UPDATE messages SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
  WHERE id = NEW.id;
END;

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
