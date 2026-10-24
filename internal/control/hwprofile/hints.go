// Package hwprofile 把客户机开机上报的硬件归并成「机型」，并记录每个还原点在哪些机型上能开机。
package hwprofile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

// Hints 是 iPXE 在 /boot 链接上带来的硬件参数（见 bootURLWithHints）。老 iPXE 不展开变量，各项可能为空或原样。
type Hints struct {
	Platform     string // ${platform}: efi | pcbios
	BusID        string // ${net0/busid}: 总线类型 + PCI 厂商 + 设备，十六进制冒号分隔
	BusLoc       string // ${net0/busloc}: PCI 位置的十进制整数
	Manufacturer string // ${manufacturer}
	Product      string // ${product}
}

const pciBusType = "01"

// ParseBusID 从 busid 取 PCI 厂商与设备 ID（小写 4 位十六进制）。非 PCI 总线或残缺值返回 false。
func ParseBusID(s string) (vendor, device string, ok bool) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if s == "" || strings.ContainsAny(s, "${}") {
		return "", "", false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", "", false
		}
	}
	switch len(s) {
	case 10:
		if s[:2] != pciBusType {
			return "", "", false
		}
		return s[2:6], s[6:10], true
	case 8:
		return s[:4], s[4:8], true
	}
	return "", "", false
}

// FormatBusLoc 把 iPXE 的 busloc 整数显示成 lspci 风格的 "bus:slot.fn"；不是整数时为空。
func FormatBusLoc(s string) string {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return ""
	}
	seg, bus, slot, fn := v>>16, (v>>8)&0xff, (v>>3)&0x1f, v&7
	if seg != 0 {
		return fmt.Sprintf("%04x:%02x:%02x.%d", seg, bus, slot, fn)
	}
	return fmt.Sprintf("%02x:%02x.%d", bus, slot, fn)
}

// ProfileFromHints 按上报参数构造机型（不含 ID 与时间）。没有可用的网卡 ID 时返回 false。
func ProfileFromHints(h Hints) (domain.HardwareProfile, bool) {
	vendor, device, ok := ParseBusID(h.BusID)
	if !ok {
		return domain.HardwareProfile{}, false
	}
	busloc := strings.TrimSpace(h.BusLoc)
	if FormatBusLoc(busloc) == "" {
		busloc = ""
	}
	platform := strings.ToLower(strings.TrimSpace(h.Platform))
	if strings.ContainsAny(platform, "${}") {
		platform = ""
	}
	p := domain.HardwareProfile{
		Fingerprint:  fmt.Sprintf("%s:%s/%s/%s", vendor, device, busloc, platform),
		NICVendor:    vendor,
		NICDevice:    device,
		BusLoc:       busloc,
		Platform:     platform,
		Manufacturer: cleanHint(h.Manufacturer),
		Product:      cleanHint(h.Product),
	}
	p.Name = DefaultName(p)
	return p, true
}

// DefaultName 是机型的默认名字：「机器 · 网卡型号」，如「VMware 虚拟机 · VMware vmxnet3」。
func DefaultName(p domain.HardwareProfile) string {
	nic := NICName(p.NICVendor, p.NICDevice)
	if machine := machineName(p.Manufacturer, p.Product); machine != "" {
		return machine + " · " + nic
	}
	return nic
}

// DisplayName 是机型在界面和告警里的名字：还是系统按旧规则起的就换成新规则，运维改过的不动。
func DisplayName(p domain.HardwareProfile) string {
	if p.Name == "" || p.Name == legacyDefaultName(p) {
		return DefaultName(p)
	}
	return p.Name
}

// legacyDefaultName 是早先自动起的「厂商 型号 · 网卡 VEN:DEV」，只用来认出未改过的旧名字。
func legacyDefaultName(p domain.HardwareProfile) string {
	nic := "网卡 " + NICLabel(p)
	model := p.Product
	if words := strings.FieldsFunc(p.Manufacturer, func(r rune) bool { return r == ' ' || r == ',' }); len(words) > 0 {
		first := strings.ToLower(words[0])
		if !strings.Contains(strings.ToLower(model), first) {
			model = strings.TrimSpace(p.Manufacturer + " " + model)
		}
	}
	if model == "" {
		return nic
	}
	return model + " · " + nic
}

// machineName 把 SMBIOS 厂商与型号写成运维认得的机器名；虚拟机的型号是硬件版本号（如 VMware20,1），换成平台名。
func machineName(manufacturer, product string) string {
	m, pr := strings.ToLower(manufacturer), strings.ToLower(product)
	switch {
	case strings.Contains(m, "vmware"):
		return "VMware 虚拟机"
	case strings.Contains(m, "qemu") || strings.Contains(pr, "kvm"):
		return "QEMU/KVM 虚拟机"
	case strings.Contains(m, "innotek") || strings.Contains(pr, "virtualbox"):
		return "VirtualBox 虚拟机"
	case strings.Contains(m, "microsoft") && strings.Contains(pr, "virtual"):
		return "Hyper-V 虚拟机"
	}
	model := product
	if words := strings.FieldsFunc(manufacturer, func(r rune) bool { return r == ' ' || r == ',' }); len(words) > 0 {
		if !strings.Contains(pr, strings.ToLower(words[0])) {
			model = strings.TrimSpace(manufacturer + " " + model)
		}
	}
	return strings.TrimSpace(model)
}

// 常见启动网卡的型号名；不在表里的显示厂商名加 PCI 编号。
var nicModels = map[string]string{
	"15ad:07b0": "VMware vmxnet3", "15ad:0720": "VMware vmxnet",
	"8086:100f": "Intel E1000", "8086:100e": "Intel PRO/1000 (82540EM)", "8086:10d3": "Intel E1000E",
	"8086:1533": "Intel I210", "8086:1539": "Intel I211", "8086:15b8": "Intel I219-V",
	"8086:15f3": "Intel I225-V", "8086:125c": "Intel I226-V", "8086:125b": "Intel I226-LM",
	"10ec:8168": "Realtek RTL8111/8168", "10ec:8125": "Realtek RTL8125", "10ec:8136": "Realtek RTL8101/8102", "10ec:8139": "Realtek RTL8139",
	"1af4:1000": "VirtIO 网卡", "1af4:1041": "VirtIO 网卡",
}

var nicVendors = map[string]string{
	"8086": "Intel", "10ec": "Realtek", "15ad": "VMware", "14e4": "Broadcom", "1af4": "VirtIO",
	"1d6a": "Aquantia", "1969": "Qualcomm Atheros", "15b3": "Mellanox", "1077": "QLogic",
}

// NICName 是网卡在界面上的名字，如「VMware vmxnet3」「Intel E1000」。
func NICName(vendor, device string) string {
	key := strings.ToLower(vendor) + ":" + strings.ToLower(device)
	if name, ok := nicModels[key]; ok {
		return name
	}
	label := strings.ToUpper(vendor + ":" + device)
	if v, ok := nicVendors[strings.ToLower(vendor)]; ok {
		return v + " 网卡 " + label
	}
	return "网卡 " + label
}

// NICLabel 是界面上的网卡 ID 写法 "15AD:07B0"。
func NICLabel(p domain.HardwareProfile) string {
	return strings.ToUpper(p.NICVendor + ":" + p.NICDevice)
}

func cleanHint(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "${}") {
		return ""
	}
	return s
}
