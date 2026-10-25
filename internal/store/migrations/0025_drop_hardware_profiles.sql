-- +goose Up
-- Retire hardware profiles: collection at boot is lightweight, but consumers (compatibility matrix FR-F5/F6) were already removed
-- in migration 0021. No remaining functionality reads these records beyond collection/listing/renaming.
ALTER TABLE terminals DROP COLUMN hardware_profile_id;
DROP TABLE IF EXISTS hardware_profiles;

-- +goose Down
CREATE TABLE hardware_profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  boot_mode TEXT NOT NULL DEFAULT '',
  nic_pci_id TEXT NOT NULL DEFAULT '',
  board_vendor TEXT NOT NULL DEFAULT '',
  board_model TEXT NOT NULL DEFAULT '',
  device_hwids TEXT NOT NULL DEFAULT '[]',
  probe_level TEXT NOT NULL DEFAULT 'l1' CHECK (probe_level IN ('l1','l2')),
  created_at TEXT NOT NULL
);
ALTER TABLE terminals ADD COLUMN hardware_profile_id TEXT REFERENCES hardware_profiles(id);
