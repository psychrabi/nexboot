-- +goose Up
-- v2 保留上传包，撤下旧驱动集和未保存的离线注入状态。
DROP TABLE driver_bundle_packs;
DROP TABLE driver_bundles;
ALTER TABLE driver_packs DROP COLUMN recommended;
ALTER TABLE terminals DROP COLUMN pending_bundle_id;
ALTER TABLE terminals DROP COLUMN pending_bundle_at;

-- +goose Down
ALTER TABLE driver_packs ADD COLUMN recommended BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE terminals ADD COLUMN pending_bundle_id TEXT;
ALTER TABLE terminals ADD COLUMN pending_bundle_at TEXT;
CREATE TABLE driver_bundles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    os_type TEXT NOT NULL CHECK (os_type IN ('windows','linux')),
    created_at TEXT NOT NULL
);
CREATE TABLE driver_bundle_packs (
    id TEXT PRIMARY KEY,
    bundle_id TEXT NOT NULL REFERENCES driver_bundles(id) ON DELETE CASCADE,
    pack_id TEXT NOT NULL REFERENCES driver_packs(id) ON DELETE RESTRICT,
    UNIQUE (bundle_id, pack_id)
);
