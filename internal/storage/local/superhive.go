package local

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/winreg"
)

// superHiveFn 在超管机系统盘存成还原点之前把它的 SYSTEM 注册表恢复到已提交状态；测试替换它。
var superHiveFn = recoverSuperHive

// prepareSuperHive 只在全新保存时检查：替换开始后（配置已挪开）超管机克隆已不在原名下，且不能半途拒绝。
func (a *Agent) prepareSuperHive(ctx context.Context, top *topology, mac, configID string) error {
	superDataset := a.zfs.Dataset(storage.SuperClientCloneName(mac))
	oldDataset := a.zfs.Dataset(storage.SupersededConfigName(strings.TrimSpace(configID)))
	if !top.has(superDataset) || top.has(oldDataset) {
		return nil
	}
	return superHiveFn(ctx, a, superDataset)
}

// recoverSuperHive 先只读挂载查看：hiberfil.sys 头为 HIBR/WAKE（快速启动、休眠）则拒绝；SYSTEM 干净则不写盘。
// 只有 SYSTEM 未提交时才读写挂载回放事务日志，失败返回 SuperHiveUnsafe，hive 原样不动。
// 没有 Windows 分区（Linux 镜像）不检查。
func recoverSuperHive(ctx context.Context, a *Agent, dataset string) error {
	r := a.runner
	if r == nil {
		r = execRunner{}
	}
	dev := a.zfs.VolumePath(dataset)
	dirty := false
	err := withWindowsPartition(ctx, r, dev, volRO, clonePartitionWait, func(root string) error {
		if hibernated(root) {
			return storage.SuperHiveUnsafe{FastStartup: true}
		}
		b, err := os.ReadFile(systemHive(root))
		if err != nil {
			return err
		}
		dirty, err = winreg.HiveDirty(b)
		if err != nil {
			return storage.SuperHiveUnsafe{Reason: err.Error()}
		}
		return nil
	})
	if errors.Is(err, errNoWindowsPartition) {
		return nil
	}
	if err != nil || !dirty {
		return err
	}
	return withWindowsPartition(ctx, r, dev, volRW, clonePartitionWait, func(root string) error {
		res, err := winreg.RecoverHive(ctx, systemHive(root))
		if err != nil {
			return storage.SuperHiveUnsafe{Reason: err.Error()}
		}
		slog.Info("super save: replayed SYSTEM transaction logs", "dataset", dataset, "format", res.Format, "entries", res.Applied, "seq", res.Seq)
		return nil
	})
}

func systemHive(root string) string {
	if hive, ok := lookupPath(root, "Windows/System32/config/SYSTEM"); ok {
		return hive
	}
	return filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
}

// hibernated 报告卷根的 hiberfil.sys 是否带有效休眠头（HIBR/WAKE，大小写不限）；
// 完全关机或恢复后 Windows 会清掉这个头。
func hibernated(root string) bool {
	path, ok := lookupPath(root, "hiberfil.sys")
	if !ok {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	switch strings.ToUpper(string(head)) {
	case "HIBR", "WAKE":
		return true
	}
	return false
}
