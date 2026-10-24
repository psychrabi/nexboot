-- +goose Up
ALTER TABLE pe_sessions ADD COLUMN config_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pe_sessions ADD COLUMN reduction_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pe_sessions ADD COLUMN reduction_name TEXT NOT NULL DEFAULT '';
ALTER TABLE pe_sessions ADD COLUMN reduction_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE pe_sessions ADD COLUMN target_portal TEXT NOT NULL DEFAULT '';
ALTER TABLE pe_sessions ADD COLUMN driver_pack_ids TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE pe_sessions DROP COLUMN driver_pack_ids;
ALTER TABLE pe_sessions DROP COLUMN target_portal;
ALTER TABLE pe_sessions DROP COLUMN reduction_display_name;
ALTER TABLE pe_sessions DROP COLUMN reduction_name;
ALTER TABLE pe_sessions DROP COLUMN reduction_id;
ALTER TABLE pe_sessions DROP COLUMN config_id;
