package install

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 会话存文件库内的相对路径：换到池名不同的主机上，按那台的文件库解析到同一个文件；旧会话的绝对路径照用。
func TestSourcePathIsStoredRelativeToTheFilesDir(t *testing.T) {
	ctx := context.Background()
	nodeA, nodeB := t.TempDir(), t.TempDir()
	filesA := func(context.Context) (string, error) { return nodeA, nil }
	filesB := func(context.Context) (string, error) { return nodeB, nil }
	rel := storedSourcePath(ctx, filesA, filepath.Join(nodeA, "imports", "win10.iso"))
	if rel != filepath.Join("imports", "win10.iso") {
		t.Fatalf("文件库内应存相对路径：%q", rel)
	}
	if got, _ := resolveSourcePath(ctx, filesB, rel); got != filepath.Join(nodeB, "imports", "win10.iso") {
		t.Fatalf("另一台应按自己的文件库解析：%q", got)
	}
	for _, legacy := range []string{"/var/lib/ndiskless/imports/win10.iso", filepath.Join(nodeA, "..", "x.iso")} {
		abs := filepath.Clean(legacy)
		if got := storedSourcePath(ctx, filesA, abs); got != abs {
			t.Fatalf("文件库外保留绝对路径：%q → %q", abs, got)
		}
		if got, _ := resolveSourcePath(ctx, filesB, abs); got != abs {
			t.Fatalf("旧会话的绝对路径原样使用：%q", got)
		}
	}
	if got := storedSourcePath(ctx, nil, "/x/y.iso"); got != "/x/y.iso" {
		t.Fatalf("没有文件库时保留原样：%q", got)
	}
}

// 新建安装：库里记相对路径，界面拿到本机绝对路径（与 ISO 列表一致）。
func TestStartStoresTheRelativeSourceAndViewsResolveIt(t *testing.T) {
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
	root := t.TempDir()
	src := filepath.Join(root, "imports", "ubuntu.iso")
	disk := &diskFake{}
	svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080",
		FilesDir: func(context.Context) (string, error) { return root, nil },
		InspectSource: func(context.Context, string) (Metadata, error) {
			return Metadata{Path: src, Fingerprint: "fp", OSType: domain.OSTypeLinux, Distribution: "ubuntu-server", Version: "24.04", Architecture: "amd64"}, nil
		}, Prepare: func(_ context.Context, s domain.InstallSession) (MediaFiles, error) {
			return MediaFiles{Dir: t.TempDir(), SHA256: "x", Files: map[string]string{"media.iso": "/install/media.iso"}}, nil
		}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: src, SourceFingerprint: "fp", Name: "Ubuntu", SizeGiB: 64, MAC: "001122334455",
		IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-relpath-01"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := st.InstallSessions().Get(ctx, res.SessionID)
	if err != nil || raw.SourcePath != filepath.Join("imports", "ubuntu.iso") {
		t.Fatalf("库里应存相对路径：%q %v", raw.SourcePath, err)
	}
	v, err := svc.Get(ctx, res.SessionID)
	if err != nil || v.SourcePath != src {
		t.Fatalf("Get 应给绝对路径：%q %v", v.SourcePath, err)
	}
	list, _ := svc.List(ctx)
	if len(list.Items) != 1 || list.Items[0].SourcePath != src {
		t.Fatalf("List 应给绝对路径：%+v", list.Items)
	}
}

// 导入目录与安装目录同在文件库：固定 ISO 用硬链接，不复制。
func TestPinISOHardlinksWithinTheSameFilesystem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.iso")
	if err := os.WriteFile(src, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pinISO(context.Background(), src, filepath.Join(dir, "pinned.iso"), ""); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(src)
	b, _ := os.Stat(filepath.Join(dir, "pinned.iso"))
	if a.Sys().(*syscall.Stat_t).Ino != b.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("同一文件系统内应硬链接")
	}
	if _, err := os.Stat(filepath.Join(dir, ".iso-link")); !os.IsNotExist(err) {
		t.Fatalf("不该留下临时链接：%v", err)
	}
}
