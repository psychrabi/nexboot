-- +goose Up
ALTER TABLE terminal_identities ADD COLUMN template_boot_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE terminal_identities ADD COLUMN template_boot_at TEXT NOT NULL DEFAULT '0001-01-01T00:00:00Z';
ALTER TABLE terminal_identities ADD COLUMN template_ok_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE terminal_identities ADD COLUMN template_failed_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE terminal_identities ADD COLUMN template_strikes INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE terminal_identities DROP COLUMN template_strikes;
ALTER TABLE terminal_identities DROP COLUMN template_failed_ref;
ALTER TABLE terminal_identities DROP COLUMN template_ok_ref;
ALTER TABLE terminal_identities DROP COLUMN template_boot_at;
ALTER TABLE terminal_identities DROP COLUMN template_boot_ref;
