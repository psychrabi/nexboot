package adapt

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/config"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/driverinf"
	"github.com/tianwei/diskless/internal/store"
)

var (
	ErrDriverPackInvalid     = errs.Invalid("驱动包必须是包含至少一个 .inf 的 zip")
	ErrDriverStatusInvalid   = errs.Invalid("驱动包状态无效")
	ErrDriverCategoryInvalid = errs.Invalid("驱动包类别无效")
)

// staleDriverAge 之前的驱动包在列表中标为有风险。
const staleDriverAge = 5 * 365 * 24 * time.Hour

type DriverService struct {
	Store    store.Store
	Dir      string
	FilesDir func(context.Context) (string, error)
	Now      func() time.Time
}

type UploadDriverPackRequest struct {
	Name     string
	Category domain.DriverPackCategory
	OSType   domain.OSType
	Arch     string
	Filename string
	Data     []byte
}

type DriverPackItem struct {
	Pack  domain.DriverPack `json:"pack"`
	Risks []string          `json:"risks"`
}

type DriverPackListResult struct {
	Items []DriverPackItem `json:"items"`
	Total int              `json:"total"`
}

func (s DriverService) UploadPack(ctx context.Context, req UploadDriverPackRequest) (DriverPackItem, error) {
	if len(req.Data) == 0 {
		return DriverPackItem{}, ErrDriverPackInvalid
	}
	reader, err := zip.NewReader(bytes.NewReader(req.Data), int64(len(req.Data)))
	if err != nil {
		return DriverPackItem{}, ErrDriverPackInvalid
	}

	var parsed []driverinf.Driver
	hasCatalog := false
	for _, f := range reader.File {
		lower := strings.ToLower(f.Name)
		switch {
		case strings.HasSuffix(lower, ".cat"):
			hasCatalog = true
		case strings.HasSuffix(lower, ".inf"):
			rc, err := f.Open()
			if err != nil {
				return DriverPackItem{}, err
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return DriverPackItem{}, err
			}
			d, err := driverinf.Parse(data)
			if err != nil {
				continue // 容忍混入的非驱动 inf 文件
			}
			parsed = append(parsed, d)
		}
	}
	if len(parsed) == 0 {
		return DriverPackItem{}, ErrDriverPackInvalid
	}

	merged := mergeDrivers(parsed)
	now := s.now()
	pack := domain.DriverPack{
		ID:          fmt.Sprintf("drvpack-%d", now.UnixNano()),
		Name:        strings.TrimSpace(req.Name),
		Category:    req.Category,
		OSType:      req.OSType,
		Arch:        strings.TrimSpace(req.Arch),
		Version:     merged.Version,
		ReleaseDate: merged.ReleaseDate,
		Vendor:      merged.Provider,
		Signed:      hasCatalog,
		HWIDs:       merged.HWIDs,
		Status:      domain.DriverPackEnabled,
		CreatedAt:   now,
	}
	if pack.Name == "" {
		pack.Name = defaultPackName(req.Filename, merged)
	}
	if pack.OSType == "" {
		pack.OSType = domain.OSTypeWindows
	}
	if pack.OSType != domain.OSTypeWindows && pack.OSType != domain.OSTypeLinux {
		return DriverPackItem{}, fmt.Errorf("invalid os_type: %s", pack.OSType)
	}
	if pack.Arch == "" {
		pack.Arch = "x64"
	}
	if pack.Category == "" {
		pack.Category = inferCategory(merged.ClassName)
	}
	if !validDriverCategory(pack.Category) {
		return DriverPackItem{}, ErrDriverCategoryInvalid
	}

	driverDir, err := s.driverDir(ctx)
	if err != nil {
		return DriverPackItem{}, err
	}
	dir := filepath.Join(driverDir, "packs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return DriverPackItem{}, err
	}
	pack.StoragePath = filepath.Join(dir, pack.ID+".zip")
	if err := os.WriteFile(pack.StoragePath, req.Data, 0o644); err != nil {
		return DriverPackItem{}, err
	}
	if err := s.Store.DriverPacks().Create(ctx, pack); err != nil {
		_ = os.Remove(pack.StoragePath)
		return DriverPackItem{}, err
	}
	return DriverPackItem{Pack: pack, Risks: s.packRisks(pack)}, nil
}

func (s DriverService) ListPacks(ctx context.Context) (DriverPackListResult, error) {
	packs, err := s.Store.DriverPacks().List(ctx)
	if err != nil {
		return DriverPackListResult{}, err
	}
	items := make([]DriverPackItem, 0, len(packs))
	for _, pack := range packs {
		items = append(items, DriverPackItem{Pack: pack, Risks: s.packRisks(pack)})
	}
	return DriverPackListResult{Items: items, Total: len(items)}, nil
}

func (s DriverService) SetPackStatus(ctx context.Context, id string, status domain.DriverPackStatus) (domain.DriverPack, error) {
	if status != domain.DriverPackEnabled && status != domain.DriverPackDisabled {
		return domain.DriverPack{}, ErrDriverStatusInvalid
	}
	pack, err := s.Store.DriverPacks().Get(ctx, id)
	if err != nil {
		return domain.DriverPack{}, err
	}
	pack.Status = status
	if err := s.Store.DriverPacks().Update(ctx, pack); err != nil {
		return domain.DriverPack{}, err
	}
	return pack, nil
}

func (s DriverService) DeletePack(ctx context.Context, id string) error {
	pack, err := s.Store.DriverPacks().Get(ctx, id)
	if err != nil {
		return err
	}
	file, err := s.PackPath(ctx, pack)
	if err != nil {
		return err
	}
	if err := s.Store.DriverPacks().Delete(ctx, id); err != nil {
		return err
	}
	_ = os.Remove(file)
	return nil
}

func safeArchivePath(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == "." || clean == "/" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

func (s DriverService) packRisks(pack domain.DriverPack) []string {
	risks := []string{}
	if !pack.Signed {
		risks = append(risks, "unsigned")
	}
	if len(pack.HWIDs) == 0 {
		risks = append(risks, "no_hwids")
	}
	if t, ok := parseDriverDate(pack.ReleaseDate); ok && s.now().Sub(t) > staleDriverAge {
		risks = append(risks, "outdated")
	}
	return risks
}

func parseDriverDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"01/02/2006", "1/2/2006", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func mergeDrivers(drivers []driverinf.Driver) driverinf.Driver {
	var merged driverinf.Driver
	seen := map[string]bool{}
	for _, d := range drivers {
		if merged.Provider == "" {
			merged.Provider = d.Provider
		}
		if merged.ClassName == "" {
			merged.ClassName = d.ClassName
		}
		if merged.Version == "" {
			merged.Version = d.Version
		}
		if merged.ReleaseDate == "" {
			merged.ReleaseDate = d.ReleaseDate
		}
		if merged.CatalogFile == "" {
			merged.CatalogFile = d.CatalogFile
		}
		for _, hw := range d.HWIDs {
			if !seen[hw] {
				seen[hw] = true
				merged.HWIDs = append(merged.HWIDs, hw)
			}
		}
	}
	return merged
}

func defaultPackName(filename string, merged driverinf.Driver) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(filename)), filepath.Ext(filename))
	if base != "" && base != "." {
		return base
	}
	if merged.Provider != "" {
		return strings.TrimSpace(merged.Provider + " " + merged.ClassName)
	}
	return "driver-pack"
}

func inferCategory(class string) domain.DriverPackCategory {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "net":
		return domain.DriverCategoryNIC
	case "display":
		return domain.DriverCategoryGPU
	case "media", "audioprocessingobject":
		return domain.DriverCategoryAudio
	case "system", "hdc", "scsiadapter", "usb":
		return domain.DriverCategoryChipset
	default:
		return domain.DriverCategoryOther
	}
}

func validDriverCategory(c domain.DriverPackCategory) bool {
	switch c {
	case domain.DriverCategoryBootCriticalNIC, domain.DriverCategoryNIC, domain.DriverCategoryGPU,
		domain.DriverCategoryAudio, domain.DriverCategoryChipset, domain.DriverCategoryOther:
		return true
	}
	return false
}

// PackPath 优先当前文件库；旧绝对路径只在当前文件缺失且旧文件存在时兼容。
func (s DriverService) PackPath(ctx context.Context, pack domain.DriverPack) (string, error) {
	dir, err := s.driverDir(ctx)
	if err != nil {
		return "", err
	}
	derived := filepath.Join(dir, "packs", pack.ID+".zip")
	if _, err := os.Stat(derived); err == nil {
		return derived, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if pack.StoragePath != "" && pack.StoragePath != derived {
		if _, err := os.Stat(pack.StoragePath); err == nil {
			return pack.StoragePath, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return derived, nil
}

func (s DriverService) driverDir(ctx context.Context) (string, error) {
	if s.FilesDir != nil {
		dir, err := s.FilesDir(ctx)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "drivers"), nil
	}
	if strings.TrimSpace(s.Dir) == "" {
		return config.DefaultDriverDir, nil
	}
	return s.Dir, nil
}

func (s DriverService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// uniqueStrings 去空白、去空、去重；assets 包里有同样的私有实现，复制一份好过跨域 import。
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
