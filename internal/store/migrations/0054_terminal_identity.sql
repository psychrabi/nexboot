-- +goose Up
CREATE TABLE terminal_identities (
 id TEXT PRIMARY KEY,
 terminal_id TEXT NOT NULL UNIQUE,
 profile_id TEXT NOT NULL,
 instance_id TEXT NOT NULL,
 source TEXT NOT NULL,
 layer_reduction_ref TEXT NOT NULL DEFAULT '',
 layer_server_id TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
-- +goose Down
DROP TABLE terminal_identities;
