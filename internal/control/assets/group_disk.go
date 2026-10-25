package assets

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

var (
	ErrGroupDiskExists              = errs.Conflict("This mount target already exists")
	ErrGroupDiskMountTargetRequired = errs.Invalid("Mount target is required")
	ErrGroupDiskBadLetter           = errs.Invalid("Data disk drive letter for Windows groups must be a single letter from D: through Z:")
	ErrGroupDiskBadPath             = errs.Invalid("Linux data disks require an absolute mount path such as /data, not /, system directories such as /etc or /usr, .., or a trailing slash")
	ErrGroupDiskMismatch            = errs.Invalid("Data disk image and configuration do not match")
)

type GroupDiskService struct {
	Store store.Store
	Now   func() time.Time
}

type GroupDiskListResult struct {
	Items []domain.GroupDisk `json:"items"`
	Total int                `json:"total"`
}

type GroupDiskRequest struct {
	MountTarget string `json:"mount_target"`
	ImageID     string `json:"image_id"`
	ConfigID    string `json:"config_id"`
}

// GroupDataDisks lists group data disks in stable storage order, which determines their LUNs and drive letters.
// Boot exports disk i as DataDiskLUN(i). All consumers, including Super Admin shutdown, use the same ordering.
func GroupDataDisks(ctx context.Context, st store.Store, groupID string) ([]domain.GroupDisk, error) {
	disks, err := st.GroupDisks().List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]domain.GroupDisk, 0, len(disks))
	for _, disk := range disks {
		if disk.GroupID == groupID {
			items = append(items, disk)
		}
	}
	return items, nil
}

// DataDiskLUN returns the export LUN for group data disk i; 0 is the system disk and data disks start at 1.
func DataDiskLUN(index int) int { return index + 1 }

func (s GroupDiskService) List(ctx context.Context, groupID string) (GroupDiskListResult, error) {
	if _, err := s.Store.Groups().Get(ctx, groupID); err != nil {
		return GroupDiskListResult{}, err
	}
	items, err := GroupDataDisks(ctx, s.Store, groupID)
	if err != nil {
		return GroupDiskListResult{}, err
	}
	if items == nil {
		items = []domain.GroupDisk{}
	}
	return GroupDiskListResult{Items: items, Total: len(items)}, nil
}

func (s GroupDiskService) Create(ctx context.Context, groupID string, req GroupDiskRequest) (domain.GroupDisk, error) {
	terminalAllocMu.Lock()
	defer terminalAllocMu.Unlock()
	if err := refuseSuperGroupChange(ctx, s.Store, strings.TrimSpace(groupID), "add data disk"); err != nil {
		return domain.GroupDisk{}, err
	}

	disk, err := s.prepareGroupDisk(ctx, groupID, req)
	if err != nil {
		return domain.GroupDisk{}, err
	}
	if err := s.ensureMountTargetUnique(ctx, disk.GroupID, disk.MountTarget, ""); err != nil {
		return domain.GroupDisk{}, err
	}
	if err := s.Store.GroupDisks().Create(ctx, disk); err != nil {
		return domain.GroupDisk{}, err
	}
	return disk, nil
}

func (s GroupDiskService) Delete(ctx context.Context, id string) error {
	terminalAllocMu.Lock()
	defer terminalAllocMu.Unlock()
	disk, err := s.Store.GroupDisks().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := refuseSuperGroupChange(ctx, s.Store, disk.GroupID, "delete data disk"); err != nil {
		return err
	}
	return s.Store.GroupDisks().Delete(ctx, id)
}

func (s GroupDiskService) prepareGroupDisk(ctx context.Context, groupID string, req GroupDiskRequest) (domain.GroupDisk, error) {
	groupID = strings.TrimSpace(groupID)
	group, err := s.Store.Groups().Get(ctx, groupID)
	if err != nil {
		return domain.GroupDisk{}, err
	}
	mountTarget := strings.TrimSpace(req.MountTarget)
	if mountTarget == "" {
		return domain.GroupDisk{}, ErrGroupDiskMountTargetRequired
	}
	// Windows groups use actual D:–Z: drive letters, normalized for the injected client startup script.
	if group.SystemImageID != "" {
		sysImg, err := s.Store.Images().Get(ctx, group.SystemImageID)
		if err != nil {
			return domain.GroupDisk{}, err
		}
		if sysImg.OSType == domain.OSTypeLinux && !linuxMountPathOK(mountTarget) {
			return domain.GroupDisk{}, ErrGroupDiskBadPath
		}
		if sysImg.OSType == domain.OSTypeWindows {
			letter := strings.ToUpper(strings.TrimSuffix(mountTarget, ":"))
			if len(letter) != 1 || letter[0] < 'D' || letter[0] > 'Z' {
				return domain.GroupDisk{}, ErrGroupDiskBadLetter
			}
			mountTarget = letter + ":"
		}
	}
	imageID := strings.TrimSpace(req.ImageID)
	configID := strings.TrimSpace(req.ConfigID)
	if imageID == "" || configID == "" {
		return domain.GroupDisk{}, ErrGroupDiskMismatch
	}
	img, err := s.Store.Images().Get(ctx, imageID)
	if err != nil {
		return domain.GroupDisk{}, err
	}
	// Legacy records may point to system images predating purpose tagging; new data disks must use data-purpose images.
	if img.Purpose != domain.ImagePurposeData {
		return domain.GroupDisk{}, errs.Invalid(fmt.Sprintf("%s is a system disk image and cannot be used as a data disk. Select a data disk image or change its purpose in Image Management", img.Name))
	}
	cfg, err := s.Store.Configs().Get(ctx, configID)
	if err != nil {
		return domain.GroupDisk{}, err
	}
	if cfg.ImageID != imageID {
		return domain.GroupDisk{}, ErrGroupDiskMismatch
	}
	return domain.GroupDisk{
		ID:          fmt.Sprintf("group-disk-%d", s.now().UnixNano()),
		GroupID:     groupID,
		MountTarget: mountTarget,
		ImageID:     imageID,
		ConfigID:    configID,
	}, nil
}

func (s GroupDiskService) ensureMountTargetUnique(ctx context.Context, groupID, mountTarget, currentID string) error {
	disks, err := s.Store.GroupDisks().List(ctx)
	if err != nil {
		return err
	}
	for _, disk := range disks {
		if disk.ID != currentID && disk.GroupID == groupID && disk.MountTarget == mountTarget {
			return ErrGroupDiskExists
		}
	}
	return nil
}

func (s GroupDiskService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Mounting over these directories would hide operating system files.
var linuxSystemDirs = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32",
	"/proc", "/root", "/run", "/sbin", "/snap", "/sys", "/tmp", "/usr", "/var"}

func linuxMountPathOK(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || path.Clean(p) != p || strings.ContainsAny(p, "\t\n\r") {
		return false
	}
	for _, dir := range linuxSystemDirs {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return false
		}
	}
	return true
}
