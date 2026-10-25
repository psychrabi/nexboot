package assets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

// ErrImageHealthBlocked enforces C-12: images with a blocking health report cannot be assigned to groups.
var ErrImageHealthBlocked = errs.Invalid("Image health report indicates a blocking failure")

type HealthCheckResult struct {
	TaskID string `json:"task_id"`
}

// dropHealthReport removes reports for data disks, which contain no bootable OS to inspect.
func (s ImageService) dropHealthReport(ctx context.Context, imageID string) {
	if old, err := s.Store.ImageHealthReports().GetByImage(ctx, imageID); err == nil {
		_ = s.Store.ImageHealthReports().Delete(ctx, old.ID)
	}
}

// RunHealthCheck launches an image_health_check task.
func (s ImageService) RunHealthCheck(ctx context.Context, imageID string) (HealthCheckResult, error) {
	img, err := s.Store.Images().Get(ctx, imageID)
	if err != nil {
		return HealthCheckResult{}, err
	}
	if img.Purpose == domain.ImagePurposeData {
		s.dropHealthReport(ctx, img.ID)
		return HealthCheckResult{}, errs.Invalid(fmt.Sprintf("%s is a data disk with no operating system; a health check is not applicable", img.Name))
	}
	task, err := s.runner().Run(ctx, domain.TaskTypeImageHealthCheck, img.ID, func(ctx context.Context, task domain.Task) error {
		return s.executeHealthCheck(ctx, task, img)
	})
	if err != nil {
		return HealthCheckResult{}, err
	}
	return HealthCheckResult{TaskID: task.ID}, nil
}

func (s ImageService) executeHealthCheck(ctx context.Context, task domain.Task, img domain.Image) error {
	configID, snapshotName, err := s.healthCheckSource(ctx, img.ID)
	if err != nil {
		return err
	}
	report, err := s.Storage.InspectImage(ctx, storage.InspectImageReq{
		ImageID:      img.ID,
		ConfigID:     configID,
		SnapshotName: snapshotName,
		OSType:       img.OSType,
	})
	if err != nil {
		return err
	}
	items := make([]domain.HealthCheckItem, 0, len(report.Items))
	for _, item := range report.Items {
		items = append(items, domain.HealthCheckItem{Name: item.Name, Level: item.Level, Detail: item.Detail})
	}
	record := domain.ImageHealthReport{
		ID:             "health-" + img.ID,
		ImageID:        img.ID,
		Level:          report.Level,
		PartitionStyle: report.PartitionStyle,
		BootModes:      report.BootModes,
		Items:          items,
		NICPCIIDs:      report.NICPCIIDs,
		CreatedAt:      s.now(),
	}
	// Retain only the latest health report per image; new checks overwrite old reports.
	if _, err := s.Store.ImageHealthReports().GetByImage(ctx, img.ID); err == nil {
		if err := s.Store.ImageHealthReports().Update(ctx, record); err != nil {
			return err
		}
	} else if errs.IsNotFound(err) {
		if err := s.Store.ImageHealthReports().Create(ctx, record); err != nil {
			return err
		}
	} else {
		return err
	}
	task.Status = domain.TaskStatusSuccess
	task.Progress = 100
	task.Result = string(report.Level)
	finished := s.now()
	task.FinishedAt = &finished
	return s.Store.Tasks().Update(ctx, task)
}

// healthCheckSource resolves Image → Default Configuration → Latest Ready Restore Point and returns (configID, ZFS snapshot).
func (s ImageService) healthCheckSource(ctx context.Context, imageID string) (string, string, error) {
	configs, err := s.Store.Configs().ListByImage(ctx, imageID)
	if err != nil {
		return "", "", err
	}
	if len(configs) == 0 {
		return "", "", fmt.Errorf("image %s has no config to inspect", imageID)
	}
	cfg := configs[0]
	for _, c := range configs {
		if c.Name == "default" {
			cfg = c
			break
		}
	}
	reductions, err := s.Store.Reductions().ListByConfig(ctx, cfg.ID)
	if err != nil {
		return "", "", err
	}
	var latest *domain.Reduction
	var latestAt time.Time
	for i, r := range reductions {
		if r.Status != domain.ReductionStatusReady {
			continue
		}
		if latest == nil || r.CreatedAt.After(latestAt) {
			latest = &reductions[i]
			latestAt = r.CreatedAt
		}
	}
	if latest == nil {
		return "", "", fmt.Errorf("config %s has no ready reduction to inspect", cfg.ID)
	}
	return cfg.ID, strings.TrimPrefix(latest.Name, "@"), nil
}

func (s ImageService) GetHealth(ctx context.Context, imageID string) (domain.ImageHealthReport, error) {
	if _, err := s.Store.Images().Get(ctx, imageID); err != nil {
		return domain.ImageHealthReport{}, err
	}
	return s.Store.ImageHealthReports().GetByImage(ctx, imageID)
}
