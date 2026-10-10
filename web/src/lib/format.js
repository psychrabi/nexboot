// Shared formatting helpers and display labels used throughout the application.
export const pad2 = (n) => String(n).padStart(2, "0");

// Null (new Date(null) yields 1970) and Go zero timestamps both mean unavailable.
const toDate = (iso) => {
  if (!iso) return null;
  const d = new Date(iso);
  return isNaN(d) || d.getFullYear() < 1971 ? null : d;
};

export const fmtDateTime = (iso) => {
  const d = toDate(iso);
  if (!d) return "—";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
};

export const fmtDateTimeSec = (iso) => {
  const d = toDate(iso);
  if (!d) return "—";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
};

export const fmtTime = (iso) => {
  const d = new Date(iso);
  if (isNaN(d)) return "—";
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
};

export const GiB = 1024 ** 3;
export const TiB = 1024 ** 4;

export const fmtCap = (b) => {
  b = Number(b || 0);
  if (b >= TiB) return `${(b / TiB).toFixed(2)} TiB`;
  if (b >= GiB) return `${(b / GiB).toFixed(1)} GiB`;
  return `${(b / (1024 ** 2)).toFixed(0)} MiB`;
};

// Match task labels to user-facing button text rather than internal action names.
export const TASK_LABELS = {
  import_image: "Import image", export_image: "Export image", create_blank_image: "Create data disk", create_config: "Create configuration", delete_config: "Delete configuration",
  create_reduction: "Create restore point", delete_reduction: "Delete restore point",
  merge_config: "Overwrite original image", merge_reduction: "Merge restore point", copy_image: "Save as new image",
  super_stop: "Shut down and save restore point", publish_data_disk: "Publish data disk",
  create_pool: "Create storage pool", destroy_pool: "Destroy storage pool", add_disk: "Add storage disk",
  migrate_disk: "Remove disk", replace_disk: "Replace disk", detach_disk: "Detach image disk", mirror_upgrade: "Upgrade to mirror",
  add_special: "Add metadata disk", remove_special: "Remove metadata disk", add_spare: "Add hot spare", remove_spare: "Remove hot spare",
  add_read_cache: "Add read cache", remove_read_cache: "Remove read cache",
  add_write_cache: "Add write cache", remove_write_cache: "Remove write cache",
  flush_cache: "Flush write cache", backup_dataset: "Data backup", image_health_check: "Image health check",
};

// Prefer the user-provided restore-point name; fall back to the ASCII snapshot name for older records.
export const redName = (r) => (r && (r.DisplayName || r.Name)) || "";
