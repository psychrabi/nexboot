package assets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
)

func newLegacy(t *testing.T) (*LegacyImports, string, string) {
	t.Helper()
	old, imports := t.TempDir(), t.TempDir()
	return &LegacyImports{
		Dirs:      func(context.Context) []string { return []string{old} },
		ImportDir: func(context.Context) (string, error) { return imports, nil },
	}, old, imports
}

func waitMoved(t *testing.T, l *LegacyImports, path string) LegacyImportFile {
	t.Helper()
	for i := 0; i < 200; i++ {
		l.mu.Lock()
		m := l.moves[path]
		l.mu.Unlock()
		if m == nil || m.done {
			res, _ := l.List(context.Background())
			for _, it := range res.Items {
				if it.Path == path {
					return it
				}
			}
			return LegacyImportFile{}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("移入没结束")
	return LegacyImportFile{}
}

// 旧目录只列可导入的文件，只读；移入后源文件消失、导入目录里内容一致。
func TestLegacyImportsListsAndMovesIntoImports(t *testing.T) {
	ctx := context.Background()
	l, old, imports := newLegacy(t)
	_ = os.WriteFile(filepath.Join(old, "win10.iso"), []byte("iso-bytes"), 0o644)
	_ = os.WriteFile(filepath.Join(old, "notes.txt"), []byte("x"), 0o644)
	res, err := l.List(ctx)
	if err != nil || len(res.Items) != 1 || res.Items[0].Name != "win10.iso" || res.ImportDir != imports || len(res.Dirs) != 1 {
		t.Fatalf("列表不对：%#v %v", res, err)
	}
	src := filepath.Join(old, "win10.iso")
	if it, err := l.Move(ctx, MoveLegacyImportRequest{Path: src}); err != nil || !it.Moving {
		t.Fatalf("移入没开始：%#v %v", it, err)
	}
	if it := waitMoved(t, l, src); it.Path != "" {
		t.Fatalf("移入后旧目录不该再列出它：%#v", it)
	}
	if b, _ := os.ReadFile(filepath.Join(imports, "win10.iso")); string(b) != "iso-bytes" {
		t.Fatalf("导入目录里内容不对：%q", b)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("源文件应已移走：%v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(imports, uploadsSubdir)); len(ents) != 0 {
		t.Fatalf("不该留下中间文件：%v", ents)
	}
}

// 只接受旧目录里直接放着的镜像；同名不覆盖；空间不足当场拒绝且源文件不动。
func TestLegacyImportsMoveRefusals(t *testing.T) {
	ctx := context.Background()
	l, old, imports := newLegacy(t)
	src := filepath.Join(old, "a.iso")
	_ = os.WriteFile(src, []byte("new"), 0o644)
	for _, p := range []string{"/etc/passwd", filepath.Join(old, "sub", "a.iso"), filepath.Join(old, "../x.iso")} {
		if _, err := l.Move(ctx, MoveLegacyImportRequest{Path: p}); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("%s 应拒绝：%v", p, err)
		}
	}
	_ = os.WriteFile(filepath.Join(imports, "a.iso"), []byte("keep"), 0o644)
	if _, err := l.Move(ctx, MoveLegacyImportRequest{Path: src}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("同名应拒绝：%v", err)
	}
	_ = os.Remove(filepath.Join(imports, "a.iso"))
	l.Uploads = &UploadService{FreeSpace: func(string) (int64, error) { return 10, nil }}
	if _, err := l.Move(ctx, MoveLegacyImportRequest{Path: src}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("空间不足应拒绝：%v", err)
	}
	if b, _ := os.ReadFile(src); string(b) != "new" {
		t.Fatal("被拒时源文件必须完好")
	}
	l.ImportDir = func(context.Context) (string, error) { return "", errs.Invalid("请先创建存储池") }
	l.Uploads = nil
	if _, err := l.Move(ctx, MoveLegacyImportRequest{Path: src}); err == nil {
		t.Fatal("没有存储池时应拒绝")
	}
}

// 跨盘移入走复制：模拟时让第一次硬链接失败不了，只能直接测复制路径。
func TestMoveIntoImportsCopyPathKeepsSourceUntilDone(t *testing.T) {
	old, imports := t.TempDir(), t.TempDir()
	src := filepath.Join(old, "b.iso")
	_ = os.WriteFile(src, []byte("payload"), 0o644)
	tmp := filepath.Join(imports, uploadsSubdir)
	_ = os.MkdirAll(tmp, 0o755)
	if err := copyFileSync(src, filepath.Join(tmp, "b.iso.moving")); err != nil {
		t.Fatal(err)
	}
	if err := copyFileSync(src, filepath.Join(tmp, "b.iso.moving")); err == nil {
		t.Fatal("中间文件已存在时不该覆盖")
	}
	if b, _ := os.ReadFile(src); string(b) != "payload" {
		t.Fatal("复制不应动源文件")
	}
}

// 中途停机留下的 .moving 由每小时的清理收走，正在写的不动。
func TestReapRemovesStaleMovingCopies(t *testing.T) {
	ctx := context.Background()
	svc, dir := newUploads(t, "nodeA")
	parts := filepath.Join(dir, uploadsSubdir)
	_ = os.MkdirAll(parts, 0o755)
	stale, fresh := filepath.Join(parts, "x.iso.moving"), filepath.Join(parts, "y.iso.moving")
	_ = os.WriteFile(stale, []byte("x"), 0o644)
	_ = os.WriteFile(fresh, []byte("y"), 0o644)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	if n := svc.Reap(ctx, 24*time.Hour); n != 1 {
		t.Fatalf("应收走 1 个：%d", n)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新的 .moving 不该删")
	}
}
