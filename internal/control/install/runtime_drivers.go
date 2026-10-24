package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/driverinf"
)

// 上传的驱动包和 ISO 自带的驱动都带进安装环境，上传的排在前面先加载：
// 自带驱动多半只有系统级签名，WinRE 里可能加载不了。
func (r Runtime) prepareWindowsDrivers(ctx context.Context, s domain.InstallSession, dir, wim string) (string, error) {
	dest := filepath.Join(dir, "drivers")
	hwid := strings.ToUpper(strings.TrimSpace(s.HWID))
	if err := os.MkdirAll(dest, 0700); err != nil {
		return "", err
	}
	if err := stageImageDrivers(ctx, s, dir, filepath.Join(dest, "windows"), wim, hwid); err != nil {
		return "", err
	}
	if err := r.stageUploadedDrivers(ctx, dir, filepath.Join(dest, "uploaded"), hwid); err != nil {
		return "", err
	}
	return dest, nil
}

// ISO 自带驱动按 ISO、版本、网卡缓存：扫一遍驱动库要一两秒，开机请求里不宜每次都扫。
func stageImageDrivers(ctx context.Context, s domain.InstallSession, dir, dest, wim, hwid string) error {
	marker := filepath.Join(dir, "driver-hwid")
	key := fmt.Sprintf("iso-driver-v3\n%s\n%d\n%s", s.SourceSHA256, s.EditionIndex, hwid)
	prior, _ := os.ReadFile(marker)
	if string(prior) == key && completeDriverDirectory(dest) {
		return nil
	}
	stage, err := os.MkdirTemp(dir, ".drivers-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	matched, err := stageWIMNetworkDrivers(ctx, wim, s.EditionIndex, hwid, stage)
	if err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if !matched {
		return nil
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	return writePrivate(marker, []byte(key))
}

// 上传的驱动包每次重新取：运维随时可能上传、替换或删除。
func (r Runtime) stageUploadedDrivers(ctx context.Context, dir, dest, hwid string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if r.PE.Store == nil {
		return nil
	}
	stage, err := os.MkdirTemp(dir, ".drivers-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if _, err := r.PE.StageUploadedDriver(ctx, hwid, stage); err != nil {
		return err
	}
	if !completeDriverDirectory(stage) {
		return nil
	}
	return os.Rename(stage, dest)
}

func completeDriverDirectory(dir string) bool {
	inf, sys := false, false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".inf":
			inf = true
		case ".sys":
			sys = true
		}
		return nil
	})
	return err == nil && inf && sys
}

// Read all INF metadata in one WIM operation, then extract only matching x64 NIC packages.
func stageWIMNetworkDrivers(ctx context.Context, wim string, index int, hwid, dest string) (bool, error) {
	metadata, err := os.MkdirTemp(filepath.Dir(dest), ".driver-inf-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(metadata)
	if err := extractWIMDriverPaths(ctx, wim, index, metadata, []string{"/Windows/System32/DriverStore/FileRepository/*/*.inf"}); err != nil {
		return false, err
	}
	packages := map[string]bool{}
	err = filepath.WalkDir(metadata, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".inf") {
			return nil
		}
		if !strings.Contains(strings.ToLower(filepath.Base(filepath.Dir(path))), "_amd64_") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inf, err := driverinf.Parse(raw)
		if err != nil || !strings.EqualFold(inf.ClassName, "Net") {
			return nil
		}
		for _, id := range inf.HWIDs {
			id = strings.ToUpper(id)
			if id == hwid || strings.HasPrefix(id, hwid+"&") {
				rel, err := filepath.Rel(metadata, filepath.Dir(path))
				if err != nil {
					return err
				}
				packages["/"+filepath.ToSlash(rel)] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("读取 Windows ISO 网卡驱动元数据失败，请检查 ISO 和服务器存储：%w", err)
	}
	if len(packages) == 0 {
		return false, nil
	}
	var paths []string
	for path := range packages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	raw, err := os.MkdirTemp(filepath.Dir(dest), ".driver-inf-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(raw)
	if err := extractWIMDriverPaths(ctx, wim, index, raw, paths); err != nil {
		return false, err
	}
	for _, path := range paths {
		if err := os.Rename(filepath.Join(raw, filepath.FromSlash(path)), filepath.Join(dest, filepath.Base(path))); err != nil {
			return false, fmt.Errorf("整理 Windows ISO 网卡驱动包失败：%w", err)
		}
	}
	if !completeDriverDirectory(dest) {
		return false, fmt.Errorf("Windows ISO 中匹配网卡「%s」的驱动包缺少 INF 或 SYS，请检查 ISO 完整性并重新选择完整的安装文件", hwid)
	}
	err = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.ContainsAny(rel, "\r\n|\"") {
			return fmt.Errorf("Windows ISO 网卡驱动包文件路径无效：%s", rel)
		}
		return nil
	})
	return err == nil, err
}

func extractWIMDriverPaths(ctx context.Context, wim string, index int, dest string, paths []string) error {
	args := append([]string{"extract", wim, strconv.Itoa(index)}, paths...)
	args = append(args, "--dest-dir="+dest, "--preserve-dir-structure", "--no-acls", "--no-attributes", "--nullglob")
	cmd := exec.CommandContext(ctx, "wimlib-imagex", args...)
	cmd.Env = append(os.Environ(), "WIMLIB_IMAGEX_IGNORE_CASE=1")
	var stderr wimStderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("请在镜像所在服务器安装 wimtools 后重试：找不到 wimlib-imagex")
		}
		return fmt.Errorf("读取 Windows ISO 第 %d 个版本的网卡驱动失败，请检查 ISO 完整性和服务器存储后重试：%w；%s", index, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// driverNote 是带进安装环境、列有启动网卡的一份 INF；网卡没装上时，安装环境据此说清是不是系统版本不支持。
type driverNote struct {
	INF      string `json:"inf"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	MinBuild int    `json:"minbuild"`
}

func driverNotes(dir, hwid string) ([]driverNote, error) {
	notes := []driverNote{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".inf") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		build, listed, err := driverinf.MinBuild(raw, hwid)
		if err != nil || !listed {
			return nil
		}
		inf, _ := driverinf.Parse(raw)
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		source, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		notes = append(notes, driverNote{INF: strings.ReplaceAll(filepath.ToSlash(rel), "/", `\`), Version: inf.Version, Source: source, MinBuild: build})
		return nil
	})
	return notes, err
}
