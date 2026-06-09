-- +goose Up
ALTER TABLE js_plugins ADD COLUMN icon TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE js_plugins DROP COLUMN icon;
