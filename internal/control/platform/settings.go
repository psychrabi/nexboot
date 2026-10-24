package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

type SystemSettingsService struct {
	Store store.Store
	// ImportsDir 解析本节点存储池上的导入目录（<pool>/nd/files/imports）。目录随目录库复制到备机，
	// 主备切换后新主机上同一批文件仍在；没有存储池时返回错误。
	ImportsDir func(ctx context.Context) (string, error)
	// LegacyImportDir 是旧版本的部署导入目录（NDISKLESS_IMPORT_DIR）。只作为旧文件来源列出，不再往里写。
	LegacyImportDir string
}

// SystemSettingsRequest 保留 import_dir 只为兼容旧控制台：导入目录不再可设，收到会被忽略并给出提示。
type SystemSettingsRequest struct {
	ImportDir string `json:"import_dir"`
}

type SystemSettingsView struct {
	// ImportDir 是本节点实际使用的导入目录（存储池上），只读；没有存储池时为空，原因见 ImportDirError。
	ImportDir      string `json:"import_dir"`
	ImportDirError string `json:"import_dir_error,omitempty"`
	// LegacyImportDirs 是旧版本留下、仍存在的导入目录，里面的文件可移入新目录。
	LegacyImportDirs []string `json:"legacy_import_dirs,omitempty"`
	// Warning 说明提交里被忽略的部分（如旧控制台提交的 import_dir）。
	Warning string `json:"warning,omitempty"`
	// EnforceWindowsPolicy 为真时，Windows 客户机开机关闭自动更新、自动重启、睡眠和休眠。
	EnforceWindowsPolicy bool `json:"enforce_windows_policy"`
}

// WindowsPolicyRequest 打开或关闭 Windows 客户机的开机策略。
type WindowsPolicyRequest struct {
	Enforce bool `json:"enforce"`
}

// noPoolForImports：上传和导入的文件放在存储池上，没有池就无处可放。
const noPoolForImports = "请先创建存储池：上传和导入的文件存放在存储池的 imports 目录"

func (s SystemSettingsService) Get(ctx context.Context) (SystemSettingsView, error) {
	view := SystemSettingsView{EnforceWindowsPolicy: true}
	var stored domain.SystemSettings
	if s.Store != nil {
		settings, err := s.Store.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
		if err != nil && !errs.IsNotFound(err) {
			return SystemSettingsView{}, err
		}
		if err == nil {
			stored = settings
			view.EnforceWindowsPolicy = !settings.KeepWindowsDefaults
		}
	}
	if dir, err := s.ImportDir(ctx); err != nil {
		view.ImportDirError = err.Error()
	} else {
		view.ImportDir = dir
	}
	view.LegacyImportDirs = s.legacyDirs(stored.ImportDir, view.ImportDir)
	return view, nil
}

// SaveWindowsPolicy 只改 Windows 客户机开机策略，其余设置保留。
func (s SystemSettingsService) SaveWindowsPolicy(ctx context.Context, req WindowsPolicyRequest) (SystemSettingsView, error) {
	settings, err := s.Store.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if err != nil && !errs.IsNotFound(err) {
		return SystemSettingsView{}, err
	}
	settings.ID = domain.SystemSettingsDefaultID
	settings.KeepWindowsDefaults = !req.Enforce
	if err := s.Store.SystemSettings().Upsert(ctx, settings); err != nil {
		return SystemSettingsView{}, err
	}
	return s.Get(ctx)
}

// Save 不再保存导入目录：目录固定在各节点存储池上。旧控制台或脚本提交的 import_dir 被忽略，
// 应答里说明实际目录；库里旧值原样保留（只用来列出旧文件）。
func (s SystemSettingsService) Save(ctx context.Context, req SystemSettingsRequest) (SystemSettingsView, error) {
	view, err := s.Get(ctx)
	if err != nil {
		return SystemSettingsView{}, err
	}
	if strings.TrimSpace(req.ImportDir) != "" {
		where := view.ImportDir
		if where == "" {
			where = "存储池上的 imports 目录"
		}
		view.Warning = "镜像导入目录已改为自动使用 " + where + "，不再支持设置；提交的目录已忽略"
	}
	return view, nil
}

// ImportDir 返回本节点存储池上的导入目录，不存在就建出来。没有存储池时报错，不退回系统盘目录：
// 退回会让上传悄悄落到另一块小得多的盘上，界面显示的又是别的路径。
func (s SystemSettingsService) ImportDir(ctx context.Context) (string, error) {
	if s.ImportsDir == nil {
		return "", errs.Invalid(noPoolForImports)
	}
	dir, err := s.ImportsDir(ctx)
	if err != nil {
		return "", errs.Invalid(fmt.Sprintf("%s（%v）", noPoolForImports, err))
	}
	if strings.TrimSpace(dir) == "" {
		return "", errs.Invalid(noPoolForImports)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// LegacyImportDirs 列出旧版本的导入目录（部署默认目录、库里保存过的目录）中仍存在的，供移入新目录。
func (s SystemSettingsService) LegacyImportDirs(ctx context.Context) []string {
	view, err := s.Get(ctx)
	if err != nil {
		return nil
	}
	return view.LegacyImportDirs
}

func (s SystemSettingsService) legacyDirs(stored, current string) []string {
	var out []string
	seen := map[string]bool{}
	if current != "" {
		seen[filepath.Clean(current)] = true
	}
	for _, raw := range []string{s.LegacyImportDir, stored} {
		dir, err := normalizeImportDir(raw)
		if err != nil || seen[dir] {
			continue
		}
		seen[dir] = true
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

func normalizeImportDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errs.Invalid("镜像目录不能为空")
	}
	dir := filepath.Clean(raw)
	if !filepath.IsAbs(dir) {
		return "", errs.Invalid("镜像目录必须是绝对路径")
	}
	if dir == string(filepath.Separator) {
		return "", errs.Invalid("镜像目录不能是根目录")
	}
	for _, blocked := range []string{"/dev", "/proc", "/run", "/sys"} {
		if dir == blocked || strings.HasPrefix(dir, blocked+"/") {
			return "", errs.Invalid("镜像目录不能位于 " + blocked)
		}
	}
	return dir, nil
}

// ReplicationRate 返回生效的批量传输限速：操作者设过则用设定值（0 表示明确不限速，
// 如无客户机的维护窗口），否则用部署默认值。source 为 "setting" 或 "default"。
func (s SystemSettingsService) ReplicationRate(ctx context.Context, defaultMBPS int) (mbps int, source string, err error) {
	if s.Store == nil {
		return defaultMBPS, "default", nil
	}
	settings, err := s.Store.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if errs.IsNotFound(err) {
		return defaultMBPS, "default", nil
	}
	if err != nil {
		return 0, "", err
	}
	if settings.ReplicationRateMBPS == nil {
		return defaultMBPS, "default", nil
	}
	return *settings.ReplicationRateMBPS, "setting", nil
}

// SaveReplicationRate 保存限速（0 表示不限），共享行里的其它设置保持不变。
func (s SystemSettingsService) SaveReplicationRate(ctx context.Context, mbps int) error {
	if mbps < 0 {
		return errs.Invalid("同步限速必须 ≥ 0（0 表示不限速）")
	}
	settings, err := s.Store.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if err != nil && !errs.IsNotFound(err) {
		return err
	}
	settings.ID = domain.SystemSettingsDefaultID
	settings.ReplicationRateMBPS = &mbps
	return s.Store.SystemSettings().Upsert(ctx, settings)
}

// SaveDataPool 记录本节点存放镜像/克隆的 ZFS 池，共享行里的其它设置保持不变。
// 界面建池后调用，使选择重启后仍有效；池名不是写死的 "tank"。
func (s SystemSettingsService) SaveDataPool(ctx context.Context, pool string) error {
	settings, err := s.Store.SystemSettings().Get(ctx, domain.SystemSettingsDefaultID)
	if err != nil && !errs.IsNotFound(err) {
		return err
	}
	settings.ID = domain.SystemSettingsDefaultID
	settings.DataPool = strings.TrimSpace(pool)
	return s.Store.SystemSettings().Upsert(ctx, settings)
}
