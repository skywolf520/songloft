-- +goose Up
INSERT OR IGNORE INTO configs (key, value) VALUES ('scan_auto_create_playlists', 'true');

-- +goose Down
DELETE FROM configs WHERE key = 'scan_auto_create_playlists';
