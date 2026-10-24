package local

import (
	"context"
	"fmt"
	"github.com/tianwei/diskless/internal/driverinf"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (a *Agent) prepareModelVolume(ctx context.Context, dataset, hwid, dest string) error {
	r := a.runner
	if r == nil {
		r = execRunner{}
	}
	return withWindowsPartition(ctx, r, a.zfs.VolumePath(dataset), volRW, clonePartitionWait, func(root string) error {
		if err := os.WriteFile(filepath.Join(root, "ndadapt-target.txt"), []byte(dataset), 0600); err != nil {
			return err
		}
		repo, ok := lookupPath(root, "Windows/System32/DriverStore/FileRepository")
		if !ok {
			return nil
		}
		match := ""
		err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".inf") {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			inf, err := driverinf.Parse(raw)
			if err != nil {
				return nil
			}
			for _, id := range inf.HWIDs {
				if strings.HasPrefix(strings.ToUpper(id), strings.ToUpper(hwid)) {
					match = filepath.Dir(p)
					return fs.SkipAll
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if match == "" {
			return nil
		}
		return filepath.WalkDir(match, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(match, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dest, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0755)
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("驱动目录包含链接：%s", rel)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0600)
		})
	})
}
