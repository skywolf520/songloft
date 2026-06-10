-- +goose Up
ALTER TABLE js_plugins ADD COLUMN external_paths TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE js_plugins DROP COLUMN external_paths;
