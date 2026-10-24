package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func poolImports(dir string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return dir, nil }
}

// 导入目录就是本节点存储池上的 imports 目录：池名不同的节点各得各的路径，库里旧的设置值不起作用。
func TestImportDirIsThePoolImportsDirOnEachNode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := newSettingsTestStore(t)
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, ImportDir: "/ndiskless/data/imports"}); err != nil {
		t.Fatal(err)
	}
	for _, pool := range []string{"data", "tank"} {
		want := filepath.Join(root, pool, "nd", "files", "imports")
		svc := SystemSettingsService{Store: st, ImportsDir: poolImports(want), LegacyImportDir: "/var/lib/ndiskless/imports"}
		got, err := svc.ImportDir(ctx)
		if err != nil || got != want {
			t.Fatalf("池 %s：%q %v，要 %q", pool, got, err, want)
		}
		if info, err := os.Stat(want); err != nil || !info.IsDir() {
			t.Fatalf("池 %s 的导入目录没建出来：%v", pool, err)
		}
		view, err := svc.Get(ctx)
		if err != nil || view.ImportDir != want || view.ImportDirError != "" {
			t.Fatalf("池 %s 的设置视图：%#v %v", pool, view, err)
		}
	}
}

// 没有存储池就明确说先建池，不悄悄退回系统盘上的目录。
func TestImportDirWithoutPoolSaysCreateAPool(t *testing.T) {
	ctx := context.Background()
	for name, svc := range map[string]SystemSettingsService{
		"未接存储": {Store: newSettingsTestStore(t)},
		"池不存在": {Store: newSettingsTestStore(t), ImportsDir: func(context.Context) (string, error) { return "", errors.New("dataset does not exist") }},
	} {
		if _, err := svc.ImportDir(ctx); !errors.Is(err, errs.ErrInvalid) || !strings.Contains(err.Error(), "请先创建存储池") {
			t.Fatalf("%s：err=%v", name, err)
		}
		view, err := svc.Get(ctx)
		if err != nil || view.ImportDir != "" || !strings.Contains(view.ImportDirError, "请先创建存储池") {
			t.Fatalf("%s：设置视图 %#v %v", name, view, err)
		}
	}
}

// 旧控制台仍会提交 import_dir：忽略并提示实际目录，库里的旧值不改（只用来列出旧文件）。
func TestSaveIgnoresImportDirWithWarning(t *testing.T) {
	ctx := context.Background()
	st := newSettingsTestStore(t)
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, ImportDir: "/old/imports"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "imports")
	svc := SystemSettingsService{Store: st, ImportsDir: poolImports(dir)}
	view, err := svc.Save(ctx, SystemSettingsRequest{ImportDir: "/somewhere/else"})
	if err != nil || view.ImportDir != dir || !strings.Contains(view.Warning, dir) || !strings.Contains(view.Warning, "已忽略") {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	stored, err := st.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if err != nil || stored.ImportDir != "/old/imports" {
		t.Fatalf("库里的旧值被改了：%#v %v", stored, err)
	}
	if view, _ := svc.Save(ctx, SystemSettingsRequest{}); view.Warning != "" {
		t.Fatalf("没提交目录不该有提示：%q", view.Warning)
	}
}

// 旧目录：部署默认目录和库里保存过的目录，存在的才列出，与当前目录相同的不列。
func TestLegacyImportDirsListsExistingOldDirs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	envDir := filepath.Join(root, "var-imports")
	storedDir := filepath.Join(root, "data-imports")
	for _, d := range []string{envDir, storedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := newSettingsTestStore(t)
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, ImportDir: storedDir}); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "tank", "imports")
	svc := SystemSettingsService{Store: st, ImportsDir: poolImports(current), LegacyImportDir: envDir}
	got := svc.LegacyImportDirs(ctx)
	if len(got) != 2 || got[0] != envDir || got[1] != storedDir {
		t.Fatalf("旧目录 = %v", got)
	}
	// 库里存的目录在本节点不存在（属于别的池）：不列。
	if err := os.RemoveAll(storedDir); err != nil {
		t.Fatal(err)
	}
	if got := svc.LegacyImportDirs(ctx); len(got) != 1 || got[0] != envDir {
		t.Fatalf("不存在的目录不该列出：%v", got)
	}
	// 与当前目录相同的不算旧目录。
	svc.LegacyImportDir = current
	if got := svc.LegacyImportDirs(ctx); len(got) != 0 {
		t.Fatalf("当前目录不该算旧目录：%v", got)
	}
}

func newSettingsTestStore(t *testing.T) *store.SQLStore {
	t.Helper()
	st, err := store.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 复制限速：读取时按部署默认值解析三种状态；保存不能覆盖同一行里的其它设置。
func TestReplicationRateReadAndSavePreservesRow(t *testing.T) {
	ctx := context.Background()
	st := newSettingsTestStore(t)
	svc := SystemSettingsService{Store: st}

	// 从未设置：取默认值
	mbps, source, err := svc.ReplicationRate(ctx, 100)
	if err != nil || mbps != 100 || source != "default" {
		t.Fatalf("mbps=%d source=%q err=%v", mbps, source, err)
	}

	// 先写入其它设置（旧版本留下的导入目录列），再把限速设为 0（不限）
	dir := t.TempDir()
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, ImportDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveReplicationRate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	mbps, source, _ = svc.ReplicationRate(ctx, 100)
	if mbps != 0 || source != "setting" {
		t.Fatalf("explicit unlimited: mbps=%d source=%q", mbps, source)
	}
	stored, err := st.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if err != nil || stored.ImportDir != dir {
		t.Fatalf("import dir clobbered: %#v err=%v", stored, err)
	}

	if err := svc.SaveReplicationRate(ctx, -5); err == nil {
		t.Fatal("negative rate must be refused")
	}
}

// Windows 客户机策略默认开启；单独开关，不动其它设置。
func TestSystemSettingsWindowsPolicy(t *testing.T) {
	ctx := context.Background()
	st := newSettingsTestStore(t)
	svc := SystemSettingsService{Store: st}
	if view, err := svc.Get(ctx); err != nil || !view.EnforceWindowsPolicy {
		t.Fatalf("从未保存过应默认开启：%#v err=%v", view, err)
	}
	dir := t.TempDir()
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, ImportDir: dir}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.Get(ctx); !view.EnforceWindowsPolicy {
		t.Fatal("只有导入目录的行仍应开启")
	}
	if _, err := svc.SaveWindowsPolicy(ctx, WindowsPolicyRequest{Enforce: false}); err != nil {
		t.Fatal(err)
	}
	view, _ := svc.Get(ctx)
	stored, _ := st.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if view.EnforceWindowsPolicy || stored.ImportDir != dir {
		t.Fatalf("关闭后：%#v %#v", view, stored)
	}
	if _, err := svc.SaveWindowsPolicy(ctx, WindowsPolicyRequest{Enforce: true}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.Get(ctx); !view.EnforceWindowsPolicy {
		t.Fatal("重新开启没生效")
	}
}
