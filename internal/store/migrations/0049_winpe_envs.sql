-- +goose Up
CREATE TABLE winpe_envs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    image_id TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    build INTEGER NOT NULL DEFAULT 0,
    dir TEXT NOT NULL,
    files TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX winpe_envs_image ON winpe_envs(image_id);

-- +goose Down
DROP TABLE winpe_envs;
