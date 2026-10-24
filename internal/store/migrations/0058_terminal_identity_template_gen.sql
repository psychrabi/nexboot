-- +goose Up
ALTER TABLE terminal_identities ADD COLUMN template_gen TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE terminal_identities DROP COLUMN template_gen;
