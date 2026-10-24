package pe

import (
	"archive/zip"
	"context"
	"fmt"
	"github.com/tianwei/diskless/internal/control/adapt"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StageUploadedDriver keeps an existing image/template driver ahead of uploaded packages.
func (s SessionService) StageUploadedDriver(ctx context.Context, hwid, dir string) ([]string, error) {
	files, err := driverFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("请检查网卡驱动准备目录：%w", err)
	}
	if len(files) > 0 {
		return nil, nil
	}
	packs, err := s.Store.DriverPacks().List(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(packs, func(i, j int) bool { return packs[i].Version > packs[j].Version })
	for _, pack := range packs {
		if pack.Status != domain.DriverPackEnabled || pack.OSType != domain.OSTypeWindows || !pack.Signed ||
			(pack.Arch != "" && !strings.EqualFold(pack.Arch, "x64") && !strings.EqualFold(pack.Arch, "amd64")) {
			continue
		}
		matches := false
		for _, id := range pack.HWIDs {
			if strings.HasPrefix(strings.ToUpper(id), strings.ToUpper(hwid)) {
				matches = true
			}
		}
		if !matches {
			continue
		}
		name := pack.Name
		if name == "" {
			name = pack.ID
		}
		file, err := (adapt.DriverService{FilesDir: s.FilesDir}).PackPath(ctx, pack)
		if err != nil {
			return nil, fmt.Errorf("请检查驱动包「%s」：%w", name, err)
		}
		zr, err := zip.OpenReader(file)
		if err != nil {
			return nil, fmt.Errorf("请重新上传驱动包「%s」：%w", name, err)
		}
		err = func() error {
			defer zr.Close()
			for _, f := range zr.File {
				name := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(f.Name, `\`, "/")))
				unsafe := filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.ContainsAny(name, "\r\n|\":*?<>")
				for _, part := range strings.Split(filepath.ToSlash(name), "/") {
					unsafe = unsafe || strings.TrimRight(part, " .") != part
				}
				if unsafe {
					return errs.Invalid("驱动包文件路径无效")
				}
				target := filepath.Join(dir, name)
				if f.FileInfo().IsDir() {
					continue
				}
				if f.UncompressedSize64 > 64<<20 {
					return errs.Invalid("驱动包内单文件超过64MB")
				}
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return err
				}
				src, err := f.Open()
				if err != nil {
					return err
				}
				data, err := io.ReadAll(io.LimitReader(src, 64<<20+1))
				src.Close()
				if err != nil {
					return err
				}
				if len(data) > 64<<20 {
					return errs.Invalid("驱动文件超过64MB")
				}
				if err := os.WriteFile(target, data, 0600); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			return nil, fmt.Errorf("请重新上传驱动包「%s」：%w", name, err)
		}
		return []string{pack.ID}, nil
	}
	// Windows/WinPE inbox drivers are the first source; a missing matching package is reported by the PE host probe.
	return nil, nil
}
func driverFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if strings.ContainsAny(rel, "\r\n|\"") {
			return fmt.Errorf("驱动文件名无效")
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}
