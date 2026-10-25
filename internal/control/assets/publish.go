package assets

import (
	"context"
	"errors"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// PublishDataDiskRequest identifies a Super Admin data disk and the resulting restore-point name.
type PublishDataDiskRequest struct {
	DiskID string `json:"disk_id"`
	Name   string `json:"name"`
}

// PublishDataDiskResult includes the new restore point and optional Detail when a later step fails,
// so an already completed publish is not incorrectly reported as failed.
type PublishDataDiskResult struct {
	TaskID    string            `json:"task_id,omitempty"`
	Reduction *domain.Reduction `json:"reduction,omitempty"`
	Detail    string            `json:"detail,omitempty"`
}

// quietWindow is the required write-free interval before publishing a disk.
const quietWindow = 3 * time.Second

func (s TerminalService) quietWindow() time.Duration {
	if s.QuietWindow > 0 {
		return s.QuietWindow
	}
	return quietWindow
}

// PublishDataDisk snapshots a Super Admin data disk without shutdown and makes the point current for its configuration,
// effective at client next boot. System disks require shutdown to save.
func (s TerminalService) PublishDataDisk(ctx context.Context, id string, req PublishDataDiskRequest) (PublishDataDiskResult, error) {
	terminal, err := s.Store.Terminals().Get(ctx, id)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	if !terminal.IsSuper {
		return PublishDataDiskResult{}, ErrTerminalSuperRequired
	}
	group, err := s.Store.Groups().Get(ctx, terminal.GroupID)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	disks, err := GroupDataDisks(ctx, s.Store, group.ID)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	disk, lun, ok := findDataDisk(disks, req.DiskID)
	if !ok {
		return PublishDataDiskResult{}, errs.Invalid("分组 " + group.Name + " does not contain this data disk. Refresh and try again")
	}
	cfg, err := s.Store.Configs().Get(ctx, disk.ConfigID)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	claim, err := claimImages(ctx, s.Store, domain.TaskTypePublishDataDisk, cfg.ImageID)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	defer claim.Drop()
	reductions, err := s.Store.Reductions().ListByConfig(ctx, cfg.ID)
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	displayName, name, err := validateReductionName(req.Name, reductions)
	if err != nil {
		return PublishDataDiskResult{}, err
	}

	var result PublishDataDiskResult
	task, err := claim.Run(ctx, s.runner(), domain.TaskTypePublishDataDisk, terminal.MAC, func(ctx context.Context, task domain.Task) error {
		var err error
		result, err = s.executePublish(ctx, task, terminal, disk, cfg, lun, name, displayName)
		return err
	})
	if err != nil {
		return PublishDataDiskResult{}, err
	}
	if s.Async {
		return PublishDataDiskResult{TaskID: task.ID}, nil
	}
	result.TaskID = task.ID
	return result, nil
}

func (s TerminalService) executePublish(ctx context.Context, task domain.Task, terminal domain.Terminal, disk domain.GroupDisk, cfg domain.Config, lun int, name, displayName string) (PublishDataDiskResult, error) {
	// Pool and database must change together, without interleaved replication cycles (see storage.ChangeCatalogue).
	defer storage.ChangeCatalogue()()
	if err := s.requireQuietDisk(ctx, terminal, lun, disk.MountTarget); err != nil {
		return PublishDataDiskResult{}, err
	}
	out, err := s.Storage.PublishDataDisk(ctx, storage.PublishDataDiskReq{
		MAC: terminal.MAC, LUN: lun, ConfigID: cfg.ID, ReductionName: name, MountTarget: disk.MountTarget})
	detail := ""
	if err != nil {
		// If publishing succeeded but remount failed, avoid reporting failure and triggering duplicate publishes; reboot the client.
		var notBack storage.DiskNotReattached
		if !errors.As(err, &notBack) {
			return PublishDataDiskResult{}, err
		}
		detail = "Published; " + disk.MountTarget + " did not remount; reboot the client to restore access"
	}
	reduction := out.Reduction
	reduction.ConfigID = cfg.ID
	reduction.Name = name
	reduction.DisplayName = displayName
	if err := s.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.Reductions().Create(ctx, reduction); err != nil {
			return err
		}
		// Publishing sets the restore point as current, making it available to clients on next boot.
		if err := setConfigCurrent(ctx, tx, cfg.ID, reduction.ID); err != nil {
			return err
		}
		return s.runner().Finish(ctx, tx, task, reduction.ID)
	}); err != nil {
		return PublishDataDiskResult{}, err
	}
	if s.BackupTrigger != nil {
		go func() {
			if err := s.BackupTrigger(context.Background()); err != nil && s.Logger != nil {
				s.Logger.Error("backup trigger failed", "error", err)
			}
		}()
	}
	return PublishDataDiskResult{Reduction: &reduction, Detail: detail}, nil
}

// requireQuietDisk rejects disks still receiving writes; publishing a snapshot mid-install would distribute
// incomplete contents to every client. Detect writes by sampling bytes twice, without a client agent.
func (s TerminalService) requireQuietDisk(ctx context.Context, terminal domain.Terminal, lun int, mount string) error {
	if s.Storage == nil || terminal.State != domain.TerminalStateOnline {
		return nil // A shut-down client cannot be writing.
	}
	first, err := s.Storage.SuperDiskWritten(ctx, terminal.MAC, lun)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.quietWindow()):
	}
	second, err := s.Storage.SuperDiskWritten(ctx, terminal.MAC, lun)
	if err != nil {
		return err
	}
	if second != first {
		return errs.Conflict(mount + " is still being written. Wait for writes to finish before publishing")
	}
	return nil
}

func findDataDisk(disks []domain.GroupDisk, id string) (domain.GroupDisk, int, bool) {
	for i, disk := range disks {
		if disk.ID == id {
			return disk, DataDiskLUN(i), true
		}
	}
	return domain.GroupDisk{}, 0, false
}

// SuperDiskItem exposes a Super Admin data disk's mount target, current published point and pending writes.
type SuperDiskItem struct {
	DiskID      string `json:"disk_id"`
	LUN         int    `json:"lun"`
	MountTarget string `json:"mount_target"`
	ConfigID    string `json:"config_id"`
	ConfigName  string `json:"config_name"`
	CurrentName string `json:"current_name"`
	Written     int64  `json:"written"`
	// Ready=false means the client has not booted as Super Admin since the disk was assigned.
	Ready bool `json:"ready"`
}

type SuperDiskListResult struct {
	Items []SuperDiskItem `json:"items"`
	Total int             `json:"total"`
}

// SuperDisks lists Super Admin data disks; system disks are saved on shutdown rather than through publish.
func (s TerminalService) SuperDisks(ctx context.Context, id string) (SuperDiskListResult, error) {
	terminal, err := s.Store.Terminals().Get(ctx, id)
	if err != nil {
		return SuperDiskListResult{}, err
	}
	if !terminal.IsSuper {
		return SuperDiskListResult{}, ErrTerminalSuperRequired
	}
	disks, err := GroupDataDisks(ctx, s.Store, terminal.GroupID)
	if err != nil {
		return SuperDiskListResult{}, err
	}
	items := make([]SuperDiskItem, 0, len(disks))
	for i, disk := range disks {
		item := SuperDiskItem{DiskID: disk.ID, LUN: DataDiskLUN(i), MountTarget: disk.MountTarget, ConfigID: disk.ConfigID}
		if cfg, err := s.Store.Configs().Get(ctx, disk.ConfigID); err == nil {
			item.ConfigName = cfg.Name
			if cfg.DefaultReductionID != nil && *cfg.DefaultReductionID != "" {
				if red, err := s.Store.Reductions().Get(ctx, *cfg.DefaultReductionID); err == nil {
					item.CurrentName = displayNameOf(red)
				}
			}
		}
		if s.Storage != nil {
			// Missing pool data indicates the client has not booted this disk in Super Admin mode; keep Ready=false.
			if written, err := s.Storage.SuperDiskWritten(ctx, terminal.MAC, item.LUN); err == nil {
				item.Written, item.Ready = written, true
			}
		}
		items = append(items, item)
	}
	return SuperDiskListResult{Items: items, Total: len(items)}, nil
}
