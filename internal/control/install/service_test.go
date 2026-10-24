package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/hwprofile"
	"github.com/tianwei/diskless/internal/control/tasks"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

type diskFake struct {
	storage.StorageAgent
	saved, cloned bool
	deletedImage  string
	workRemoved   bool
	prepared      []storage.InstallationDiskReq
	savedScript   []byte
}

func (f *diskFake) DeleteImage(_ context.Context, id string, _ []string) error {
	f.deletedImage = id
	return nil
}
func (f *diskFake) CleanupClientClones(context.Context, string, []string) error {
	return fmt.Errorf("installation cleanup must not remove ordinary client clones")
}

func (f *diskFake) DataPoolName() string { return "tank" }

func TestInstallationRejectsBackupPoolBeforePreparingDisk(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Pools().Create(ctx, domain.Pool{ID: "backup", ServerID: "local", Name: "backup", Capacity: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) { return MediaFiles{}, nil }, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "", nil }}
	_, err = svc.Start(ctx, StartRequest{Name: "Ubuntu", PoolID: "backup", MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", SizeGiB: 64, Username: "user", Password: "Password123!"})
	if err == nil || !strings.Contains(err.Error(), "数据池") {
		t.Fatalf("backup pool accepted: %v", err)
	}
}

func (f *diskFake) PrepareInstallation(_ context.Context, req storage.InstallationDiskReq) (storage.LUNInfo, error) {
	f.prepared = append(f.prepared, req)
	return storage.LUNInfo{Server: "192.168.50.1", Target: "iqn.test:installation", System: storage.LUN{LUN: 0}}, nil
}
func (f *diskFake) SaveInstallation(_ context.Context, r storage.InstallationSaveReq) (storage.ImportImageResult, error) {
	f.saved = true
	f.savedScript = r.MountScript
	rid := r.ImageID + "_default_0"
	// 与存储层一致：Windows 带了脚本就是写进去了。
	injected := r.OSType == domain.OSTypeWindows && len(r.MountScript) > 0
	return storage.ImportImageResult{Image: domain.Image{ID: r.ImageID, Name: r.Name, OSType: r.OSType}, Config: domain.Config{ID: r.ImageID + "_default", ImageID: r.ImageID, Name: "default", DefaultReductionID: &rid}, Reduction: domain.Reduction{ID: rid, ConfigID: r.ImageID + "_default", Name: "@0"}, MountScriptInjected: injected}, nil
}
func (f *diskFake) CleanupInstallation(context.Context, storage.InstallationDiskReq) error {
	f.workRemoved = true
	return nil
}
func (f *diskFake) CleanupInstallationCandidate(ctx context.Context, req storage.InstallationCleanupReq) error {
	return f.DeleteImage(ctx, req.ImageID, nil)
}
func (f *diskFake) ActiveClientMACs(context.Context) ([]string, error) {
	if f.cloned {
		return []string{"001122334455"}, nil
	}
	return nil, nil
}
func (f *diskFake) CreateClientLUN(context.Context, storage.ClientReq) (storage.LUNInfo, error) {
	f.cloned = true
	return storage.LUNInfo{Server: "192.168.50.1", Target: "iqn.test:clone", System: storage.LUN{LUN: 0}}, nil
}

func TestInstallationStartsWithoutImageAndOnlyPublishesAfterOrdinaryBoot(t *testing.T) {
	tasks.SetNode("192.168.50.1")
	t.Cleanup(func() { tasks.SetNode("") })
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/ubuntu.iso", Fingerprint: "fp", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64"}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir(), Files: map[string]string{"media.iso": "/install/media.iso"}}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/ubuntu.iso", SourceFingerprint: "fp", Name: "Ubuntu", PoolID: "pool", SizeGiB: 64, MAC: "00:11:22:33:44:55", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "Password123!"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := st.InstallSessions().Get(ctx, res.SessionID)
	createdTask, err := st.Tasks().Get(ctx, res.TaskID)
	if err != nil || createdTask.Node != "192.168.50.1" || s.TaskID != createdTask.ID || createdTask.TargetRef != res.ImageID || createdTask.Status != domain.TaskStatusRunning {
		t.Fatalf("installation task must retain its origin and session relationship: %+v, %v", createdTask, err)
	}
	if _, ok, err := svc.BootScript(ctx, s.MAC); err != nil || !ok {
		t.Fatalf("boot override = %v, %v", ok, err)
	}
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "installing"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "initializing"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "installed"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finalize(ctx, res.SessionID); err != nil {
		t.Fatal(err)
	}
	if images, _ := st.Images().List(ctx); len(images) != 0 {
		t.Fatal("installer success must not publish a usable image")
	}
	s, _ = st.InstallSessions().Get(ctx, s.ID)
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "verified", BootID: s.BootID}); err == nil {
		t.Fatal("verification must require an ordinary clone issued this boot")
	}
	if _, ok, err := svc.BootScript(ctx, s.MAC); err != nil || !ok || !disk.cloned {
		t.Fatalf("ordinary boot = %v, %v", ok, err)
	}
	s, _ = st.InstallSessions().Get(ctx, s.ID)
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "verified", BootID: s.BootID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "verified", BootID: s.BootID}); err != nil {
		t.Fatalf("duplicate completion must be idempotent: %v", err)
	}
	if image, err := st.Images().Get(ctx, res.ImageID); err != nil || image.State != domain.ImageStateNormal {
		t.Fatalf("ready image = %+v, %v", image, err)
	}
}

func TestSetupRebootNeverFormatsAgainOrCreatesOrdinaryClone(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "setup", MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", OSType: domain.OSTypeWindows, Token: "setup-token", Phase: "installing", TaskID: "task-setup", Attempt: 1, SizeBytes: 64 << 30}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) {
		t.Fatal("setup reboots must never return to the formatting installer")
		return "", nil
	}}
	if script, ok, err := svc.BootScript(ctx, sess.MAC); err != nil || !ok || !strings.Contains(script, "interrupted") {
		t.Fatalf("incomplete install reboot = %q %v", script, err)
	}
	if err := svc.Report(ctx, sess.Token, Report{Phase: "initializing", Attempt: 0}); err == nil {
		t.Fatal("old attempts must not advance the installation")
	}
	if err := svc.Report(ctx, sess.Token, Report{Phase: "initializing", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if script, ok, err := svc.BootScript(ctx, sess.MAC); err != nil || !ok || !strings.Contains(script, "sanhook") || disk.cloned {
		t.Fatalf("system initialization must boot the persistent work disk: %q %v clone=%v", script, err, disk.cloned)
	}
}

func TestInstallationSaveWaitsForContinuousOfflineGrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	sess := domain.InstallSession{ID: "grace", MAC: "001122334455", Token: "grace-token", Phase: "installed", TaskID: "task-grace", ImageID: "candidate", Name: "candidate", Attempt: 1, SizeBytes: 64 << 30}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk, Now: func() time.Time { return now }}
	if err := svc.Finalize(ctx, sess.ID); err == nil || disk.saved {
		t.Fatal("first missing connection must not immediately save the disk")
	}
	now = now.Add(20 * time.Second)
	if err := svc.Finalize(ctx, sess.ID); err == nil || disk.saved {
		t.Fatal("offline grace has not elapsed")
	}
	disk.cloned = true
	if err := svc.Finalize(ctx, sess.ID); err == nil || disk.saved {
		t.Fatal("a reconnect must reset offline grace")
	}
	disk.cloned = false
	now = now.Add(30 * time.Second)
	if err := svc.Finalize(ctx, sess.ID); err == nil || disk.saved {
		t.Fatal("must start a new grace period after reconnect")
	}
	now = now.Add(31 * time.Second)
	if err := svc.Finalize(ctx, sess.ID); err != nil || !disk.saved {
		t.Fatalf("safe save = %v saved=%v", err, disk.saved)
	}
}

func TestInstallationAccountRejectsOperatingSystemReservedNames(t *testing.T) {
	for _, name := range []string{"administrator", "guest", "root", "defaultaccount", "wdagutilityaccount", "nobody", "user_name_over_20_chars"} {
		if validInitialAccount(name) {
			t.Errorf("reserved or overlong account accepted: %s", name)
		}
	}
	if !validInitialAccount("nexboot") {
		t.Fatal("normal account rejected")
	}
}

func TestInstallationTimeoutPreservesDiskAndDoesNotExpireUserActionStages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	for _, phase := range []string{"installing", "initializing", "waiting", "verifying"} {
		sess := domain.InstallSession{ID: phase, MAC: fmt.Sprintf("0011223344%02d", len(phase)), Token: phase + "-token", Phase: phase, TaskID: phase + "-task", UpdatedAt: now.Add(-3 * time.Hour)}
		if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
			t.Fatal(err)
		}
		if err := st.InstallSessions().Create(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	svc := Service{Store: st, Now: func() time.Time { return now }}
	svc.Resume(ctx)
	for _, phase := range []string{"installing", "initializing", "waiting", "verifying"} {
		sess, err := st.InstallSessions().Get(ctx, phase)
		if err != nil {
			t.Fatal(err)
		}
		if phase == "installing" || phase == "initializing" {
			if sess.Phase != "failed" || !sess.Active() || !strings.Contains(sess.Error, "任务") {
				t.Fatalf("stalled installer must stop safely: %+v", sess)
			}
		} else if sess.Phase != phase {
			t.Fatalf("stage awaiting user action expired: %+v", sess)
		}
	}
}

func TestCancelCleansCandidateEvenWhenSaveResultWasNotRecorded(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "crash-save", MAC: "001122334455", Token: "token", Phase: "saving", TaskID: "crash-save-task", ImageID: "iso_unrecorded", Password: "private-password", MediaDir: t.TempDir()}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.MediaDir, "password.hash"), []byte("private-hash"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sess.MediaDir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.MediaDir, "logs", "console.log"), []byte("private-password private-hash"), 0600); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk}
	if _, err := svc.Cancel(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if disk.deletedImage != sess.ImageID {
		t.Fatalf("unrecorded candidate was leaked: %q", disk.deletedImage)
	}
	stored, _ := st.InstallSessions().Get(ctx, sess.ID)
	if stored.Password != "" || stored.Phase != "cancelled" {
		t.Fatalf("private session not finalized: %+v", stored)
	}
	if _, err := os.Stat(filepath.Join(sess.MediaDir, "password.hash")); !os.IsNotExist(err) {
		t.Fatal("installation credentials retained after cancel")
	}
	b, err := os.ReadFile(filepath.Join(sess.MediaDir, "logs", "console.log"))
	if err != nil || strings.Contains(string(b), "private-") {
		t.Fatalf("sanitized diagnostics not retained: %q %v", b, err)
	}
}

func TestCancelUsesExactInstallationCleanup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "cancel-waiting", MAC: "001122334455", Token: "token", Phase: "waiting", TaskID: "cancel-waiting-task", ImageID: "iso_cancelled"}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk}
	if _, err := svc.Cancel(ctx, sess.ID); err != nil {
		t.Fatalf("work disk prevented its own cancellation: %v", err)
	}
}

func TestCancelFailedPreparationRemovesPrivateResources(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk, InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/ubuntu.iso", Fingerprint: "fp", OSType: domain.OSTypeLinux, Architecture: "amd64"}, nil
	}, Prepare: func(_ context.Context, sess domain.InstallSession) (MediaFiles, error) {
		for name, content := range map[string]string{"media.iso": "partial ISO", "password.hash": "private-hash"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(filepath.Join(dir, "logs"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "logs", "console.log"), []byte(sess.Password+" private-hash"), 0600); err != nil {
			t.Fatal(err)
		}
		return MediaFiles{Dir: dir}, fmt.Errorf("Ubuntu package download interrupted")
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "", nil }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/ubuntu.iso", SourceFingerprint: "fp", Name: "Ubuntu", PoolID: "pool", SizeGiB: 64, MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "PrivatePassword!"})
	if err == nil || !strings.Contains(err.Error(), "Ubuntu package download interrupted") {
		t.Fatalf("preparation error lost: %v", err)
	}
	if failed, err := svc.Get(ctx, res.SessionID); err != nil || failed.Phase != "failed" {
		t.Fatalf("failed installation = %+v, %v", failed, err)
	}
	if cancelled, err := svc.Cancel(ctx, res.SessionID); err != nil || cancelled.Phase != "cancelled" {
		t.Fatalf("cancel failed preparation = %+v, %v", cancelled, err)
	}
	for _, name := range []string{"media.iso", "password.hash"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("cancel retained installation resource %s: %v", name, err)
		}
	}
	log, err := os.ReadFile(filepath.Join(dir, "logs", "console.log"))
	if err != nil || strings.Contains(string(log), "PrivatePassword!") || strings.Contains(string(log), "private-hash") {
		t.Fatalf("sanitized diagnostics not retained: %q, %v", log, err)
	}
}

func TestLateFailureReportCannotReverseCancellation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "cancel-report", MAC: "001122334455", Token: "cancel-report-token", Phase: "cancelling", Attempt: 1, TaskID: "cancel-report-task"}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: st}
	if err := svc.Report(ctx, sess.Token, Report{Attempt: 1, Phase: "failed", Message: "late setup failure"}); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.InstallSessions().Get(ctx, sess.ID)
	if stored.Phase != "cancelling" {
		t.Fatalf("late report reversed user's cancellation: %s", stored.Phase)
	}
}

// 安装文件与安装盘都在应答这台上，池由节点唯一确定：前任主机的池（切换后重试）或不传，都用本机数据池。
func TestStartUsesThisNodesDataPoolWhateverPoolIsRequested(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, srv := range []domain.Server{{ID: "local", Name: "local", IP: "192.168.50.1"}, {ID: "old", Name: "old", IP: "192.168.50.2"}} {
		srv.Role, srv.Status = domain.ServerRoleAll, domain.ServerStatusUp
		if err := st.Servers().Create(ctx, srv); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []domain.Pool{
		{ID: "pool-old--tank", Name: "tank", ServerID: "old", Capacity: 1 << 40},
		{ID: "pool-local--backup", Name: "backup", ServerID: "local", Capacity: 1 << 40},
		{ID: "pool-local--tank", Name: "tank", ServerID: "local", Capacity: 1 << 40},
	} {
		if err := st.Pools().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	disk := &diskFake{}
	svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/ubuntu.iso", Fingerprint: "fp", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64"}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir(), Files: map[string]string{"media.iso": "/install/media.iso"}}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	for i, requested := range []string{"pool-old--tank", ""} {
		res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/ubuntu.iso", SourceFingerprint: "fp", Name: fmt.Sprintf("Ubuntu-%d", i), PoolID: requested, SizeGiB: 64,
			MAC: fmt.Sprintf("00112233445%d", i), IP: fmt.Sprintf("192.168.50.1%d", i), Netmask: "255.255.255.0", Username: "user", Password: "pw-pool-0001"})
		if err != nil {
			t.Fatalf("requested %q: %v", requested, err)
		}
		s, _ := st.InstallSessions().Get(ctx, res.SessionID)
		if s.PoolID != "pool-local--tank" || s.ServerID != "local" {
			t.Fatalf("requested %q installed to %s on %s", requested, s.PoolID, s.ServerID)
		}
	}
}

// 安装盘是稀疏卷，按满额判断会把放得下的安装拦掉；按预计实际写入判断，拒绝时点名池、剩余与需要。
func TestStartChecksSpaceByExpectedWritesNotFullDiskSize(t *testing.T) {
	ctx := context.Background()
	iso := int64(72) << 30 / 10
	for _, tc := range []struct {
		name  string
		free  int64
		ok    bool
		parts []string
	}{
		{"剩 55 GiB 装 64 GiB 系统盘", 55 << 30, true, nil},
		{"只剩 30 GiB", 30 << 30, false, []string{"「tank」", "剩余 30.0 GiB", "预计需要约 47.2 GiB", "清理"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
				t.Fatal(err)
			}
			if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 96 << 30, Used: 96<<30 - tc.free}); err != nil {
				t.Fatal(err)
			}
			disk := &diskFake{}
			svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
				return Metadata{Path: "/iso/win.iso", Fingerprint: "fp", SizeBytes: iso, OSType: domain.OSTypeWindows, Version: "Windows 11", Architecture: "amd64", Editions: []Edition{{Index: 1, Name: "Pro"}}}, nil
			}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
				return MediaFiles{Dir: t.TempDir()}, nil
			}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
			_, err = svc.Start(ctx, StartRequest{SourcePath: "/iso/win.iso", SourceFingerprint: "fp", EditionIndex: 1, Name: "Win11", SizeGiB: 64,
				MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-space-0001"})
			if tc.ok {
				if err != nil {
					t.Fatalf("should fit: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("should be rejected")
			}
			for _, part := range tc.parts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q lacks %q", err, part)
				}
			}
		})
	}
}

type livePoolDisk struct {
	*diskFake
	status domain.PoolStatus
}

func (d livePoolDisk) PoolStatus(context.Context, string) (domain.PoolStatus, error) {
	return d.status, nil
}

// 刚清理完上一次安装时，库里的池记录要等心跳才刷新；空间检查以存储池实时余量为准。
func TestStartChecksSpaceAgainstLivePoolUsage(t *testing.T) {
	ctx := context.Background()
	iso := int64(72) << 30 / 10
	for _, tc := range []struct {
		name                 string
		recordFree, liveFree int64
		ok                   bool
	}{
		{"记录过期偏少，实时够用", 30 << 30, 60 << 30, true},
		{"记录偏多，实时不够", 60 << 30, 30 << 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			_ = st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp})
			_ = st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 96 << 30, Used: 96<<30 - tc.recordFree})
			disk := &diskFake{}
			live := livePoolDisk{diskFake: disk, status: domain.PoolStatus{Name: "tank", Capacity: 96 << 30, Used: 96<<30 - tc.liveFree}}
			svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: live, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
				return Metadata{Path: "/iso/win.iso", Fingerprint: "fp", SizeBytes: iso, OSType: domain.OSTypeWindows, Architecture: "amd64", Editions: []Edition{{Index: 1, Name: "Pro"}}}, nil
			}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
				return MediaFiles{Dir: t.TempDir()}, nil
			}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
			_, err = svc.Start(ctx, StartRequest{SourcePath: "/iso/win.iso", SourceFingerprint: "fp", EditionIndex: 1, Name: "Win11", SizeGiB: 64,
				MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-live-0001"})
			if tc.ok && err != nil {
				t.Fatalf("live pool has room: %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "剩余 30.0 GiB")) {
				t.Fatalf("must reject with live numbers: %v", err)
			}
		})
	}
}

// ISO 做的 Windows 镜像保存时就烘焙标准启动器并记下版本，客户机开机不再逐台注入；
// 验证那一次开机由 /boot/mount-script 另带验证回报。
func TestWindowsInstallationBakesTheLauncherAndRecordsItsVersion(t *testing.T) {
	tasks.SetNode("192.168.50.1")
	t.Cleanup(func() { tasks.SetNode("") })
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/win10.iso", Fingerprint: "fp", OSType: domain.OSTypeWindows, Distribution: "windows", Version: "Windows 10", Architecture: "amd64", Editions: []Edition{{Index: 1, Name: "Windows 10 Pro", Build: 19041}}}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir()}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil },
		MountScript:     func(domain.InstallSession) []byte { return []byte("standard+verify") },
		BakeMountScript: func() ([]byte, string) { return []byte("standard"), "bake-v1" }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/win10.iso", SourceFingerprint: "fp", EditionIndex: 1, Name: "Win10", PoolID: "pool", SizeGiB: 64, MAC: "00:11:22:33:44:55", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "Password123!"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := st.InstallSessions().Get(ctx, res.SessionID)
	for _, phase := range []string{"installing", "initializing", "installed"} {
		if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: phase}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := svc.VerificationMountScript(ctx, s.MAC); ok {
		t.Fatal("还没保存就给了验证版脚本")
	}
	if err := svc.Finalize(ctx, res.SessionID); err != nil {
		t.Fatal(err)
	}
	if string(disk.savedScript) != "standard" {
		t.Fatalf("烘焙进镜像的 = %q，应是标准挂盘脚本", disk.savedScript)
	}
	s, _ = st.InstallSessions().Get(ctx, s.ID)
	if s.CandidateImage.MountScriptVersion != "bake-v1" {
		t.Fatalf("候选镜像版本 = %q，开机路径会逐台注入", s.CandidateImage.MountScriptVersion)
	}
	if script, ok, err := svc.VerificationMountScript(ctx, "00-11-22-33-44-55"); err != nil || !ok || string(script) != "standard+verify" {
		t.Fatalf("验证阶段脚本 = %q, %v, %v", script, ok, err)
	}
	if _, ok, err := svc.BootScript(ctx, s.MAC); err != nil || !ok {
		t.Fatalf("verification boot = %v, %v", ok, err)
	}
	s, _ = st.InstallSessions().Get(ctx, s.ID)
	if err := svc.Report(ctx, s.Token, Report{Attempt: 1, Phase: "verified", BootID: s.BootID}); err != nil {
		t.Fatal(err)
	}
	if image, err := st.Images().Get(ctx, res.ImageID); err != nil || image.MountScriptVersion != "bake-v1" {
		t.Fatalf("发布的镜像 = %+v, %v", image, err)
	}
	if _, ok, _ := svc.VerificationMountScript(ctx, s.MAC); ok {
		t.Fatal("发布后不该再带验证回报")
	}
}

// 识别不再算整张校验值：识别到开始安装之间用文件指纹防换文件，当场拒绝；
// 重新开始安装只带着原会话的校验值，复制 ISO 时再核对。
func TestStartChecksTheIdentifiedFileByFingerprint(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/ubuntu.iso", Fingerprint: "fp-now", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64"}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir(), Files: map[string]string{"media.iso": "/install/media.iso"}}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	req := func(i int) StartRequest {
		return StartRequest{SourcePath: "/iso/ubuntu.iso", Name: fmt.Sprintf("Ubuntu-%d", i), SizeGiB: 64, MAC: fmt.Sprintf("00112233445%d", i),
			IP: fmt.Sprintf("192.168.50.1%d", i), Netmask: "255.255.255.0", Username: "user", Password: "pw-fingerprint-1"}
	}

	stale := req(0)
	stale.SourceFingerprint = "fp-before"
	if _, err := svc.Start(ctx, stale); err == nil || !strings.Contains(err.Error(), "安装文件已变化") {
		t.Fatalf("replaced file: %v", err)
	}
	none := req(0)
	if _, err := svc.Start(ctx, none); err == nil || !strings.Contains(err.Error(), "识别") {
		t.Fatalf("unidentified file: %v", err)
	}

	fresh := req(1)
	fresh.SourceFingerprint = "fp-now"
	res, err := svc.Start(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := st.InstallSessions().Get(ctx, res.SessionID); s.SourceSHA256 != "" {
		t.Fatalf("checksum is filled in while copying the ISO, got %q at start", s.SourceSHA256)
	}

	restart := req(2)
	restart.SourceSHA256 = "sha-of-first-attempt"
	res, err = svc.Start(ctx, restart)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := st.InstallSessions().Get(ctx, res.SessionID); s.SourceSHA256 != "sha-of-first-attempt" {
		t.Fatalf("restart must carry the expected checksum to the copy step, got %q", s.SourceSHA256)
	}
}

func TestVerifiedInstallationRecordsOnlyItsCandidateBoot(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		newer, mismatch, interleaved bool
	}{
		{name: "exact hardware"}, {name: "different hardware", mismatch: true}, {name: "newer ordinary boot", newer: true}, {name: "boot changes during verified report", interleaved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
			started := now
			old := now.Add(-time.Hour)
			p := domain.HardwareProfile{ID: "current-hardware", NICVendor: "8086", NICDevice: "100f", Platform: "efi", BusLoc: "16"}
			if tc.mismatch {
				p.NICDevice = "07b0"
			}
			if err := st.HardwareProfiles().Create(ctx, p); err != nil {
				t.Fatal(err)
			}
			for _, e := range []error{
				st.Images().Create(ctx, domain.Image{ID: "old-image", Name: "old", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal}),
				st.Configs().Create(ctx, domain.Config{ID: "old-config", ImageID: "old-image", Name: "default"}),
				st.Reductions().Create(ctx, domain.Reduction{ID: "deleted-old-point", ConfigID: "old-config", Name: "@0", Status: domain.ReductionStatusReady}),
				st.Groups().Create(ctx, domain.Group{ID: "g", Name: "g", ClientMax: 10, SystemImageID: "old-image", SystemConfigID: "old-config", SystemReductionID: "deleted-old-point"}),
			} {
				if e != nil {
					t.Fatal(e)
				}
			}
			terminal := domain.Terminal{ID: "client", GroupID: "g", IP: "10.0.0.2", MAC: "001122334455", State: domain.TerminalStateOffline, HardwareProfileID: p.ID, LastBootProfileID: "stale-profile", LastBootReductionID: "deleted-old-point", LastBootAt: &old, LastBootOKAt: &old}
			if err := st.Terminals().Create(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			other := domain.Terminal{ID: "other", GroupID: "g", IP: "10.0.0.3", MAC: "001122334466", State: domain.TerminalStateOffline, LastBootAt: &old}
			if err := st.Terminals().Create(ctx, other); err != nil {
				t.Fatal(err)
			}
			sess := domain.InstallSession{ID: "verify", MAC: terminal.MAC, IP: "10.0.0.2", Netmask: "255.255.255.0", Token: "token", Phase: "verifying", Attempt: 1, TaskID: "task", OSType: domain.OSTypeWindows, Firmware: "efi", HWID: `PCI\VEN_8086&DEV_100F`, BusLoc: "16", CandidateImage: domain.Image{ID: "new-image", OSType: domain.OSTypeWindows}, CandidateConfig: domain.Config{ID: "new-config", ImageID: "new-image"}, CandidateReduction: domain.Reduction{ID: "new-point", ConfigID: "new-config", Name: "@0"}}
			if err := st.Tasks().Create(ctx, domain.Task{ID: "task", Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusRunning}); err != nil {
				t.Fatal(err)
			}
			if err := st.InstallSessions().Create(ctx, sess); err != nil {
				t.Fatal(err)
			}
			svc := Service{Store: st, Storage: &diskFake{}, Now: func() time.Time { return now }}
			if _, ok, err := svc.BootScript(ctx, sess.MAC); err != nil || !ok {
				t.Fatalf("boot %v %v", ok, err)
			}
			sess, _ = st.InstallSessions().Get(ctx, sess.ID)
			if err := svc.Report(ctx, sess.Token, Report{Attempt: 1, Phase: "verified", BootID: "wrong-boot"}); err == nil {
				t.Fatal("wrong BootID accepted")
			}
			now = now.Add(time.Minute)
			// An unrelated progress timestamp must not become the boot's start timestamp.
			sess.UpdatedAt = now
			if err := st.InstallSessions().Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
			if tc.newer {
				terminal.LastBootAt = &now
				terminal.LastBootReductionID = "later-point"
				if err := st.Terminals().Update(ctx, terminal); err != nil {
					t.Fatal(err)
				}
			}
			if tc.interleaved {
				svc.Store = bootChangedDuringVerificationStore{Store: st, at: now}
			}
			if err := svc.Report(ctx, sess.Token, Report{Attempt: 1, Phase: "verified", BootID: sess.BootID}); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Terminals().Get(ctx, terminal.ID)
			if tc.newer || tc.interleaved {
				if got.LastBootReductionID != "later-point" || !got.LastBootOKAt.Equal(old) {
					t.Fatalf("late report rewrote newer boot: %+v", got)
				}
			} else {
				if got.LastBootAt == nil || !got.LastBootAt.Equal(started) || got.LastBootOKAt == nil || !got.LastBootOKAt.Equal(now) || got.LastBootReductionID != "new-point" {
					t.Fatalf("verified boot ledger: %+v", got)
				}
				wantProfile := p.ID
				if tc.mismatch {
					wantProfile = ""
				}
				if got.LastBootProfileID != wantProfile {
					t.Fatalf("profile=%q want %q", got.LastBootProfileID, wantProfile)
				}
			}
			if tc.mismatch {
				if err := (hwprofile.Service{Store: st, Now: func() time.Time { return now }}).NoteBooted(ctx, got); err != nil {
					t.Fatal(err)
				}
				ads, err := st.ModelAdaptations().List(ctx)
				if err != nil || len(ads) != 0 {
					t.Fatalf("later callback borrowed unverified profile: %+v %v", ads, err)
				}
			}
			otherGot, _ := st.Terminals().Get(ctx, other.ID)
			if otherGot.LastBootOKAt != nil {
				t.Fatal("another MAC marked successful")
			}
			now = now.Add(time.Minute)
			got.LastBootReductionID = "after-ready"
			if err := st.Terminals().Update(ctx, got); err != nil {
				t.Fatal(err)
			}
			if err := svc.Report(ctx, sess.Token, Report{Attempt: 1, Phase: "verified", BootID: sess.BootID}); err != nil {
				t.Fatal(err)
			}
			again, _ := st.Terminals().Get(ctx, got.ID)
			if again.LastBootReductionID != "after-ready" {
				t.Fatal("duplicate report rewrote newer boot")
			}
		})
	}
}

// The exact candidate row is read before another boot replaces its ledger.
type bootChangedDuringVerificationStore struct {
	store.Store
	at time.Time
}

func (s bootChangedDuringVerificationStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.Tx(ctx, func(tx store.Store) error { return fn(bootChangedDuringVerificationStore{Store: tx, at: s.at}) })
}
func (s bootChangedDuringVerificationStore) Terminals() store.TerminalRepo {
	return bootChangedDuringVerificationRepo{TerminalRepo: s.Store.Terminals(), at: s.at}
}

type bootChangedDuringVerificationRepo struct {
	store.TerminalRepo
	at time.Time
}

func (r bootChangedDuringVerificationRepo) GetByMAC(ctx context.Context, mac string) (domain.Terminal, error) {
	old, err := r.TerminalRepo.GetByMAC(ctx, mac)
	if err != nil {
		return old, err
	}
	fresh := old
	fresh.LastBootAt = &r.at
	fresh.LastBootReductionID = "later-point"
	return old, r.TerminalRepo.Update(ctx, fresh)
}

func TestBootScriptBIOSNetworkBootReturnsClearIpxePrompt(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{
		ID: "install-bios", MAC: "000C29D6C0E2", IP: "192.168.10.222", Netmask: "255.255.255.0",
		Token: "tok", Phase: "waiting", Attempt: 1, TaskID: "task", Firmware: "pcbios",
		OSType: domain.OSTypeLinux, Distribution: "anaconda-centos",
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: st, Host: &diskFake{}}
	script, ok, err := svc.BootScript(ctx, sess.MAC)
	if err != nil || !ok {
		t.Fatalf("BIOS ISO boot must return a prompt script, not an error: ok=%v err=%v", ok, err)
	}
	for _, want := range []string{
		"#!ipxe",
		"platform=pcbios",
		"BIOS/Legacy",
		"UEFI",
		"snponly.efi",
		"undionly.kpxe",
		"VMware",
		"Firmware = EFI",
		"Network only",
		"Waiting for client",
		"sleep 30",
		"exit",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("prompt missing %q in:\n%s", want, script)
		}
	}
	// BIOS VGA cannot render UTF-8; keep the on-screen script ASCII-only.
	for _, r := range script {
		if r > 127 {
			t.Fatalf("iPXE prompt must be ASCII-only, found %q (U+%04X) in:\n%s", r, r, script)
		}
	}
	// EFI must get past the firmware gate and receive the normal waiting-phase script.
	sess.Firmware = "efi"
	if err := st.InstallSessions().Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc.Prepare = func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir()}, nil
	}
	svc.Render = func(context.Context, domain.InstallSession, MediaFiles) (string, error) {
		return "#!ipxe\nsanhook\n", nil
	}
	script, ok, err = svc.BootScript(ctx, sess.MAC)
	if err != nil || !ok || !strings.Contains(script, "sanhook") || strings.Contains(script, "BIOS/Legacy") {
		t.Fatalf("EFI waiting boot = %q ok=%v err=%v", script, ok, err)
	}
}
