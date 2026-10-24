package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/tianwei/diskless/internal/domain"
)

// Exercise the real startup migration against a populated public-release database,
// including task-table rebuilds and retirement of bundles without deleting uploaded packs.
func TestUpgradeFromPublicSchema47PreservesExistingData(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "public-release.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 47); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
	 INSERT INTO images(id,name,os_type,state,created_at) VALUES('win','Windows','windows','normal','2026-10-01T00:00:00Z');
	 INSERT INTO configs(id,image_id,name,default_reduction_id,created_at) VALUES('cfg','win','default','red','2026-10-01T00:00:00Z');
	 INSERT INTO reductions(id,config_id,name,display_name,status,created_at) VALUES('red','cfg','0','首次导入','ready','2026-10-01T00:00:00Z');
	 INSERT INTO groups(id,name,start_ip,client_max,gateway,netmask,system_image_id,system_config_id,system_reduction_id) VALUES('g','前台','10.0.0.10',10,'','255.255.255.0','win','cfg','red');
	 INSERT INTO terminals(id,name,mac,ip,group_id,state,pending_bundle_id,pending_bundle_at) VALUES('t','前台01','001122334455','10.0.0.10','g','offline','bundle','2026-10-01T00:00:00Z');
	 INSERT INTO driver_packs(id,name,category,os_type,status,storage_path,recommended,created_at) VALUES('pack','Intel','boot_critical_nic','windows','enabled','packs/intel.zip',TRUE,'2026-10-01T00:00:00Z');
	 INSERT INTO driver_bundles(id,name,os_type,created_at) VALUES('bundle','旧驱动集','windows','2026-10-01T00:00:00Z');
	 INSERT INTO driver_bundle_packs(id,bundle_id,pack_id) VALUES('bundle-pack','bundle','pack');
	 INSERT INTO system_settings(id,import_dir,client_iface,data_pool) VALUES('default','/tank/imports','ens33','tank');`); err != nil {
		t.Fatal(err)
	}
	old := New(db, "sqlite3")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	finished := now.Add(time.Minute)
	legacyTypes := []domain.TaskType{
		"import_image", "create_config", "delete_config", "create_reduction", "delete_reduction",
		"merge_config", "merge_reduction", "super_stop", "create_pool", "destroy_pool", "add_disk",
		"migrate_disk", "replace_disk", "add_read_cache", "remove_read_cache", "add_write_cache",
		"remove_write_cache", "flush_cache", "backup_dataset", "image_health_check", "detach_disk",
		"mirror_upgrade", "add_special", "remove_special", "add_spare", "remove_spare",
		"create_blank_image", "export_image", "publish_data_disk", "copy_image",
	}
	wantTasks := make([]domain.Task, 0, len(legacyTypes))
	for i, typ := range legacyTypes {
		task := domain.Task{ID: string(typ), Type: typ, TargetRef: "cfg", Status: domain.TaskStatusFailed,
			Progress: 37, Message: "保留进度消息", Result: "保留结果", Error: "保留失败原因",
			Node: "10.0.0.3", CreatedAt: now, FinishedAt: &finished}
		if i == 0 {
			task.Status, task.FinishedAt = domain.TaskStatusRunning, nil
		}
		if err := old.Tasks().Create(ctx, task); err != nil {
			t.Fatalf("seed legacy task %s: %v", typ, err)
		}
		wantTasks = append(wantTasks, task)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for _, want := range wantTasks {
		got, err := upgraded.Tasks().Get(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("historical %s changed during upgrade: got=%+v want=%+v err=%v", want.Type, got, want, err)
		}
	}
	for _, typ := range []domain.TaskType{"winpe_extract", "pe_probe", "model_adapt", "identity_capture", "identity_layer_rebuild", "install_image"} {
		if err := upgraded.Tasks().Create(ctx, domain.Task{ID: "new-" + string(typ), Type: typ, Status: domain.TaskStatusPending, CreatedAt: now}); err != nil {
			t.Fatalf("upgraded database must accept %s: %v", typ, err)
		}
	}
	term, err := upgraded.Terminals().Get(ctx, "t")
	if err != nil || term.Name != "前台01" || term.MAC != "001122334455" || term.IP != "10.0.0.10" || term.GroupID != "g" || term.State != domain.TerminalStateOffline || term.HardwareProfileID != "" || term.LastBootAt != nil {
		t.Fatalf("legacy terminal must retain data and start with unknown hardware: %+v err=%v", term, err)
	}
	group, err := upgraded.Groups().Get(ctx, "g")
	if err != nil || group.SystemImageID != "win" || group.SystemConfigID != "cfg" || group.SystemReductionID != "red" {
		t.Fatalf("group boot selection changed: %+v err=%v", group, err)
	}
	red, err := upgraded.Reductions().Get(ctx, "red")
	if err != nil || red.DisplayName != "首次导入" || red.Status != domain.ReductionStatusReady {
		t.Fatalf("restore point changed: %+v err=%v", red, err)
	}
	pack, err := upgraded.DriverPacks().Get(ctx, "pack")
	if err != nil || pack.StoragePath != "packs/intel.zip" || pack.Status != domain.DriverPackEnabled {
		t.Fatalf("uploaded driver must survive retiring its old bundle: %+v err=%v", pack, err)
	}
	settings, err := upgraded.SystemSettings().Get(ctx, "default")
	if err != nil || settings.ImportDir != "/tank/imports" || settings.ClientIface != "ens33" || settings.DataPool != "tank" || settings.KeepWindowsDefaults {
		t.Fatalf("existing settings and default Windows policy must remain usable: %+v err=%v", settings, err)
	}
}
