-- +goose Up
UPDATE configs SET value = '{"path": "covers"}' WHERE key = 'cover_storage_path' AND value = '{"path": "data/covers"}';

-- +goose Down
UPDATE configs SET value = '{"path": "data/covers"}' WHERE key = 'cover_storage_path' AND value = '{"path": "covers"}';
