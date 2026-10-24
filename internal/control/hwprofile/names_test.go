package hwprofile

import (
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

// 网卡显示型号名而不是 PCI 编号；认不出的给厂商名加编号，厂商也认不出才只给编号。
func TestNICNameShowsModelNotPCIID(t *testing.T) {
	for _, tc := range []struct{ vendor, device, want string }{
		{"15ad", "07b0", "VMware vmxnet3"},
		{"8086", "100f", "Intel E1000"},
		{"8086", "10d3", "Intel E1000E"},
		{"10ec", "8125", "Realtek RTL8125"},
		{"1af4", "1041", "VirtIO 网卡"},
		{"8086", "abcd", "Intel 网卡 8086:ABCD"},
		{"1234", "5678", "网卡 1234:5678"},
	} {
		if got := NICName(tc.vendor, tc.device); got != tc.want {
			t.Errorf("NICName(%s:%s) = %q, want %q", tc.vendor, tc.device, got, tc.want)
		}
	}
}

func TestDefaultNameReadsLikeAMachine(t *testing.T) {
	for _, tc := range []struct {
		p    domain.HardwareProfile
		want string
	}{
		{domain.HardwareProfile{Manufacturer: "VMware, Inc.", Product: "VMware20,1", NICVendor: "15ad", NICDevice: "07b0"}, "VMware 虚拟机 · VMware vmxnet3"},
		{domain.HardwareProfile{Manufacturer: "ASUS", Product: "PRIME B660M-A", NICVendor: "10ec", NICDevice: "8125"}, "ASUS PRIME B660M-A · Realtek RTL8125"},
		{domain.HardwareProfile{NICVendor: "8086", NICDevice: "100f"}, "Intel E1000"},
	} {
		if got := DefaultName(tc.p); got != tc.want {
			t.Errorf("DefaultName = %q, want %q", got, tc.want)
		}
	}
}

// 已有机型若还是系统按旧规则起的名字，按新规则显示；运维手动改过的保持原样。
func TestDisplayNameUpgradesOnlyAutoNames(t *testing.T) {
	p := domain.HardwareProfile{Manufacturer: "VMware, Inc.", Product: "VMware20,1", NICVendor: "8086", NICDevice: "100f", Name: "VMware20,1 · 网卡 8086:100F"}
	if got := DisplayName(p); got != "VMware 虚拟机 · Intel E1000" {
		t.Fatalf("legacy auto name = %q", got)
	}
	p.Name = "电竞区 E1000"
	if got := DisplayName(p); got != "电竞区 E1000" {
		t.Fatalf("custom name changed: %q", got)
	}
}
