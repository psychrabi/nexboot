package local

import (
	"context"
	"fmt"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/iscsi"
	"slices"
)

func (a *Agent) CreateModelAdaptation(ctx context.Context, req storage.ModelAdaptReq) (storage.LUNInfo, error) {
	unlock := a.lockClient(req.MAC)
	defer unlock()
	if a.exporter == nil {
		return storage.LUNInfo{}, storage.ErrNotImplemented
	}
	datasets, err := a.zfs.ListDatasets(ctx)
	if err != nil {
		return storage.LUNInfo{}, err
	}
	for _, dataset := range datasets {
		c := storage.Classify(datasetName(dataset))
		if c.MAC == storage.NormalizeMAC(req.MAC) && (c.Kind == storage.CarrierInstallation || c.Kind == storage.CarrierSuper) {
			return storage.LUNInfo{}, fmt.Errorf("客户机 %s 有其他安装或超管磁盘，请先结束原任务后再适配", req.MAC)
		}
	}
	if err := a.installationOffline(ctx, req.MAC); err != nil {
		return storage.LUNInfo{}, err
	}
	if err := a.cleanupModelAdaptation(ctx, req.MAC); err != nil {
		return storage.LUNInfo{}, err
	}
	name := a.zfs.Dataset(storage.ModelAdaptCloneName(req.MAC))
	if err := a.zfs.Clone(ctx, a.zfs.Snapshot(req.System.ConfigID, req.System.SnapshotName), name); err != nil {
		return storage.LUNInfo{}, err
	}
	if req.DriverDir != "" {
		if err := a.prepareModelVolume(ctx, name, req.HWID, req.DriverDir); err != nil {
			_ = a.cleanupModelAdaptation(ctx, req.MAC)
			return storage.LUNInfo{}, err
		}
	}
	export := iscsi.ExportRequest{MAC: req.MAC, VolPath: a.zfs.VolumePath(name), LUN: 0}
	if iscsi.CHAPEnabled(a.chapSecret) {
		c := iscsi.CHAPCredentials(a.chapSecret, req.MAC)
		export.CHAP = &c
	}
	luns, err := a.exporter.ExportBlocks(ctx, []iscsi.ExportRequest{export})
	if err != nil {
		_ = a.cleanupModelAdaptation(ctx, req.MAC)
		return storage.LUNInfo{}, err
	}
	return storage.LUNInfo{Server: a.server, Target: luns[0].Target, System: luns[0]}, nil
}
func (a *Agent) SaveModelAdaptation(ctx context.Context, req storage.SuperStopReq) (domain.Reduction, error) {
	unlock := a.lockClient(req.MAC)
	defer unlock()
	if a.exporter != nil {
		if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(req.MAC), []int{0}); err != nil {
			return domain.Reduction{}, err
		}
	}
	top, err := readTopology(ctx, a.zfs)
	if err != nil {
		return domain.Reduction{}, err
	}
	return a.saveSuperClone(ctx, top, 0, storage.ModelAdaptCloneName(req.MAC), req.ConfigID, req.ReductionName)
}
func (a *Agent) CleanupModelAdaptation(ctx context.Context, mac string) error {
	unlock := a.lockClient(mac)
	defer unlock()
	datasets, err := a.zfs.ListDatasets(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(datasets, a.zfs.Dataset(storage.ModelAdaptCloneName(mac))) {
		return nil
	}
	return a.cleanupModelAdaptation(ctx, mac)
}
func (a *Agent) cleanupModelAdaptation(ctx context.Context, mac string) error {
	if a.exporter != nil {
		if err := a.exporter.TeardownTarget(ctx, iscsi.TargetForMAC(mac), []int{0}); err != nil {
			return err
		}
	}
	top, err := readTopology(ctx, a.zfs)
	if err != nil {
		return err
	}
	return top.ensureDestroyed(ctx, a.zfs.Dataset(storage.ModelAdaptCloneName(mac)))
}
