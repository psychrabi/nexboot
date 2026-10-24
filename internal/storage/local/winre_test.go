package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/zfs"
)

func TestFindWinREPrefersRecoveryPartition(t *testing.T) {
	rec := t.TempDir()
	writeTree(t, rec, map[string]string{"Recovery/WindowsRE/Winre.wim": "wim", "Recovery/WindowsRE/boot.sdi": "sdi"})
	win := t.TempDir()
	writeTree(t, win, map[string]string{
		"Windows/System32/ntoskrnl.exe": "x", "Windows/System32/Recovery/Winre.wim": "old",
		"Windows/System32/curl.exe": "curl", "Windows/Boot/DVD/EFI/BCD": "efi", "Windows/Boot/DVD/PCAT/BCD": "pcat",
	})
	files, ok := findWinRE([]mountedFS{{Root: win, FSType: "ntfs"}, {Root: rec, FSType: "ntfs"}})
	if !ok || !strings.HasPrefix(files.WIM, rec) || !strings.HasPrefix(files.SDI, rec) {
		t.Fatalf("files = %+v ok = %v", files, ok)
	}
	// 回报工具和启动配置在系统分区上，跟着一起带出。
	if !strings.HasSuffix(files.Curl, "curl.exe") || !strings.HasSuffix(files.BCDEFI, filepath.FromSlash("EFI/BCD")) || !strings.HasSuffix(files.BCDPCAT, filepath.FromSlash("PCAT/BCD")) {
		t.Fatalf("extras = %+v", files)
	}
}

// 没有恢复分区时 WinRE 在系统分区的 System32\Recovery 下，boot.sdi 在 Windows\Boot\DVD 下。
func TestFindWinREFallsBackToSystemPartition(t *testing.T) {
	win := t.TempDir()
	writeTree(t, win, map[string]string{
		"Windows/System32/Recovery/WinRE.wim": "wim",
		"Windows/Boot/DVD/EFI/boot.sdi":       "sdi",
	})
	files, ok := findWinRE([]mountedFS{{Root: win, FSType: "ntfs"}})
	if !ok || !strings.HasSuffix(files.WIM, "WinRE.wim") || !strings.HasSuffix(files.SDI, filepath.FromSlash("DVD/EFI/boot.sdi")) {
		t.Fatalf("files = %+v ok = %v", files, ok)
	}
}

func TestFindWinREReportsMissing(t *testing.T) {
	win := t.TempDir()
	writeTree(t, win, map[string]string{"Windows/System32/ntoskrnl.exe": "x"})
	if _, ok := findWinRE([]mountedFS{{Root: win, FSType: "ntfs"}}); ok {
		t.Fatal("没有 Winre.wim 不能报找到")
	}
	rec := t.TempDir()
	writeTree(t, rec, map[string]string{"Recovery/WindowsRE/Winre.wim": "wim"}) // 缺 boot.sdi
	if _, ok := findWinRE([]mountedFS{{Root: rec, FSType: "ntfs"}}); ok {
		t.Fatal("缺 boot.sdi 时不能报找到")
	}
}

// 拷贝走临时名再改名，中途失败不会留下半截文件被当成可用环境；哈希随拷贝计算。
func TestCopyWinREFileHashesAndRenames(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Winre.wim")
	if err := os.WriteFile(src, []byte("hello winre"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "sub", "boot.wim")
	sum, size, err := copyWinREFile(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("hello winre"))
	if sum != hex.EncodeToString(h[:]) || size != int64(len("hello winre")) {
		t.Fatalf("sum = %s size = %d", sum, size)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Fatalf("目标目录应只剩成品：%v", entries)
	}
}

// 镜像里没有 WinRE：报操作者能懂的错，临时克隆照样销毁。
func TestExtractWinREFailsCleanlyWithoutRecoveryEnvironment(t *testing.T) {
	zfsRunner := &fakeRunner{}
	agent := New("server-a", zfs.New("tank", zfsRunner, slog.New(slog.NewTextHandler(io.Discard, nil))))
	agent.runner = &fakeRunner{lsblkOutput: `{"blockdevices":[{"path":"/dev/zvol/tank/run/x","fstype":"","children":[{"path":"/dev/zvol/tank/run/x-part1","fstype":"ntfs"}]}]}`}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := agent.ExtractWinRE(ctx, storage.ExtractWinREReq{ImageID: "win11", ConfigID: "win11_default", SnapshotName: "0", DestDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "恢复环境") {
		t.Fatalf("err = %v", err)
	}
	if len(zfsRunner.calls) != 2 || zfsRunner.calls[0].args[0] != "clone" || zfsRunner.calls[1].args[0] != "destroy" {
		t.Fatalf("zfs calls = %#v", zfsRunner.calls)
	}
	if !strings.HasPrefix(zfsRunner.calls[0].args[2], "tank/run/INSPECT-WINRE-WIN11-") {
		t.Fatalf("clone name = %q", zfsRunner.calls[0].args[2])
	}
}
