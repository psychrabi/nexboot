package assets

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tianwei/diskless/internal/control/errs"
)

// LegacyImports 列出旧版本导入目录（部署默认目录、库里保存过的目录）里留下的文件，只读；
// 由操作者逐个「移入导入目录」，启动时不自动搬：这些文件可能很大，也可能正被人用着。
type LegacyImports struct {
	// Dirs 返回本机仍存在的旧目录，不含当前导入目录。
	Dirs func(ctx context.Context) []string
	// ImportDir 返回当前导入目录（存储池上的 imports）。
	ImportDir func(ctx context.Context) (string, error)
	// Uploads 用来按上传同样的规则查空间（本机池 + 最小的备机池）；为 nil 时不查。
	Uploads *UploadService

	mu    sync.Mutex
	moves map[string]*legacyMove
}

type legacyMove struct {
	err  string
	done bool
}

// LegacyImportFile 是旧目录里的一个可导入文件。Moving 表示正在移入；Error 是上次移入失败的原因。
type LegacyImportFile struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Dir    string `json:"dir"`
	Size   int64  `json:"size"`
	Moving bool   `json:"moving"`
	Error  string `json:"error,omitempty"`
}

type LegacyImportList struct {
	ImportDir string             `json:"import_dir"`
	Dirs      []string           `json:"dirs"`
	Items     []LegacyImportFile `json:"items"`
}

type MoveLegacyImportRequest struct {
	Path string `json:"path"`
}

func legacyImportable(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".iso") || isSupportedImportImageFile(lower)
}

func (l *LegacyImports) dirs(ctx context.Context) []string {
	if l.Dirs == nil {
		return nil
	}
	return l.Dirs(ctx)
}

func (l *LegacyImports) List(ctx context.Context) (LegacyImportList, error) {
	res := LegacyImportList{Dirs: []string{}, Items: []LegacyImportFile{}}
	if l.ImportDir != nil {
		res.ImportDir, _ = l.ImportDir(ctx)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, dir := range l.dirs(ctx) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		res.Dirs = append(res.Dirs, dir)
		for _, e := range entries {
			if !e.Type().IsRegular() || !legacyImportable(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			item := LegacyImportFile{Name: e.Name(), Path: filepath.Join(dir, e.Name()), Dir: dir, Size: info.Size()}
			if m := l.moves[item.Path]; m != nil {
				item.Moving, item.Error = !m.done, m.err
			}
			res.Items = append(res.Items, item)
		}
	}
	sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].Path < res.Items[j].Path })
	return res, nil
}

// Move 把旧目录里的一个文件移入导入目录，后台进行（跨盘时要复制几 GB），立即返回。
// 只接受旧目录里直接放着的可导入文件；导入目录已有同名文件时拒绝，绝不覆盖。
func (l *LegacyImports) Move(ctx context.Context, req MoveLegacyImportRequest) (LegacyImportFile, error) {
	src := filepath.Clean(strings.TrimSpace(req.Path))
	var dir string
	for _, d := range l.dirs(ctx) {
		if filepath.Dir(src) == filepath.Clean(d) {
			dir = d
		}
	}
	if dir == "" || !legacyImportable(src) {
		return LegacyImportFile{}, errs.Invalid("只能移入旧导入目录里的镜像文件：" + req.Path)
	}
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return LegacyImportFile{}, errs.NotFound("文件不存在：" + src)
	}
	if l.ImportDir == nil {
		return LegacyImportFile{}, errs.Invalid("请先创建存储池：上传和导入的文件存放在存储池的 imports 目录")
	}
	dest, err := l.ImportDir(ctx)
	if err != nil {
		return LegacyImportFile{}, err
	}
	name := filepath.Base(src)
	if _, err := os.Lstat(filepath.Join(dest, name)); err == nil {
		return LegacyImportFile{}, errs.Conflict(fmt.Sprintf("导入目录 %s 里已有同名文件 %s，请先改名或删除其中一个", dest, name))
	}
	if l.Uploads != nil {
		if err := l.Uploads.checkSpace(ctx, dest, info.Size()); err != nil {
			return LegacyImportFile{}, err
		}
	}
	l.mu.Lock()
	if l.moves == nil {
		l.moves = map[string]*legacyMove{}
	}
	if m := l.moves[src]; m != nil && !m.done {
		l.mu.Unlock()
		return LegacyImportFile{}, errs.Conflict("这个文件正在移入导入目录：" + src)
	}
	m := &legacyMove{}
	l.moves[src] = m
	l.mu.Unlock()
	go func() {
		err := moveIntoImports(src, dest, name)
		l.mu.Lock()
		defer l.mu.Unlock()
		m.done = true
		if err != nil {
			m.err = err.Error()
		} else {
			delete(l.moves, src)
		}
	}()
	return LegacyImportFile{Name: name, Path: src, Dir: dir, Size: info.Size(), Moving: true}, nil
}

// moveIntoImports 同盘时直接改名；跨盘先复制到导入目录的 .uploads 下（不被列出、同一文件系统），
// 写完 fsync，再硬链接到最终名（不覆盖），最后才删源文件：中途失败时源文件始终完好。
func moveIntoImports(src, dest, name string) error {
	final := filepath.Join(dest, name)
	if err := os.Link(src, final); err == nil {
		return os.Remove(src)
	} else if os.IsExist(err) {
		return fmt.Errorf("导入目录里已有同名文件 %s", name)
	}
	staging := filepath.Join(dest, uploadsSubdir)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(staging, name+".moving")
	if err := copyFileSync(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Link(tmp, final); err != nil {
		_ = os.Remove(tmp)
		if os.IsExist(err) {
			return fmt.Errorf("导入目录里已有同名文件 %s", name)
		}
		return err
	}
	_ = os.Remove(tmp)
	return os.Remove(src)
}

func copyFileSync(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return nil
}
