package local

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/iscsi"
)

var installID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var installMAC = regexp.MustCompile(`^[0-9A-F]{12}$`)

// installationLUNs 是安装 target 上可能有的盘：LUN 0 安装盘，LUN 1 只读安装介质。
var installationLUNs = []int{0, 1}

func validateInstallDisk(req storage.InstallationDiskReq) error {
	if !installID.MatchString(req.ID) || !installMAC.MatchString(storage.NormalizeMAC(req.MAC)) {
		return fmt.Errorf("安装会话或客户机 MAC 无效，请重新创建安装任务")
	}
	return nil
}

func (a *Agent) installationOffline(ctx context.Context, mac string) error {
	if a.exporter == nil {
		return storage.ErrNotImplemented
	}
	active, err := a.exporter.ActiveSessions(ctx)
	if err != nil {
		return err
	}
	for _, m := range active {
		if storage.NormalizeMAC(m) == storage.NormalizeMAC(mac) {
			return fmt.Errorf("请先关闭客户机 %s，等待安装盘连接断开后重试", mac)
		}
	}
	return nil
}

// PrepareInstallation re-exports the same disk after a reboot; it never formats it.
func (a *Agent) PrepareInstallation(ctx context.Context, req storage.InstallationDiskReq) (storage.LUNInfo, error) {
	if err := validateInstallDisk(req); err != nil {
		return storage.LUNInfo{}, err
	}
	if req.SizeBytes < 16<<30 {
		return storage.LUNInfo{}, fmt.Errorf("系统安装盘至少需要 16 GiB")
	}
	if req.MediaPath != "" {
		if fi, err := os.Stat(req.MediaPath); err != nil || !fi.Mode().IsRegular() {
			return storage.LUNInfo{}, fmt.Errorf("安装介质 %s 不在本节点，请放弃这次安装后重新开始", req.MediaPath)
		}
	}
	defer a.lockClient(req.MAC)()
	datasets, err := a.zfs.ListDatasets(ctx)
	if err != nil {
		return storage.LUNInfo{}, err
	}
	dataset := a.zfs.Dataset(storage.InstallationDiskName(req.MAC, req.ID))
	for _, d := range datasets {
		c := storage.Classify(datasetName(d))
		if c.MAC == storage.NormalizeMAC(req.MAC) && (c.Kind == storage.CarrierAdapt || c.Kind == storage.CarrierSuper || (c.Kind == storage.CarrierInstallation && d != dataset)) {
			return storage.LUNInfo{}, fmt.Errorf("客户机 %s 有其他安装、适配或超管磁盘，请先结束原任务", req.MAC)
		}
	}
	if !req.Booting {
		if err := a.installationOffline(ctx, req.MAC); err != nil {
			return storage.LUNInfo{}, err
		}
	}
	if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(req.MAC), installationLUNs); err != nil {
		return storage.LUNInfo{}, err
	}
	if !slices.Contains(datasets, dataset) {
		if err := a.zfs.CreateVolume(ctx, dataset, req.SizeBytes, defaultVolblockBytes); err != nil {
			return storage.LUNInfo{}, err
		}
	}
	export := iscsi.ExportRequest{MAC: req.MAC, VolPath: a.zfs.VolumePath(dataset), LUN: 0}
	if iscsi.CHAPEnabled(a.chapSecret) {
		c := iscsi.CHAPCredentials(a.chapSecret, req.MAC)
		export.CHAP = &c
	}
	exports := []iscsi.ExportRequest{export}
	if req.MediaPath != "" {
		out, err := a.runner.Run(ctx, "losetup", "--read-only", "--find", "--show", req.MediaPath)
		loop := strings.TrimSpace(string(out))
		if err != nil || !strings.HasPrefix(loop, "/dev/loop") {
			return storage.LUNInfo{}, fmt.Errorf("挂载安装介质 %s 失败：%v：%s", req.MediaPath, err, loop)
		}
		// LIO 打开后立即 detach：loop 转为自动回收，backstore 从任何路径被拆都不会留下它。
		defer a.runner.Run(context.Background(), "losetup", "--detach", loop)
		media := export
		media.VolPath, media.LUN, media.ReadOnly = loop, 1, true
		exports = append(exports, media)
	}
	luns, err := a.exporter.ExportBlocks(ctx, exports)
	if err != nil {
		return storage.LUNInfo{}, err
	}
	if len(luns) != len(exports) {
		return storage.LUNInfo{}, fmt.Errorf("安装盘导出未返回唯一系统盘，请检查存储节点")
	}
	return storage.LUNInfo{Server: a.server, Target: luns[0].Target, System: luns[0]}, nil
}

// CleanupInstallation only releases this attempt's work disk, never an ordinary clone.
func (a *Agent) CleanupInstallation(ctx context.Context, req storage.InstallationDiskReq) error {
	if err := validateInstallDisk(req); err != nil {
		return err
	}
	defer a.lockClient(req.MAC)()
	datasets, err := a.zfs.ListDatasets(ctx)
	if err != nil {
		return err
	}
	dataset := a.zfs.Dataset(storage.InstallationDiskName(req.MAC, req.ID))
	if !slices.Contains(datasets, dataset) {
		return nil
	}
	if err := a.installationOffline(ctx, req.MAC); err != nil {
		return err
	}
	if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(req.MAC), installationLUNs); err != nil {
		return err
	}
	return a.destroyClone(ctx, dataset)
}

// CleanupInstallationCandidate leaves ordinary/data clones and other attempts untouched.
func (a *Agent) CleanupInstallationCandidate(ctx context.Context, req storage.InstallationCleanupReq) error {
	if err := validateInstallDisk(storage.InstallationDiskReq{ID: req.ID, MAC: req.MAC}); err != nil {
		return err
	}
	if req.ImageID == "" {
		return nil
	}
	if !strings.HasPrefix(req.ID, "install-") || req.ImageID != "iso_"+strings.TrimPrefix(req.ID, "install-") || !installationImageID(req.ImageID) {
		return fmt.Errorf("候选镜像不属于本次安装，请检查安装任务")
	}
	defer a.lockClient(req.MAC)()
	top, err := readTopology(ctx, a.zfs)
	if err != nil {
		return err
	}
	configID := storage.DefaultConfigName(req.ImageID)
	image, config := a.zfs.Dataset(req.ImageID), a.zfs.Dataset(configID)
	if !top.has(image) && !top.has(config) {
		return nil
	}
	if err := a.installationOffline(ctx, req.MAC); err != nil {
		return err
	}
	origins, err := a.zfs.ListOrigins(ctx)
	if err != nil {
		return err
	}
	clientID := storage.ClientCloneName(req.MAC)
	client := a.zfs.Dataset(clientID)
	ids := []string{configID, req.ImageID}
	clientParent := strings.SplitN(origins[client], "@", 2)[0]
	verification := clientParent == image || clientParent == config
	for dataset, origin := range origins {
		parent := strings.SplitN(origin, "@", 2)[0]
		if parent != image && parent != config && !(verification && parent == client) {
			continue
		}
		if dataset == image || dataset == config {
			continue
		}
		if dataset != client || parent == client {
			return fmt.Errorf("安装候选镜像仍被 %s 引用，请先检查该磁盘", dataset)
		}
	}
	if verification {
		if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(req.MAC), []int{0}); err != nil {
			return err
		}
		ids = append([]string{clientID}, ids...)
	}
	return a.destroyInDependencyOrder(ctx, top, ids, "")
}

func installationImageID(id string) bool {
	return installID.MatchString(id) && storage.Classify(id).Kind == storage.CarrierEntity && !storage.ReservedCatalogueChild(id) && !strings.Contains(id, "@")
}

// SaveInstallation advances an interrupted save without deleting the installed disk.
// The returned candidate is intentionally unavailable to ordinary groups until verified.
func (a *Agent) SaveInstallation(ctx context.Context, req storage.InstallationSaveReq) (storage.ImportImageResult, error) {
	if err := validateInstallDisk(storage.InstallationDiskReq{ID: req.ID, MAC: req.MAC}); err != nil {
		return storage.ImportImageResult{}, err
	}
	if !installationImageID(req.ImageID) || strings.TrimSpace(req.Name) == "" || (req.OSType != domain.OSTypeWindows && req.OSType != domain.OSTypeLinux) {
		return storage.ImportImageResult{}, fmt.Errorf("镜像名称或系统类型无效，请检查安装任务")
	}
	defer a.lockClient(req.MAC)()
	if err := a.installationOffline(ctx, req.MAC); err != nil {
		return storage.ImportImageResult{}, err
	}
	datasets, err := a.zfs.ListDatasets(ctx)
	if err != nil {
		return storage.ImportImageResult{}, err
	}
	work := a.zfs.Dataset(storage.InstallationDiskName(req.MAC, req.ID))
	image := a.zfs.Dataset(req.ImageID)
	configID := storage.DefaultConfigName(req.ImageID)
	config := a.zfs.Dataset(configID)
	if slices.Contains(datasets, work) && slices.Contains(datasets, image) {
		return storage.ImportImageResult{}, fmt.Errorf("候选镜像 %s 已存在，请保留安装盘并检查任务", req.ImageID)
	}
	if !slices.Contains(datasets, work) && !slices.Contains(datasets, image) {
		return storage.ImportImageResult{}, fmt.Errorf("客户机 %s 的安装盘未找到，请检查存储池；不会重新创建或格式化", req.MAC)
	}
	if slices.Contains(datasets, work) {
		if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(req.MAC), installationLUNs); err != nil {
			return storage.ImportImageResult{}, err
		}
		if err := a.zfs.Rename(ctx, work, image); err != nil {
			return storage.ImportImageResult{}, err
		}
	}
	snapshots, err := a.zfs.ListSnapshots(ctx)
	if err != nil {
		return storage.ImportImageResult{}, err
	}
	if !slices.Contains(snapshots, image+"@0") {
		if req.OSType == domain.OSTypeWindows && len(req.MountScript) > 0 {
			if err := a.injectMountScript(ctx, a.zfs.VolumePath(image), req.MountScript); err != nil {
				return storage.ImportImageResult{}, err
			}
		}
		if err := a.zfs.SnapshotVolume(ctx, image, "0"); err != nil {
			return storage.ImportImageResult{}, err
		}
	}
	if !slices.Contains(datasets, config) {
		if err := a.zfs.Clone(ctx, image+"@0", config); err != nil {
			return storage.ImportImageResult{}, err
		}
	}
	if !slices.Contains(snapshots, config+"@0") {
		if err := a.zfs.SnapshotVolume(ctx, config, "0"); err != nil {
			return storage.ImportImageResult{}, err
		}
	}
	now := time.Now().UTC()
	reductionID := storage.ReductionID(configID, "0")
	return storage.ImportImageResult{
		Image:               domain.Image{ID: req.ImageID, Name: req.Name, OSType: req.OSType, Purpose: domain.ImagePurposeSystem, Origin: domain.ImageOriginImported, State: domain.ImageState("installing"), Size: req.SizeBytes, CreatedAt: now},
		Config:              domain.Config{ID: configID, ImageID: req.ImageID, Name: "default", DefaultReductionID: &reductionID, CreatedAt: now},
		Reduction:           domain.Reduction{ID: reductionID, ConfigID: configID, Name: "@0", Status: domain.ReductionStatus("creating"), CreatedAt: now},
		MountScriptInjected: req.OSType == domain.OSTypeWindows && len(req.MountScript) > 0,
	}, nil
}

var _ storage.InstallationHostAgent = (*Agent)(nil)
