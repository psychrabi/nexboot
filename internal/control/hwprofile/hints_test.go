package hwprofile

import "testing"

func TestParseBusID(t *testing.T) {
	cases := []struct {
		in, vendor, device string
		ok                 bool
	}{
		{"01:15:ad:07:b0", "15ad", "07b0", true}, // iPXE hex 设置的原样输出：总线类型 + 厂商 + 设备
		{"01:80:86:10:d3", "8086", "10d3", true}, // Intel E1000E
		{"0115AD07B0", "15ad", "07b0", true},     // 去掉冒号也认
		{"8086:10d3", "8086", "10d3", true},      // 只有厂商:设备
		{"", "", "", false},                      // 老 iPXE 不展开变量
		{"${net0/busid}", "", "", false},         // 变量没被展开
		{"07:00:00:00:00", "", "", false},        // 非 PCI 总线不归并
		{"01:15:ad", "", "", false},              // 残缺
	}
	for _, c := range cases {
		vendor, device, ok := ParseBusID(c.in)
		if ok != c.ok || vendor != c.vendor || device != c.device {
			t.Errorf("ParseBusID(%q) = %q %q %v, want %q %q %v", c.in, vendor, device, ok, c.vendor, c.device, c.ok)
		}
	}
}

func TestFormatBusLoc(t *testing.T) {
	cases := map[string]string{
		"1024":  "04:00.0", // bus 4 slot 0 fn 0
		"0":     "00:00.0",
		"139":   "00:11.3",
		"65536": "0001:00:00.0", // 段号非零才显示
		"":      "",
		"abc":   "",
		"${x}":  "",
	}
	for in, want := range cases {
		if got := FormatBusLoc(in); got != want {
			t.Errorf("FormatBusLoc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFingerprintAndDefaultName(t *testing.T) {
	h := Hints{Platform: "efi", BusID: "01:15:ad:07:b0", BusLoc: "1024", Manufacturer: "VMware, Inc.", Product: "VMware Virtual Platform"}
	p, ok := ProfileFromHints(h)
	if !ok {
		t.Fatal("有网卡 ID 就应得到机型")
	}
	if p.Fingerprint != "15ad:07b0/1024/efi" || p.NICVendor != "15ad" || p.NICDevice != "07b0" || p.BusLoc != "1024" || p.Platform != "efi" {
		t.Fatalf("profile = %+v", p)
	}
	if p.Name != "VMware 虚拟机 · VMware vmxnet3" {
		t.Fatalf("name = %q", p.Name)
	}
	// busloc 为空的固件：指纹退化，名字不变。
	h.BusLoc = ""
	p, _ = ProfileFromHints(h)
	if p.Fingerprint != "15ad:07b0//efi" {
		t.Fatalf("fingerprint = %q", p.Fingerprint)
	}
	// 没有厂商型号时名字只有网卡。
	p, _ = ProfileFromHints(Hints{Platform: "pcbios", BusID: "01:80:86:10:d3"})
	if p.Name != "Intel E1000E" || p.Fingerprint != "8086:10d3//pcbios" {
		t.Fatalf("profile = %+v", p)
	}
	if _, ok := ProfileFromHints(Hints{Platform: "efi"}); ok {
		t.Fatal("没有网卡 ID 不能归并")
	}
	p, ok = ProfileFromHints(Hints{Platform: "efi", BusID: "01:80:86:10:d3", Manufacturer: ", ,"})
	if !ok || p.Name != "Intel E1000E" {
		t.Fatalf("无有效厂商字词的机型: %+v", p)
	}
}
