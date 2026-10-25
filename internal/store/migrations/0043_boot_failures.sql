-- +goose Up
-- Track each client's most recent boot failure for named alerts.
CREATE TABLE boot_failures (
  mac TEXT PRIMARY KEY,
  stage TEXT NOT NULL,
  code TEXT NOT NULL DEFAULT '',
  platform TEXT NOT NULL DEFAULT '',
  image_id TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL
);
-- Comma-separated boot modes detected by health checks; empty means undetermined.
ALTER TABLE image_health_reports ADD COLUMN boot_modes TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE image_health_reports DROP COLUMN boot_modes;
DROP TABLE boot_failures;
