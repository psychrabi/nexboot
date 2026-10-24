package install

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 识别不再算校验值：复制 ISO 时顺带算出来；带着期望值（重新开始安装）就当场核对。
func TestPinISOComputesTheChecksumWhileCopying(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.iso")
	data := []byte("installation media")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(data))
	got, err := pinISO(context.Background(), src, filepath.Join(dir, "a.iso"), "")
	if err != nil || got != want {
		t.Fatalf("pin without expected checksum = %q, %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.iso")); string(b) != string(data) {
		t.Fatalf("pinned copy = %q", b)
	}
	if _, err := pinISO(context.Background(), src, filepath.Join(dir, "b.iso"), want); err != nil {
		t.Fatalf("matching checksum: %v", err)
	}
	if _, err := pinISO(context.Background(), src, filepath.Join(dir, "c.iso"), "sha-of-another-file"); err == nil || !strings.Contains(err.Error(), "安装文件已变化") {
		t.Fatalf("changed file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.iso")); !os.IsNotExist(err) {
		t.Fatalf("rejected copy must not stay behind: %v", err)
	}
}

func TestPreparingRecordsTheChecksumComputedWhileCopying(t *testing.T) {
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
		return Metadata{Path: "/iso/ubuntu.iso", Fingerprint: "fp", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64"}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir(), SHA256: "computed-while-copying", Files: map[string]string{"media.iso": "/install/media.iso"}}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/ubuntu.iso", SourceFingerprint: "fp", Name: "Ubuntu", SizeGiB: 64, MAC: "001122334455",
		IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-checksum-01"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.InstallSessions().Get(ctx, res.SessionID)
	if err != nil || s.Phase != "waiting" || s.SourceSHA256 != "computed-while-copying" {
		t.Fatalf("session phase=%s checksum=%q err=%v", s.Phase, s.SourceSHA256, err)
	}
}

// 复制完 ISO 后面的步骤失败（如提取 WinRE）：算出的校验值也要留在会话上，重新开始安装时才能核对。
func TestFailedPreparationKeepsTheChecksumOfThePinnedISO(t *testing.T) {
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
		return Metadata{Path: "/iso/win.iso", Fingerprint: "fp", OSType: domain.OSTypeWindows, Version: "11", Architecture: "amd64", Editions: []Edition{{Index: 1, Name: "Pro", Build: 22631}}}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir(), SHA256: "pinned"}, fmt.Errorf("无法提取 WinRE")
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	_, err = svc.Start(ctx, StartRequest{SourcePath: "/iso/win.iso", SourceFingerprint: "fp", EditionIndex: 1, Name: "Win11", SizeGiB: 64, MAC: "001122334455",
		IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-checksum-02"})
	if err == nil || !strings.Contains(err.Error(), "无法提取 WinRE") {
		t.Fatalf("start = %v", err)
	}
	all, err := st.InstallSessions().List(ctx)
	if err != nil || len(all) != 1 || all[0].Phase == "waiting" || all[0].SourceSHA256 != "pinned" {
		t.Fatalf("sessions = %+v err=%v", all, err)
	}
}
