package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tianwei/diskless/internal/storage"
)

// winREFiles 是镜像里恢复环境的文件在挂载点下的路径。Curl、BCDEFI、BCDPCAT 可空：
// 回报靠系统分区里的 curl.exe；启动配置取 Windows\Boot\DVD 下做恢复介质用的通用 BCD。
type winREFiles struct {
	WIM     string
	SDI     string
	Curl    string
	BCDEFI  string
	BCDPCAT string
}

// findWinRE 在只读挂载的各分区里找恢复环境：先看恢复分区的 Recovery\WindowsRE，
// 再看系统分区的 Windows\System32\Recovery（禁用恢复环境后文件会搬到这里）。
func findWinRE(mounts []mountedFS) (winREFiles, bool) {
	var extra winREFiles
	for _, m := range mounts {
		if p, ok := lookupPath(m.Root, "Windows/System32/curl.exe"); ok && extra.Curl == "" {
			extra.Curl = p
		}
		if p, ok := lookupPath(m.Root, "Windows/Boot/DVD/EFI/BCD"); ok && extra.BCDEFI == "" {
			extra.BCDEFI = p
		}
		if p, ok := lookupPath(m.Root, "Windows/Boot/DVD/PCAT/BCD"); ok && extra.BCDPCAT == "" {
			extra.BCDPCAT = p
		}
	}
	for _, m := range mounts {
		if wim, ok := lookupPath(m.Root, "Recovery/WindowsRE/Winre.wim"); ok {
			if sdi, ok := lookupPath(m.Root, "Recovery/WindowsRE/boot.sdi"); ok {
				extra.WIM, extra.SDI = wim, sdi
				return extra, true
			}
		}
	}
	for _, m := range mounts {
		wim, ok := lookupPath(m.Root, "Windows/System32/Recovery/Winre.wim")
		if !ok {
			continue
		}
		for _, rel := range []string{"Windows/System32/Recovery/boot.sdi", "Windows/Boot/DVD/EFI/boot.sdi", "Windows/Boot/DVD/PCAT/boot.sdi"} {
			if sdi, ok := lookupPath(m.Root, rel); ok {
				extra.WIM, extra.SDI = wim, sdi
				return extra, true
			}
		}
	}
	return winREFiles{}, false
}

// ExtractWinRE 从还原点临时克隆里拷出恢复环境到 req.DestDir（boot.wim、boot.sdi），只读、不改镜像。
func (a *Agent) ExtractWinRE(ctx context.Context, req storage.ExtractWinREReq) (storage.WinREExtract, error) {
	if req.ImageID == "" || req.ConfigID == "" || req.SnapshotName == "" || req.DestDir == "" {
		return storage.WinREExtract{}, fmt.Errorf("image, config, reduction and destination are required")
	}
	r := a.runner
	if r == nil {
		r = execRunner{}
	}
	dataset := a.zfs.Dataset(storage.WinRECloneName(req.ImageID))
	if err := a.zfs.Clone(ctx, a.zfs.Snapshot(req.ConfigID, req.SnapshotName), dataset); err != nil {
		return storage.WinREExtract{}, err
	}
	defer a.destroyDetached(dataset)

	var out storage.WinREExtract
	err := withEachPartition(ctx, r, a.zfs.VolumePath(dataset), func(mounts []mountedFS) error {
		files, ok := findWinRE(mounts)
		if !ok {
			return fmt.Errorf("镜像里没有恢复环境（Winre.wim），请上传用 ADK 生成的适配环境包")
		}
		if err := os.MkdirAll(req.DestDir, 0o755); err != nil {
			return err
		}
		wim := filepath.Join(req.DestDir, "boot.wim")
		sdi := filepath.Join(req.DestDir, "boot.sdi")
		wimSum, size, err := copyWinREFile(files.WIM, wim)
		if err != nil {
			return err
		}
		sdiSum, _, err := copyWinREFile(files.SDI, sdi)
		if err != nil {
			return err
		}
		out = storage.WinREExtract{WIMPath: wim, SDIPath: sdi, WIMSHA256: wimSum, SDISHA256: sdiSum, WIMSize: size}
		if files.Curl != "" {
			curl := filepath.Join(req.DestDir, "curl.exe")
			if sum, _, err := copyWinREFile(files.Curl, curl); err == nil {
				out.CurlPath, out.CurlSHA256 = curl, sum
			}
		}
		if files.BCDEFI != "" {
			dst := filepath.Join(req.DestDir, "BCD")
			if sum, _, err := copyWinREFile(files.BCDEFI, dst); err == nil {
				out.BCDEFIPath, out.BCDEFISHA256 = dst, sum
			}
		}
		if files.BCDPCAT != "" {
			dst := filepath.Join(req.DestDir, "BCD.pcat")
			if sum, _, err := copyWinREFile(files.BCDPCAT, dst); err == nil {
				out.BCDPCATPath, out.BCDPCATSHA256 = dst, sum
			}
		}
		return nil
	})
	return out, err
}

// copyWinREFile 先写临时名再改名到位，中断不会留下被当成可用环境的半截文件；哈希随拷贝计算。
func copyWinREFile(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".winre-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), in)
	if err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
