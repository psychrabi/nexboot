package driverinf

import (
	"strings"
	"testing"
)

const nicINF = `
[Version]
Class=Net
Provider=Intel
CatalogFile=e1i.cat

[Manufacturer]
%Intel% = Intel, NTamd64.10.0

[Intel.NTamd64.10.0]
%E10D3.DeviceDesc% = E10D3.10.0.1, PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD
%E100F.DeviceDesc% = E100F.10.0.1, PCI\VEN_8086&DEV_100F

[E10D3.10.0.1.Services]
AddService = e1i68x64,2,e1i68x64.Service

[e1i68x64.Service]
DisplayName = %E10D3.DeviceDesc%
ServiceType = 1
StartType = 3
ErrorControl = 1
ServiceBinary = %13%\e1i68x64.sys

[E100F.10.0.1.Services]
AddService = e1g6032e,,e1g6032e.Service

[e1g6032e.Service]
DisplayName = %E100F.DeviceDesc%
StartType = 3
ServiceBinary = %12%\e1g6032e.sys

[Strings]
Intel = "Intel"
E10D3.DeviceDesc = "Intel(R) 82574L Gigabit Network Connection"
E100F.DeviceDesc = "Intel(R) PRO/1000 MT Network Connection"
`

func TestBindingReadsTheServiceForAVENDEV(t *testing.T) {
	got, err := Binding([]byte(nicINF), `PCI\VEN_8086&DEV_10D3`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "e1i68x64" || got.SysFile != "e1i68x64.sys" || got.Section != "E10D3.10.0.1" || got.StartType != 3 {
		t.Fatalf("%+v", got)
	}
	if got.Description != "Intel(R) 82574L Gigabit Network Connection" {
		t.Fatalf("description %q", got.Description)
	}
}

func TestBindingKeepsADifferentDeviceOnItsOwnService(t *testing.T) {
	got, err := Binding([]byte(nicINF), `PCI\VEN_8086&DEV_100F`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "e1g6032e" || got.SysFile != "e1g6032e.sys" || got.Section != "E100F.10.0.1" {
		t.Fatalf("%+v", got)
	}
}

func TestBindingReadsVMwareServicesDecoratedWithNT(t *testing.T) {
	const inf = `
[Version]
Class=Net
[Manufacturer]
%Company% = vmxnet3,NTamd64.6.2
[vmxnet3.NTamd64.6.2]
%loc.vmxnet3.DeviceDesc% = vmxnet3.ndis630.x64.ndi, PCI\VEN_15AD&DEV_07B0
[vmxnet3.ndis630.x64.ndi.NT.Services]
AddService = vmxnet3ndis6, 2, vmxnet3.ndis6.x64.Service
[vmxnet3.ndis6.x64.Service]
ServiceBinary = %12%\vmxnet3.sys
StartType = 3
[Strings]
Company = "VMware"
loc.vmxnet3.DeviceDesc = "vmxnet3 Ethernet Adapter"
`
	got, err := Binding([]byte(inf), `PCI\VEN_15AD&DEV_07B0`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "vmxnet3ndis6" || got.SysFile != "vmxnet3.sys" || got.Section != "vmxnet3.ndis630.x64.ndi" {
		t.Fatalf("%+v", got)
	}
}

func TestBindingRejectsAnUnknownDeviceAndANonNetFile(t *testing.T) {
	if _, err := Binding([]byte(nicINF), `PCI\VEN_10EC&DEV_8168`); err == nil {
		t.Fatal("unknown device accepted")
	}
	if _, err := Binding([]byte("[Version]\nClass=Display\n[Manufacturer]\nA=M,NTamd64\n[M.NTamd64]\nd=i,PCI\\VEN_8086&DEV_10D3\n"), `PCI\VEN_8086&DEV_10D3`); err == nil {
		t.Fatal("display driver accepted")
	}
}

func TestBindingKeepsTheDeviceNameApartFromTheServiceName(t *testing.T) {
	inf := "[Version]\nClass=Net\n[Manufacturer]\nI=I,NTamd64\n[I.NTamd64]\n%dev%=E100F,PCI\\VEN_8086&DEV_100F\n[E100F.Services]\nAddService=E1G60,2,E1G60.Service\n[E1G60.Service]\nDisplayName=%svc%\nServiceBinary=%12%\\E1G6032E.sys\n[Strings]\ndev=\"Intel(R) PRO/1000 MT Network Connection\"\nsvc=\"Intel(R) PRO/1000 NDIS 6 Adapter Driver\"\n"
	got, err := Binding([]byte(inf), `PCI\VEN_8086&DEV_100F`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "Intel(R) PRO/1000 MT Network Connection" || got.DisplayName != "Intel(R) PRO/1000 NDIS 6 Adapter Driver" {
		t.Fatalf("%+v", got)
	}
}

const e1000INF = `
[version]
Class       = Net
Provider    = %Msft%
DriverVer   = 03/23/2010,8.4.13.0
[Manufacturer]
%Intel%     = Intel, NTamd64
[Intel.NTamd64]
%E100F.DeviceDesc%    = E100FCopper,  PCI\VEN_8086&DEV_100F&SUBSYS_10018086
%E100FGEN.DeviceDesc% = E100F,        PCI\VEN_8086&DEV_100F
[E100F]
Characteristics = 0x84 ; NCF_HAS_UI | NCF_PHYSICAL
BusType         = 5    ; PCI
AddReg          = Kodiak.reg, Advanced.reg
AddReg          = Hw.reg
*IfType         = 6
*MediaType      = 0
*PhysicalMediaType = 14
[E100F.HW]
AddReg = Msi.reg
[E100F.Services]
AddService = E1G60, 2, e1000.Service
[E100FCopper]
Characteristics = 0x84
[E100FCopper.Services]
AddService = E1G60, 2, e1000.Service
[e1000.Service]
DisplayName   = %e1000.Service.DispName%
ServiceBinary = %12%\E1G6032E.sys
StartType     = 3
[Kodiak.reg]
HKR, Ndi\Interfaces, UpperRange, 0, "ndis5"
HKR, Ndi,            Service,    0, "E1G60"
HKR, Ndi\Params\*FlowControl,      ParamDesc, 0, %FlowControl%
HKR, Ndi\Params\*FlowControl,      default,   0, "3"
HKR, Ndi\Params\*FlowControl\Enum, "0",       0, %Disabled%
HKR, Ndi\params\NetworkAddress,    default,   0, ""
HKR, Ndi\params\NetworkAddress,    Optional,  0, "1"
HKR,,                              ITR,       0, "65535"
[Advanced.reg]
HKR,, CustomMessages, 0, "0"
HKR,, Mask, 0x00010001, 0x10
[Hw.reg]
HKLM, Software\Ignored, X, 0, "1"
[Msi.reg]
HKR, Interrupt Management\MessageSignaledInterruptProperties, MSISupported, 0x00010001, 1
[Strings]
Msft = "Microsoft"
Intel = "Intel"
E100F.DeviceDesc    = "Intel(R) PRO/1000 MT Server Adapter"
E100FGEN.DeviceDesc = "Intel(R) PRO/1000 MT Network Connection"
e1000.Service.DispName = "Intel(R) PRO/1000 NDIS 6 Adapter Driver"
FlowControl = "Flow Control"
Disabled = "Disabled"
`

// VMware 的 E1000 子系统号是 075015AD，INF 里只有通用那一行配得上；配给 Intel 原厂卡的 E100FCopper 不能选。
func TestBindingFollowsWindowsRankingOverTheDevicesOwnIDs(t *testing.T) {
	got, err := Binding([]byte(e1000INF),
		`PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01`, `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD`,
		`PCI\VEN_8086&DEV_100F&CC_020000`, `PCI\VEN_8086&DEV_100F&REV_01`, `PCI\VEN_8086&DEV_100F`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Section != "E100F" || got.MatchingID != `PCI\VEN_8086&DEV_100F` || got.Description != "Intel(R) PRO/1000 MT Network Connection" {
		t.Fatalf("%+v", got)
	}
}

func TestBindingReadsWhatWindowsWritesIntoTheDriverKey(t *testing.T) {
	got, err := Binding([]byte(e1000INF), `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01`, `PCI\VEN_8086&DEV_100F`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "Microsoft" || got.DriverDate != "3-23-2010" || got.DriverVersion != "8.4.13.0" || got.Manufacturer != "Intel" {
		t.Fatalf("metadata %+v", got)
	}
	if got.Characteristics != 0x84 || got.BusType != "5" || got.IfType != 6 || got.MediaType != 0 || got.PhysicalMediaType != 14 {
		t.Fatalf("directives %+v", got)
	}
	want := []RegEntry{
		{Key: `Ndi\Interfaces`, Name: "UpperRange", Kind: RegSZ, Strings: []string{"ndis5"}},
		{Key: `Ndi`, Name: "Service", Kind: RegSZ, Strings: []string{"E1G60"}},
		{Key: `Ndi\Params\*FlowControl`, Name: "ParamDesc", Kind: RegSZ, Strings: []string{"Flow Control"}},
		{Key: `Ndi\Params\*FlowControl`, Name: "default", Kind: RegSZ, Strings: []string{"3"}},
		{Key: `Ndi\Params\*FlowControl\Enum`, Name: "0", Kind: RegSZ, Strings: []string{"Disabled"}},
		{Key: `Ndi\params\NetworkAddress`, Name: "default", Kind: RegSZ, Strings: []string{""}},
		{Key: `Ndi\params\NetworkAddress`, Name: "Optional", Kind: RegSZ, Strings: []string{"1"}},
		{Key: ``, Name: "ITR", Kind: RegSZ, Strings: []string{"65535"}},
		{Key: ``, Name: "CustomMessages", Kind: RegSZ, Strings: []string{"0"}},
		{Key: ``, Name: "Mask", Kind: RegDword, Dword: 0x10},
	}
	if len(got.DriverReg) != len(want) {
		t.Fatalf("driver reg %+v", got.DriverReg)
	}
	for i := range want {
		g, w := got.DriverReg[i], want[i]
		if g.Key != w.Key || g.Name != w.Name || g.Kind != w.Kind || g.Dword != w.Dword || strings.Join(g.Strings, "|") != strings.Join(w.Strings, "|") {
			t.Fatalf("entry %d = %+v, want %+v", i, g, w)
		}
	}
	if len(got.DeviceReg) != 1 || got.DeviceReg[0].Key != `Interrupt Management\MessageSignaledInterruptProperties` || got.DeviceReg[0].Kind != RegDword || got.DeviceReg[0].Dword != 1 {
		t.Fatalf("device reg %+v", got.DeviceReg)
	}
}

// Service tokens are used by the Realtek package selected for PCI 10EC:8168 REV_15.
const tokenServiceINF = `
[Version]
Class=Net
[Manufacturer]
Realtek=Realtek,NTamd64
[Realtek.NTamd64]
NIC=RTL8168H.ndi,PCI\VEN_10EC&DEV_8168&REV_15
[RTL8168H.ndi.NT.Services]
AddService=%ServiceName%,2,rtcx21.Service
[rtcx21.Service]
StartType=3
ServiceBinary=%13%\rtcx21x64.sys
[Strings]
ServiceName="rtcx21"
`

func TestBindingResolvesServiceNameStrings(t *testing.T) {
	got, err := Binding([]byte(tokenServiceINF), `PCI\VEN_10EC&DEV_8168&REV_15`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "rtcx21" || got.SysFile != "rtcx21x64.sys" || got.StartType != 3 {
		t.Fatalf("resolved service = %+v", got)
	}
}

func TestBindingResolvesServiceSectionStrings(t *testing.T) {
	inf := strings.Replace(tokenServiceINF, ",2,rtcx21.Service", ",2,%ServiceSection%", 1) + "\nServiceSection=\"rtcx21.Service\"\n"
	got, err := Binding([]byte(inf), `PCI\VEN_10EC&DEV_8168&REV_15`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "rtcx21" || got.SysFile != "rtcx21x64.sys" || got.StartType != 3 {
		t.Fatalf("resolved service section = %+v", got)
	}
}

func TestBindingResolvesServiceBinaryStringsWithSystemDirectories(t *testing.T) {
	for _, directory := range []string{"%12%", "%13%"} {
		t.Run(directory, func(t *testing.T) {
			inf := strings.Replace(tokenServiceINF, `ServiceBinary=%13%\rtcx21x64.sys`, `ServiceBinary=`+directory+`\%DriverFile%`, 1) + "\nDriverFile=\"rtcx21x64.sys\"\n"
			got, err := Binding([]byte(inf), `PCI\VEN_10EC&DEV_8168&REV_15`)
			if err != nil {
				t.Fatal(err)
			}
			if got.Service != "rtcx21" || got.SysFile != "rtcx21x64.sys" {
				t.Fatalf("resolved binary in %s = %+v", directory, got)
			}
		})
	}
}

func TestBindingResolvesQuotedCaseInsensitiveServiceStrings(t *testing.T) {
	inf := strings.Replace(tokenServiceINF, "AddService=%ServiceName%,2,rtcx21.Service", `AddService="%sErViCeNaMe%",2,"%SERVICESECTION%"`, 1)
	inf = strings.Replace(inf, `ServiceBinary=%13%\rtcx21x64.sys`, `ServiceBinary="%12%\%DRIVERFILE%"`, 1)
	inf += "\nServiceSection=\"rtcx21.Service\"\nDriverFile=\"rtcx21x64.sys\"\n"
	got, err := Binding([]byte(inf), `PCI\VEN_10EC&DEV_8168&REV_15`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "rtcx21" || got.SysFile != "rtcx21x64.sys" {
		t.Fatalf("quoted service references = %+v", got)
	}
}

func TestBindingDoesNotInventUnknownServiceStrings(t *testing.T) {
	for _, field := range []string{"service", "section", "binary"} {
		t.Run(field, func(t *testing.T) {
			inf := tokenServiceINF
			switch field {
			case "service":
				inf = strings.Replace(inf, "%ServiceName%", "%UnknownService%", 1)
			case "section":
				inf = strings.Replace(inf, ",2,rtcx21.Service", ",2,%UnknownSection%", 1)
			case "binary":
				inf = strings.Replace(inf, `ServiceBinary=%13%\rtcx21x64.sys`, `ServiceBinary=%13%\%UnknownFile%`, 1)
			}
			got, err := Binding([]byte(inf), `PCI\VEN_10EC&DEV_8168&REV_15`)
			if field == "section" {
				if err == nil || !strings.Contains(err.Error(), "没有 ServiceBinary") {
					t.Fatalf("unknown section was invented: %+v %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if field == "service" && got.Service != "%UnknownService%" || field == "binary" && got.SysFile != "%UnknownFile%" {
				t.Fatalf("unknown token was invented: %+v", got)
			}
		})
	}
}

// NetAdapterCx 的零值也是明确的安装要求，旧式 INF 未声明时不能补默认值。
func TestBindingPreservesDeclaredNetCxProperties(t *testing.T) {
	const fields = `
[E10D3.10.0.1]
*IfConnectorPresent = 1
*ConnectionType = 1
*DirectionType = 0
*AccessType = 2
*HardwareLoopback = 0
`
	want := map[string]uint32{"*IfConnectorPresent": 1, "*ConnectionType": 1, "*DirectionType": 0, "*AccessType": 2, "*HardwareLoopback": 0}
	for _, tc := range []struct {
		name, text string
		declared   bool
	}{
		{"declared", nicINF + fields, true}, {"legacy", nicINF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Binding([]byte(tc.text), `PCI\VEN_8086&DEV_10D3`)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range want {
				var entries []RegEntry
				for _, e := range got.DriverReg {
					if e.Key == "" && strings.EqualFold(e.Name, name) {
						entries = append(entries, e)
					}
				}
				if !tc.declared {
					if len(entries) != 0 {
						t.Errorf("legacy acquired %s: %+v", name, entries)
					}
					continue
				}
				if len(entries) != 1 || entries[0].Kind != RegDword || entries[0].Dword != value {
					t.Errorf("%s = %+v, want DWORD %d", name, entries, value)
				}
			}
		})
	}
}

func TestBindingRejectsInvalidNetCxPropertyRatherThanWritingZero(t *testing.T) {
	for _, value := range []string{"%Undefined%", "not-a-number", "-1", "4294967296", ""} {
		t.Run(value, func(t *testing.T) {
			_, err := Binding([]byte(nicINF+"\n[E10D3.10.0.1]\n*DirectionType="+value+"\n"), `PCI\VEN_8086&DEV_10D3`)
			if err == nil || !strings.Contains(err.Error(), "*DirectionType") {
				t.Fatalf("invalid %q silently accepted: %v", value, err)
			}
		})
	}
}

func TestBindingSeparatesAllServiceAddRegFromDriverAndDeviceKeys(t *testing.T) {
	inf := strings.Replace(nicINF, "ServiceBinary = %13%\\e1i68x64.sys", "ServiceBinary = %13%\\e1i68x64.sys\nAddReg = Boot.reg\nAddReg = Mode.reg, Dma.reg", 1) + `
[Boot.reg]
HKR,,BootFlags,0x00010001,1
[Mode.reg]
HKR,Parameters,DrvOption,0x00010001,0
HKR,Parameters,DrvMode,0x00010001,1
[Dma.reg]
HKR,Parameters,DmaRemappingCompatible,0x00010001,2
`
	b, err := Binding([]byte(inf), `PCI\VEN_8086&DEV_10D3`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uint32{"/BootFlags": 1, "Parameters/DrvOption": 0, "Parameters/DrvMode": 1, "Parameters/DmaRemappingCompatible": 2}
	if len(b.ServiceReg) != len(want) || len(b.DriverReg) != 0 || len(b.DeviceReg) != 0 {
		t.Fatalf("wrong HKR context: %+v", b)
	}
	for _, e := range b.ServiceReg {
		value, ok := want[e.Key+"/"+e.Name]
		if !ok || e.Kind != RegDword || e.Dword != value {
			t.Fatalf("wrong service entry %+v", e)
		}
		delete(want, e.Key+"/"+e.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing AddReg references: %+v", want)
	}
}
