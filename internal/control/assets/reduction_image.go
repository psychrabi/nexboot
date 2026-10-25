package assets

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// SaveAsImageRequest contains the image name for saving a restore point as a separate image.
type SaveAsImageRequest struct {
	Name string `json:"name"`
}

// restorePointSource validates and resolves the source for exporting/copying a restore point.
func (s ImageService) restorePointSource(ctx context.Context, reductionID string) (domain.Reduction, domain.Image, error) {
	red, err := s.Store.Reductions().Get(ctx, reductionID)
	if err != nil {
		return domain.Reduction{}, domain.Image{}, err
	}
	if red.Status != domain.ReductionStatusReady {
		return domain.Reduction{}, domain.Image{}, errs.Conflict(fmt.Sprintf("Restore point %s is not ready. Wait until it completes before proceeding", displayNameOf(red)))
	}
	cfg, err := s.Store.Configs().Get(ctx, red.ConfigID)
	if err != nil {
		return domain.Reduction{}, domain.Image{}, err
	}
	img, err := s.exportableImage(ctx, cfg.ImageID)
	return red, img, err
}

// ReductionExportName returns the suggested filename for restore-point downloads.
func (s ImageService) ReductionExportName(ctx context.Context, reductionID string, compress bool) (string, error) {
	red, img, err := s.restorePointSource(ctx, reductionID)
	if err != nil {
		return "", err
	}
	name := sanitizeExportName(img.Name + "-" + displayNameOf(red))
	if compress {
		return name + ".zfs.gz", nil
	}
	return name + ".zfs", nil
}

// StreamReduction writes a restore point as a re-importable image stream.
func (s ImageService) StreamReduction(ctx context.Context, reductionID string, w io.Writer, compress bool) error {
	red, img, err := s.restorePointSource(ctx, reductionID)
	if err != nil {
		return err
	}
	defer beginExport(img.ID)()
	return s.streamExport(ctx, storage.ExportImageReq{ConfigID: red.ConfigID, Snapshot: strings.TrimPrefix(red.Name, "@")}, w, compress)
}

// ExportReductionToDir saves a restore point to the local import directory; see exportToDir.
func (s ImageService) ExportReductionToDir(ctx context.Context, reductionID string) (ExportImageResult, error) {
	red, img, err := s.restorePointSource(ctx, reductionID)
	if err != nil {
		return ExportImageResult{}, err
	}
	return s.exportToDir(ctx, img, red.ConfigID, img.Name+"-"+displayNameOf(red),
		storage.ExportImageReq{ConfigID: red.ConfigID, Snapshot: strings.TrimPrefix(red.Name, "@")})
}

// SaveReductionAsImage clones a restore point into an independent image that may be deleted separately.
func (s ImageService) SaveReductionAsImage(ctx context.Context, reductionID string, req SaveAsImageRequest) (ImportImageResult, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ImportImageResult{}, errs.Invalid("Enter a name for the new image")
	}
	red, src, err := s.restorePointSource(ctx, reductionID)
	if err != nil {
		return ImportImageResult{}, err
	}
	claim, err := claimImageName(name, "save as image")
	if err != nil {
		return ImportImageResult{}, err
	}
	defer claim.Drop()
	if _, err := s.Store.Images().GetByName(ctx, name); err == nil {
		return ImportImageResult{}, ErrImageExists
	} else if !errs.IsNotFound(err) {
		return ImportImageResult{}, err
	}
	if err := s.ensureCopyFits(ctx, red, name); err != nil {
		return ImportImageResult{}, err
	}
	task, err := claim.Run(ctx, s.runner(), domain.TaskTypeCopyImage, src.ID, func(ctx context.Context, task domain.Task) error {
		return s.executeSaveAsImage(ctx, task, red, src, name)
	})
	if err != nil {
		return ImportImageResult{}, err
	}
	return ImportImageResult{TaskID: task.ID}, nil
}

func (s ImageService) executeSaveAsImage(ctx context.Context, task domain.Task, red domain.Reduction, src domain.Image, name string) error {
	copied, err := s.Storage.CopyReductionToImage(ctx, storage.CopyReductionReq{
		ConfigID: red.ConfigID, Snapshot: strings.TrimPrefix(red.Name, "@"), Name: name, OSType: src.OSType, Purpose: src.Purpose,
		OnTarget: func(imageID string) { recordTarget(ctx, s.Store, &task, imageID) },
		OnProgress: func(percent int, message string) {
			t := task
			t.Status, t.Progress, t.Message = domain.TaskStatusRunning, percent, message
			_ = s.Store.Tasks().Update(ctx, t)
		},
	})
	if err != nil {
		return err
	}
	// Contents, including the startup script, are inherited from the source image.
	copied.Image.MountScriptVersion = src.MountScriptVersion
	if err := s.Store.Tx(ctx, func(tx store.Store) error {
		for _, err := range []error{tx.Images().Create(ctx, copied.Image), tx.Configs().Create(ctx, copied.Config), tx.Reductions().Create(ctx, copied.Reduction)} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if rbErr := s.Storage.DeleteImage(context.Background(), copied.Image.ID, []string{copied.Config.ID}); rbErr != nil {
			slog.Warn("copied image not rolled back", "image", copied.Image.ID, "error", rbErr)
		}
		return err
	}
	if copied.Image.Purpose != domain.ImagePurposeData {
		_, _ = s.RunHealthCheck(ctx, copied.Image.ID)
	}
	return s.runner().Finish(ctx, s.Store, task, copied.Image.ID)
}

// ensureCopyFits rejects copies that clearly exceed pool capacity; uncertain estimates defer to actual copy errors.
func (s ImageService) ensureCopyFits(ctx context.Context, red domain.Reduction, name string) error {
	need, err := s.Storage.ExportImageSize(ctx, red.ConfigID)
	if err != nil || need <= 0 {
		return nil
	}
	usage, err := s.Storage.SpaceUsage(ctx)
	if err != nil || need <= usage.PoolAvailable {
		return nil
	}
	return errs.Conflict(fmt.Sprintf("Free pool space before saving %s as a new image: %s available, approximately %s required. Delete unused images/restore points or add pool disks",
		name, fmtBytes(usage.PoolAvailable), fmtBytes(need)))
}
