package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/driverinf"
	"github.com/tianwei/diskless/internal/storage"
)

// InspectImage 体检镜像：临时克隆 -> 只读挂载 -> 文件树检查 -> 清理。
// mount/umount/blkid/lsblk 等外部命令只在这一层调用，绝不写入镜像（C-11）。
func (a *Agent) InspectImage(ctx context.Context, req storage.InspectImageReq) (storage.InspectReport, error) {
	if req.ImageID == "" || req.ConfigID == "" || req.SnapshotName == "" || req.OSType == "" {
		return storage.InspectReport{}, fmt.Errorf("image, config, reduction and os type are required")
	}
	r := a.runner
	if r == nil {
		r = execRunner{}
	}
	cloneName := storage.InspectCloneName(req.ImageID)
	dataset := a.zfs.Dataset(cloneName)
	if err := a.zfs.Clone(ctx, a.zfs.Snapshot(req.ConfigID, req.SnapshotName), dataset); err != nil {
		return storage.InspectReport{}, err
	}
	// 临时克隆无论成败都不能留下。
	defer a.destroyDetached(dataset)

	dev := a.zfs.VolumePath(dataset)
	var report storage.InspectReport
	err := withEachPartition(ctx, r, dev, func(mounts []mountedFS) error {
		// blkid 放在会话内执行：分区轮询已等过新克隆设备节点的延迟。
		partitionStyle := detectPartitionStyle(ctx, r, dev)
		bootModes := detectBootModes(partitionStyle, partitionTypes(ctx, r, dev), mounts)
		items := []storage.InspectItem{partitionStyleItem(partitionStyle)}
		if len(bootModes) > 0 {
			items = append(items, bootModeItem(bootModes))
		}
		checks, nicIDs := inspectMountsReport(mounts, req.OSType)
		items = append(items, checks...)
		report = storage.InspectReport{
			Level:          aggregateLevel(items),
			PartitionStyle: partitionStyle,
			BootModes:      bootModes,
			Items:          items,
			NICPCIIDs:      nicIDs,
		}
		return nil
	})
	return report, err
}

func detectPartitionStyle(ctx context.Context, r runner, dev string) string {
	out, err := r.Run(ctx, "blkid", "-o", "value", "-s", "PTTYPE", dev)
	if err != nil {
		return ""
	}
	switch strings.TrimSpace(string(out)) {
	case "dos":
		return "mbr"
	case "gpt":
		return "gpt"
	default:
		return ""
	}
}

func partitionStyleItem(style string) storage.InspectItem {
	if style == "" {
		return storage.InspectItem{Name: "partition_style", Level: domain.HealthUnknown, Detail: "无法识别分区表类型"}
	}
	return storage.InspectItem{Name: "partition_style", Level: domain.HealthOK, Detail: strings.ToUpper(style)}
}

// GPT 上 BIOS 版 GRUB 要这种分区放 core.img。
const biosBootPartType = "21686148-6449-6e6f-744e-656564454649"

// partitionTypes 列出 dev 上各分区的类型（GPT GUID 或 0xef 这类 MBR 编号）。
// 出错按无结果处理，启动模式就不参考分区类型。
func partitionTypes(ctx context.Context, r runner, dev string) []string {
	out, err := r.Run(ctx, "lsblk", "-J", "-o", "PATH,PARTTYPE", dev)
	if err != nil {
		return nil
	}
	type entry struct {
		PartType string `json:"parttype"`
	}
	// 不带 NAME 列时输出扁平列表，否则嵌套在 children 下。
	var payload struct {
		Blockdevices []struct {
			entry
			Children []entry `json:"children"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal(out, &payload) != nil {
		return nil
	}
	var types []string
	for _, d := range payload.Blockdevices {
		for _, e := range append([]entry{d.entry}, d.Children...) {
			if e.PartType != "" {
				types = append(types, strings.ToLower(e.PartType))
			}
		}
	}
	return types
}

// detectBootModes 判断固件能以哪种方式启动镜像。iPXE 的 UEFI sanboot 找可移动介质路径，
// 所以 UEFI 需要 EFI/BOOT/BOOTX64.EFI；BIOS 需要 MBR 盘或 BIOS boot 分区（仅 GPT）。返回 nil 表示无法判断。
func detectBootModes(style string, partTypes []string, mounts []mountedFS) []string {
	var modes []string
	if style == "mbr" || slices.Contains(partTypes, biosBootPartType) {
		modes = append(modes, domain.BootModeBIOS)
	}
	for _, m := range mounts {
		if _, ok := lookupPath(m.Root, "EFI/BOOT/BOOTX64.EFI"); ok {
			modes = append(modes, domain.BootModeUEFI)
			break
		}
	}
	return modes
}

func bootModeItem(modes []string) storage.InspectItem {
	detail := "仅支持 BIOS 引导"
	switch {
	case len(modes) == 2:
		detail = "支持 BIOS 和 UEFI 引导"
	case modes[0] == domain.BootModeUEFI:
		detail = "仅支持 UEFI 引导"
	}
	return storage.InspectItem{Name: "boot_mode", Level: domain.HealthOK, Detail: detail}
}

// inspectMounts 对已挂载的根目录做纯文件树检查，与挂载解耦以便用假目录树单测。
func inspectMounts(mounts []mountedFS, osType domain.OSType) []storage.InspectItem {
	items, _ := inspectMountsReport(mounts, osType)
	return items
}

// inspectMountsReport 在 inspectMounts 之外，顺带返回同一次遍历 DriverStore 得到的网卡驱动 PCI 编号。
func inspectMountsReport(mounts []mountedFS, osType domain.OSType) ([]storage.InspectItem, []string) {
	if osType == domain.OSTypeWindows {
		return inspectWindows(mounts)
	}
	return inspectLinux(mounts), nil
}

func inspectWindows(mounts []mountedFS) ([]storage.InspectItem, []string) {
	var items []storage.InspectItem
	var nicIDs []string

	var winRoot string
	for _, m := range mounts {
		if _, ok := lookupPath(m.Root, "Windows/System32"); ok {
			winRoot = m.Root
			break
		}
	}
	if winRoot == "" {
		items = append(items, storage.InspectItem{Name: "windows_partition", Level: domain.HealthBlock, Detail: `未找到包含 \Windows\System32 的系统分区`})
	} else {
		items = append(items, storage.InspectItem{Name: "windows_partition", Level: domain.HealthOK, Detail: "找到 Windows 系统分区"})
	}

	if winRoot == "" {
		items = append(items,
			storage.InspectItem{Name: "msiscsi", Level: domain.HealthBlock, Detail: "无法检查：系统分区缺失"},
			storage.InspectItem{Name: "driverstore_nic", Level: domain.HealthUnknown, Detail: "无法检查：系统分区缺失"},
		)
	} else {
		if _, ok := lookupPath(winRoot, "Windows/System32/drivers/msiscsi.sys"); ok {
			items = append(items, storage.InspectItem{Name: "msiscsi", Level: domain.HealthOK, Detail: "msiscsi.sys 存在（iSCSI 启动栈可用）"})
		} else {
			items = append(items, storage.InspectItem{Name: "msiscsi", Level: domain.HealthBlock, Detail: `\Windows\System32\drivers\msiscsi.sys 缺失，网络启动必蓝屏`})
		}
		item, ids := inspectDriverStore(winRoot)
		items = append(items, item)
		nicIDs = ids
	}

	bcdFound := false
	for _, m := range mounts {
		if _, ok := lookupPath(m.Root, "Boot/BCD"); ok {
			bcdFound = true
			break
		}
		if _, ok := lookupPath(m.Root, "EFI/Microsoft/Boot/BCD"); ok {
			bcdFound = true
			break
		}
	}
	if bcdFound {
		items = append(items, storage.InspectItem{Name: "bcd", Level: domain.HealthOK, Detail: "BCD 启动配置存在"})
	} else {
		items = append(items, storage.InspectItem{Name: "bcd", Level: domain.HealthWarn, Detail: "未找到 BCD（\\Boot\\BCD 或 EFI 分区），可能由 iPXE 直接引导，建议试启动验证"})
	}

	items = append(items, storage.InspectItem{Name: "boot_registry", Level: domain.HealthUnknown, Detail: "启动关键注册状态无法离线深检，请通过试启动确认"})
	return items, nicIDs
}

func inspectDriverStore(winRoot string) (storage.InspectItem, []string) {
	repo, ok := lookupPath(winRoot, "Windows/System32/DriverStore/FileRepository")
	if !ok {
		return storage.InspectItem{Name: "driverstore_nic", Level: domain.HealthWarn, Detail: "DriverStore FileRepository 缺失"}, nil
	}
	var nicINFs []string
	hwids := 0
	pci := map[string]bool{}
	_ = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".inf") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		drv, err := driverinf.Parse(data)
		if err != nil {
			return nil
		}
		if strings.EqualFold(drv.ClassName, "Net") {
			nicINFs = append(nicINFs, d.Name())
			hwids += len(drv.HWIDs)
			for _, id := range drv.HWIDs {
				if m := pciVenDev.FindStringSubmatch(id); m != nil {
					pci[strings.ToUpper(m[1]+":"+m[2])] = true
				}
			}
		}
		return nil
	})
	if len(nicINFs) == 0 {
		return storage.InspectItem{Name: "driverstore_nic", Level: domain.HealthWarn, Detail: "DriverStore 未发现网卡驱动 INF，换网卡机型可能无法启动"}, nil
	}
	ids := make([]string, 0, len(pci))
	for id := range pci {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return storage.InspectItem{
		Name:   "driverstore_nic",
		Level:  domain.HealthOK,
		Detail: fmt.Sprintf("网卡驱动 INF %d 个（覆盖 HWID %d 条）: %s", len(nicINFs), hwids, strings.Join(nicINFs, ", ")),
	}, ids
}

var pciVenDev = regexp.MustCompile(`(?i)^PCI\\VEN_([0-9A-F]{4})&DEV_([0-9A-F]{4})`)

func inspectLinux(mounts []mountedFS) []storage.InspectItem {
	var items []storage.InspectItem

	var rootFS string
	for _, m := range mounts {
		if _, ok := lookupPath(m.Root, "etc"); ok {
			rootFS = m.Root
			break
		}
	}
	if rootFS == "" {
		items = append(items, storage.InspectItem{Name: "linux_rootfs", Level: domain.HealthBlock, Detail: "未找到 Linux 根文件系统"})
		items = append(items, storage.InspectItem{Name: "open_iscsi", Level: domain.HealthBlock, Detail: "无法检查：根文件系统缺失"})
		return items
	}
	items = append(items, storage.InspectItem{Name: "linux_rootfs", Level: domain.HealthOK, Detail: "找到 Linux 根文件系统"})

	iscsiFound := false
	for _, candidate := range []string{
		"sbin/iscsistart", "usr/sbin/iscsistart", "usr/bin/iscsistart",
		"sbin/iscsid", "usr/sbin/iscsid",
	} {
		if _, ok := lookupPath(rootFS, candidate); ok {
			iscsiFound = true
			break
		}
	}
	if iscsiFound {
		items = append(items, storage.InspectItem{Name: "open_iscsi", Level: domain.HealthOK, Detail: "open-iscsi 已安装（C-8）"})
	} else {
		items = append(items, storage.InspectItem{Name: "open_iscsi", Level: domain.HealthBlock, Detail: "rootfs 未发现 open-iscsi（iscsistart/iscsid），initramfs 无法挂载 iSCSI 根（C-8）"})
	}
	return append(items, iscsiBootItem(rootFS, mounts))
}

// iscsiBootItem 检查 Ubuntu initramfs-tools 与 RHEL dracut 的实际启动参数。
func iscsiBootItem(rootFS string, mounts []mountedFS) storage.InspectItem {
	roots := []string{rootFS}
	for _, m := range mounts {
		if m.Root != rootFS {
			roots = append(roots, m.Root)
		}
	}
	var configs, kernelopts []string
	for _, root := range roots {
		for _, base := range []string{"boot", "."} {
			for _, pattern := range []string{"grub/grub.cfg", "grub2/grub.cfg", "efi/EFI/*/grub.cfg", "EFI/*/grub.cfg", "loader/entries/*.conf"} {
				paths, _ := filepath.Glob(filepath.Join(root, base, pattern))
				for _, p := range paths {
					if b, err := os.ReadFile(p); err == nil {
						configs = append(configs, string(b))
					}
				}
			}
			for _, pattern := range []string{"grub2/grubenv", "efi/EFI/*/grubenv", "EFI/*/grubenv"} {
				paths, _ := filepath.Glob(filepath.Join(root, base, pattern))
				for _, p := range paths {
					if b, err := os.ReadFile(p); err == nil {
						for _, line := range strings.Split(string(b), "\n") {
							if value, ok := strings.CutPrefix(line, "kernelopts="); ok {
								kernelopts = append(kernelopts, value)
							}
						}
					}
				}
			}
		}
	}
	if len(configs) == 0 {
		return storage.InspectItem{Name: "iscsi_boot", Level: domain.HealthWarn,
			Detail: "请检查镜像的 GRUB 配置：根分区、独立启动分区和 EFI 分区未找到 grub.cfg 或 BLS 启动条目，无法确认 iSCSI 启动参数"}
	}
	for _, cfg := range configs {
		for _, line := range strings.Split(cfg, "\n") {
			fields := strings.Fields(line)
			if comment := slices.Index(fields, "#"); comment >= 0 {
				fields = fields[:comment]
			}
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "linux", "linuxefi", "linux16", "options":
			default:
				continue
			}
			args := strings.Join(fields[1:], " ")
			candidates := []string{args}
			if slices.Contains(fields, "$kernelopts") || slices.Contains(fields, "${kernelopts}") {
				candidates = nil
				for _, opts := range kernelopts {
					candidates = append(candidates, strings.ReplaceAll(strings.ReplaceAll(args, "${kernelopts}", opts), "$kernelopts", opts))
				}
			}
			for _, candidate := range candidates {
				params := strings.Fields(candidate)
				if slices.Contains(params, storage.LinuxKernelParam) {
					return storage.InspectItem{Name: "iscsi_boot", Level: domain.HealthOK, Detail: "启动条目含 iscsi_auto（initramfs-tools iSCSI 启动参数）"}
				}
				if slices.Contains(params, "rd.iscsi.firmware=1") && slices.Contains(params, "rd.neednet=1") {
					return storage.InspectItem{Name: "iscsi_boot", Level: domain.HealthOK, Detail: "启动条目含 rd.iscsi.firmware=1 与 rd.neednet=1（dracut iBFT 启动参数）"}
				}
			}
		}
	}
	return storage.InspectItem{Name: "iscsi_boot", Level: domain.HealthWarn,
		Detail: "请检查镜像的 iSCSI 启动配置：Ubuntu 启动条目需要 iscsi_auto；RHEL 系需要 rd.iscsi.firmware=1 与 rd.neednet=1，当前未找到完整参数"}
}

// lookupPath 不区分大小写地解析以斜杠分隔的相对路径（NTFS 在 Linux 上挂载时大小写不定）。
func lookupPath(root, rel string) (string, bool) {
	current := root
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", false
		}
		found := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				found = filepath.Join(current, e.Name())
				break
			}
		}
		if found == "" {
			return "", false
		}
		current = found
	}
	return current, true
}

// ensureDirFold 是 lookupPath 的写入版：逐级按不区分大小写复用已有目录，缺的才按 rel 的写法新建。
// Windows 卷在 Linux 上（ntfs3）区分大小写；照字面 MkdirAll 会在 WINDOWS\system32 旁边再建一棵
// Windows\System32，Windows 下两者同名，写进去的东西等于没写。
func ensureDirFold(root, rel string) (string, error) {
	current := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" {
			continue
		}
		next := filepath.Join(current, part)
		if entries, err := os.ReadDir(current); err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.EqualFold(e.Name(), part) {
					next = filepath.Join(current, e.Name())
					break
				}
			}
		}
		if err := os.MkdirAll(next, 0o755); err != nil {
			return "", err
		}
		current = next
	}
	return current, nil
}

// fileFold 返回 dir 下与 name 不区分大小写同名的已有文件路径，没有则返回按 name 拼出的路径。
func fileFold(dir, name string) string {
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(e.Name(), name) {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return filepath.Join(dir, name)
}

func aggregateLevel(items []storage.InspectItem) domain.HealthLevel {
	level := domain.HealthOK
	for _, item := range items {
		if domain.HealthSeverity(item.Level) > domain.HealthSeverity(level) {
			level = item.Level
		}
	}
	return level
}
