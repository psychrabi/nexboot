-- +goose Up
-- PE 会话：客户机下次开机进 WinPE 的行程。tasks 一并放宽 CHECK，含机型适配与身份层后续要用的类型。
CREATE TABLE pe_sessions (
    id TEXT PRIMARY KEY,
    terminal_id TEXT NOT NULL,
    mac TEXT NOT NULL,
    mode TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    token TEXT NOT NULL UNIQUE,
    env_id TEXT NOT NULL DEFAULT '',
    bcd_id TEXT NOT NULL DEFAULT '',
    bundle_id TEXT NOT NULL DEFAULT '',
    target_iqn TEXT NOT NULL DEFAULT '',
    lun INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX pe_sessions_terminal ON pe_sessions(terminal_id, state);
CREATE TABLE tasks_pe (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL CHECK (type IN ('import_image','create_config','delete_config','create_reduction','delete_reduction','merge_config','merge_reduction','super_stop','create_pool','destroy_pool','add_disk','migrate_disk','replace_disk','add_read_cache','remove_read_cache','add_write_cache','remove_write_cache','flush_cache','backup_dataset','image_health_check','detach_disk','mirror_upgrade','add_special','remove_special','add_spare','remove_spare','create_blank_image','export_image','publish_data_disk','copy_image','winpe_extract','pe_probe','model_adapt','identity_capture','identity_layer_rebuild')),
  target_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending','running','success','failed')),
  progress INTEGER NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT,
  message TEXT NOT NULL DEFAULT '',
  node TEXT NOT NULL DEFAULT ''
);
INSERT INTO tasks_pe (id,type,target_ref,status,progress,result,error,created_at,finished_at,message,node)
  SELECT id,type,target_ref,status,progress,result,error,created_at,finished_at,message,node FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_pe RENAME TO tasks;

-- +goose Down
DROP TABLE pe_sessions;
CREATE TABLE tasks_no_pe (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL CHECK (type IN ('import_image','create_config','delete_config','create_reduction','delete_reduction','merge_config','merge_reduction','super_stop','create_pool','destroy_pool','add_disk','migrate_disk','replace_disk','add_read_cache','remove_read_cache','add_write_cache','remove_write_cache','flush_cache','backup_dataset','image_health_check','detach_disk','mirror_upgrade','add_special','remove_special','add_spare','remove_spare','create_blank_image','export_image','publish_data_disk','copy_image','winpe_extract')),
  target_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending','running','success','failed')),
  progress INTEGER NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT,
  message TEXT NOT NULL DEFAULT '',
  node TEXT NOT NULL DEFAULT ''
);
INSERT INTO tasks_no_pe (id,type,target_ref,status,progress,result,error,created_at,finished_at,message,node)
  SELECT id,type,target_ref,status,progress,result,error,created_at,finished_at,message,node FROM tasks WHERE type NOT IN ('pe_probe','model_adapt','identity_capture','identity_layer_rebuild');
DROP TABLE tasks;
ALTER TABLE tasks_no_pe RENAME TO tasks;
