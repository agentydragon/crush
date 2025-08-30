-- +goose Up
-- +goose StatementBegin
-- Composite indexes to support efficient session-scoped delta scans
CREATE INDEX IF NOT EXISTS idx_messages_session_created_id ON messages(session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_messages_session_updated_id ON messages(session_id, updated_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_session_updated_id;
DROP INDEX IF EXISTS idx_messages_session_created_id;
-- +goose StatementEnd
