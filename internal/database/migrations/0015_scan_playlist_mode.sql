-- +goose Up
-- 将旧的布尔 scan_auto_create_include_subdirs 迁移为三值模式 scan_playlist_mode
INSERT OR IGNORE INTO configs (key, value)
VALUES ('scan_playlist_mode',
    COALESCE(
        (SELECT CASE WHEN value = 'true' THEN 'bubble_up' ELSE 'directory' END
         FROM configs WHERE key = 'scan_auto_create_include_subdirs'),
        'directory'
    )
);
DELETE FROM configs WHERE key = 'scan_auto_create_include_subdirs';

-- +goose Down
INSERT OR IGNORE INTO configs (key, value)
VALUES ('scan_auto_create_include_subdirs',
    COALESCE(
        (SELECT CASE WHEN value = 'bubble_up' THEN 'true' ELSE 'false' END
         FROM configs WHERE key = 'scan_playlist_mode'),
        'false'
    )
);
DELETE FROM configs WHERE key = 'scan_playlist_mode';
