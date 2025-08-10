-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN is_new INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN is_new;
-- +goose StatementEnd
