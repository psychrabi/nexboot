-- +goose Up
-- 机型：按开机上报的网卡与固件归并；适配记录：还原点 × 机型 = 可开机。终端记机型与最近一次开机。
CREATE TABLE hardware_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    fingerprint TEXT NOT NULL UNIQUE,
    nic_vendor TEXT NOT NULL DEFAULT '',
    nic_device TEXT NOT NULL DEFAULT '',
    nic_subsys TEXT NOT NULL DEFAULT '',
    bus_loc TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    manufacturer TEXT NOT NULL DEFAULT '',
    product TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE model_adaptations (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    reduction_id TEXT NOT NULL,
    config_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    template_instance_id TEXT NOT NULL DEFAULT '',
    class_key TEXT NOT NULL DEFAULT '',
    netcfg_instance_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    terminal_id TEXT NOT NULL DEFAULT '',
    driver_pack_ids TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX model_adaptations_key ON model_adaptations(profile_id, reduction_id);
CREATE INDEX model_adaptations_reduction ON model_adaptations(reduction_id);
ALTER TABLE terminals ADD COLUMN hardware_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE terminals ADD COLUMN last_boot_at TEXT;
ALTER TABLE terminals ADD COLUMN last_boot_reduction_id TEXT NOT NULL DEFAULT '';
ALTER TABLE terminals ADD COLUMN last_boot_ok_at TEXT;

ALTER TABLE terminals ADD COLUMN last_boot_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE boot_failures ADD COLUMN reduction_id TEXT NOT NULL DEFAULT '';
ALTER TABLE boot_failures ADD COLUMN profile_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE boot_failures DROP COLUMN profile_id;
ALTER TABLE boot_failures DROP COLUMN reduction_id;
ALTER TABLE terminals DROP COLUMN last_boot_profile_id;
ALTER TABLE terminals DROP COLUMN last_boot_ok_at;
ALTER TABLE terminals DROP COLUMN last_boot_reduction_id;
ALTER TABLE terminals DROP COLUMN last_boot_at;
ALTER TABLE terminals DROP COLUMN hardware_profile_id;
DROP TABLE model_adaptations;
DROP TABLE hardware_profiles;
