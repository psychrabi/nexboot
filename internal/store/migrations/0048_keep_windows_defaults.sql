-- +goose Up
-- 存「保留默认」而不是「要管」：从未保存过设置时零值也是要管。
ALTER TABLE system_settings ADD COLUMN keep_windows_defaults INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE system_settings DROP COLUMN keep_windows_defaults;
