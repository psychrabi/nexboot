package winreg

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/driverinf"
)

func TestMaterializeRewritesTheInstalledNICForTheTargetDriver(t *testing.T) {
	const (
		oldInstance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
		newInstance = `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`
		oldGUID     = "{AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE}"
		newGUID     = "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}"
		class       = `{4d36e972-e325-11ce-bfc1-08002be10318}`
	)
	donor := "" +
		"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000]\n" +
		"\"DriverDesc\"=\"vmxnet3 Ethernet Adapter\"\n" +
		"\"InfPath\"=\"vmxnet3.inf\"\n" +
		"\"InfSection\"=\"VMXNET3\"\n" +
		"\"MatchingDeviceId\"=\"PCI\\\\VEN_15AD&DEV_07B0\"\n" +
		"\"NetCfgInstanceId\"=\"" + oldGUID + "\"\n" +
		"\"NetLuidIndex\"=dword:00008000\n" +
		"\"DeviceInstanceID\"=hex(1):" + utf16Hex(oldInstance) + "\n" +
		"\"PermanentAddress\"=hex:00,50,56,2e,f6,4e\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000\\LogConf]\n" +
		"\"Config\"=\"drop-me\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\" + oldInstance + "]\n" +
		"\"Service\"=\"vmxnet3ndis6\"\n" +
		"\"Driver\"=\"" + class + "\\\\0000\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\vmxnet3ndis6]\n" +
		"\"Start\"=dword:00000000\n" +
		"\"Type\"=dword:00000001\n" +
		"\"Group\"=\"NDIS\"\n" +
		"\"ImagePath\"=hex(2):" + utf16Hex(`\SystemRoot\System32\drivers\vmxnet3.sys`) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\Tcpip\\Linkage]\n" +
		"\"Bind\"=hex(7):" + formatHex(encodeMulti([]string{`\Device\` + oldGUID})) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\LanmanServer\\Linkage]\n" +
		"\"Bind\"=\"drop-me\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\Tcpip\\Parameters\\Interfaces\\" + oldGUID + "]\n" +
		"\"EnableDHCP\"=dword:00000001\n"
	out, err := materialize(donor, nicTarget{
		ControlSet: "ControlSet002", ClassKey: "0002", Instance: newInstance, GUID: newGUID, MAC: "0050562EF650",
		Service: "e1i68x64", SysFile: "e1i68x64.sys", InfFile: "net1ic64.inf", Section: "E10D3.10.0.1",
		Description: "Intel(R) 82574L Gigabit Network Connection", MatchingID: `PCI\VEN_8086&DEV_10D3`, StartType: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "LogConf") || strings.Contains(out, "LanmanServer") || strings.Contains(out, "vmxnet3ndis6") {
		t.Fatalf("donor-only keys leaked:\n%s", out)
	}
	for _, want := range []string{
		`ControlSet002\Control\Class\` + class + `\0002]`,
		`ControlSet002\Enum\` + newInstance + "]",
		`ControlSet002\Services\e1i68x64]`,
		`ControlSet002\Services\Tcpip\Parameters\Interfaces\` + newGUID + "]",
		`"InfPath"="net1ic64.inf"`,
		`"InfSection"="E10D3.10.0.1"`,
		`"MatchingDeviceId"="PCI\\VEN_8086&DEV_10D3"`,
		`"NetCfgInstanceId"="` + newGUID + `"`,
		`"NetLuidIndex"=dword:00008001`,
		`"Start"=dword:00000000`,
		`ControlSet002\Enum\PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00]`,
		`"BootFlags"=dword:00000001`,
		`"Service"="e1i68x64"`,
		`"Driver"="{4d36e972-e325-11ce-bfc1-08002be10318}\\0002"`,
		"00,50,56,2e,f6,50",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s\n%s", want, out)
		}
	}
	parent := `ControlSet002\Enum\PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00]`
	instanceKey := `ControlSet002\Enum\` + newInstance + "]"
	if strings.Index(out, parent) > strings.Index(out, instanceKey) {
		t.Fatal("enum parent key must be created before the device instance")
	}
	keys := parseReg(out)
	var bind string
	var image string
	var instanceID string
	for _, k := range keys {
		if strings.HasSuffix(k.Path, `\Tcpip\Linkage`) {
			bind = k.get("Bind")
		}
		if strings.HasSuffix(k.Path, `\Services\e1i68x64`) {
			image = k.get("ImagePath")
		}
		if strings.HasSuffix(k.Path, `\0002`) {
			instanceID = k.get("DeviceInstanceID")
		}
	}
	elems := decodeHexMulti(t, bind)
	if !containsAll(elems, `\Device\`+oldGUID, `\Device\`+newGUID) {
		t.Fatalf("linkage = %v", elems)
	}
	if got := decodeRegString(instanceID); got != newInstance {
		t.Fatalf("instance %q", got)
	}
	if got := decodeRegString(image); !strings.HasSuffix(got, `\e1i68x64.sys`) {
		t.Fatalf("image path %q", got)
	}
}

func TestMaterializeRetargetsClassLinkageToTheNewNIC(t *testing.T) {
	const (
		oldInstance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
		newInstance = `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`
		oldGUID     = "{AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE}"
		newGUID     = "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}"
		class       = `{4d36e972-e325-11ce-bfc1-08002be10318}`
	)
	donor := "" +
		"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000]\n" +
		"\"DriverDesc\"=\"vmxnet3 Ethernet Adapter\"\n" +
		"\"NetCfgInstanceId\"=\"" + oldGUID + "\"\n" +
		"\"DeviceInstanceID\"=hex(1):" + utf16Hex(oldInstance) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000\\Linkage]\n" +
		"\"Bind\"=hex(7):" + formatHex(encodeMulti([]string{`\Device\` + oldGUID, oldGUID})) + "\n" +
		"\"RootDevice\"=\"" + oldGUID + "\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\" + oldInstance + "]\n" +
		"\"Service\"=\"vmxnet3ndis6\"\n" +
		"\"Driver\"=\"" + class + "\\\\0000\"\n" +
		"\"ContainerID\"=\"" + oldGUID + "\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\vmxnet3ndis6]\n" +
		"\"Start\"=dword:00000000\n" +
		"\"ImagePath\"=hex(2):" + utf16Hex(`\SystemRoot\System32\drivers\vmxnet3.sys`) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\vmxnet3ndis6\\Parameters]\n" +
		"\"NetCfgInstanceId\"=\"" + oldGUID + "\"\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\Tcpip\\Linkage]\n" +
		"\"Bind\"=hex(7):" + formatHex(encodeMulti([]string{`\Device\` + oldGUID})) + "\n"
	out, err := materialize(donor, nicTarget{
		ControlSet: "ControlSet001", ClassKey: "0002", Instance: newInstance, GUID: newGUID, MAC: "0050562EF650",
		Service: "e1i68x64", SysFile: "e1i68x64.sys", InfFile: "net1ic64.inf", Section: "E10D3",
		Description: "Intel 82574L", MatchingID: `PCI\VEN_8086&DEV_10D3`, StartType: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := parseReg(out)
	var classBind, classRoot, enumContainer, serviceParam, tcpip string
	for _, k := range keys {
		switch {
		case strings.HasSuffix(k.Path, `\0002\Linkage`):
			classBind = k.get("Bind")
			classRoot = k.get("RootDevice")
		case strings.HasSuffix(k.Path, `\Enum\`+newInstance):
			enumContainer = k.get("ContainerID")
		case strings.HasSuffix(k.Path, `\Services\e1i68x64\Parameters`):
			serviceParam = k.get("NetCfgInstanceId")
		case strings.HasSuffix(k.Path, `\Tcpip\Linkage`):
			tcpip = k.get("Bind")
		}
	}
	bind := decodeHexMulti(t, classBind)
	if len(bind) != 2 || bind[0] != `\Device\`+newGUID || bind[1] != newGUID {
		t.Fatalf("class linkage Bind = %v", bind)
	}
	if unquoteReg(classRoot) != newGUID {
		t.Fatalf("class RootDevice = %s", classRoot)
	}
	if enumContainer != "" {
		t.Fatalf("enum ContainerID = %s", enumContainer)
	}
	if unquoteReg(serviceParam) != newGUID {
		t.Fatalf("service NetCfgInstanceId = %s", serviceParam)
	}
	shared := decodeHexMulti(t, tcpip)
	if !containsAll(shared, `\Device\`+oldGUID, `\Device\`+newGUID) {
		t.Fatalf("shared Tcpip linkage = %v", shared)
	}
}

func TestMaterializeSkipsWhenTheInstalledServiceIsMissing(t *testing.T) {
	const (
		oldInstance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
		oldGUID     = "{AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE}"
		class       = `{4d36e972-e325-11ce-bfc1-08002be10318}`
	)
	donor := "" +
		"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000]\n" +
		"\"NetCfgInstanceId\"=\"" + oldGUID + "\"\n" +
		"\"DeviceInstanceID\"=hex(1):" + utf16Hex(oldInstance) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\" + oldInstance + "]\n" +
		"\"Service\"=\"vmxnet3ndis6\"\n"
	_, err := materialize(donor, nicTarget{
		ControlSet: "ControlSet001", ClassKey: "0002",
		Instance: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`,
		GUID:     "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}", MAC: "0050562EF650",
		Service: "e1i68x64", SysFile: "e1i68x64.sys", InfFile: "net1ic64.inf", Section: "E10D3",
		Description: "Intel 82574L", MatchingID: `PCI\VEN_8086&DEV_10D3`, StartType: 3,
	})
	if !IsSkipTemplate(err) {
		t.Fatalf("missing service: %v", err)
	}
}

func TestApplyGeneratedCopiesPublishedInboxDriverThenRefusesABadHive(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", "net1ic64.inf_amd64_test")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	inf := "[Version]\nClass=Net\n[Manufacturer]\nIntel=Intel,NTamd64\n[Intel.NTamd64]\n%desc%=E10D3,PCI\\VEN_8086&DEV_10D3\n[E10D3.Services]\nAddService=e1i68x64,,e1i68x64.Service\n[e1i68x64.Service]\nServiceBinary=%12%\\e1i68x64.sys\nStartType=3\nDisplayName=%desc%\n[Strings]\ndesc=\"Intel 82574L\"\n"
	if err := os.WriteFile(filepath.Join(pkg, "net1ic64.inf"), []byte(inf), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Windows", "INF"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Windows", "INF", "net1ic64.inf"), []byte(inf), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "e1i68x64.sys"), testDriverPE(""), 0600); err != nil {
		t.Fatal(err)
	}
	hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
	if err := os.MkdirAll(filepath.Dir(hive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hive, []byte("not-a-hive"), 0600); err != nil {
		t.Fatal(err)
	}
	err := ApplyGenerated(context.Background(), root, hive, GenerateSpec{
		Instance: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`,
		GUID:     "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}", MAC: "0050562EF650", Vendor: "8086", Device: "10d3",
	})
	if err == nil || IsSkipTemplate(err) {
		t.Fatalf("expected a hive error, got %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "Windows", "System32", "drivers", "e1i68x64.sys"))
	if err != nil || !bytes.Equal(got, testDriverPE("")) {
		t.Fatalf("sys copy %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", "ndiskless_net1ic64_amd64")); !os.IsNotExist(err) {
		t.Fatalf("inbox driver was copied again: %v", err)
	}
	// A repository can contain several staged versions; keep the image's known installed service binary.
	driver := filepath.Join(root, "Windows", "System32", "drivers", "e1i68x64.sys")
	if err := os.WriteFile(driver, testDriverPE("Ucx"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = ApplyGenerated(context.Background(), root, hive, GenerateSpec{Instance: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`, GUID: "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}", MAC: "0050562EF650", Vendor: "8086", Device: "10d3"})
	got, err = os.ReadFile(driver)
	if err != nil || !bytes.Equal(got, testDriverPE("Ucx")) {
		t.Fatalf("installed binary overwritten: %s %v", got, err)
	}

}

func TestApplyGeneratedSkipsUnstagedUploadedPackageWithoutWrites(t *testing.T) {
	root := t.TempDir()
	hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
	if err := os.MkdirAll(filepath.Dir(hive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hive, []byte("not-a-hive"), 0600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "rt.zip")
	writeDriverZip(t, zipPath, "rt640x64.inf", "rt640x64.sys", "uploaded-sys")
	spec := GenerateSpec{Instance: `PCI\VEN_10EC&DEV_8168&SUBSYS_012310EC&REV_15\4&1&0&00E0`, GUID: "{7C1E0A22-50F6-4E00-8A10-D3E1000E0001}", MAC: "001122334455", Vendor: "10ec", Device: "8168", DriverZip: zipPath}
	if err := ApplyGenerated(context.Background(), root, hive, spec); !IsSkipTemplate(err) {
		t.Fatalf("unstaged package must require WinPE adaptation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Windows", "System32", "drivers")); !os.IsNotExist(err) {
		t.Fatalf("skip wrote driver files: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(hive))
	if err != nil || len(entries) != 1 {
		t.Fatalf("skip wrote hive files: %v %v", entries, err)
	}
}

func writeDriverZip(t *testing.T, path, infName, sysName, sysBody string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	inf := "[Version]\nClass=Net\n[Manufacturer]\nR=R,NTamd64\n[R.NTamd64]\n%desc%=Install,PCI\\VEN_10EC&DEV_8168\n[Install.Services]\nAddService=rt640x64,,rt640x64.Service\n[rt640x64.Service]\nServiceBinary=%12%\\" + sysName + "\nStartType=3\nDisplayName=%desc%\n[Strings]\ndesc=\"Realtek PCIe GbE\"\n"
	iw, err := w.Create(infName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iw.Write([]byte(inf)); err != nil {
		t.Fatal(err)
	}
	sw, err := w.Create(sysName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write([]byte(sysBody)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentifyDonorReadsTheHexStringsHivexExports(t *testing.T) {
	const (
		instance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
		guid     = "{4ED073C7-D4E5-4E93-AD77-BD2AD624389B}"
		class    = `{4d36e972-e325-11ce-bfc1-08002be10318}`
	)
	text := "" +
		"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0000]\n" +
		"\"DriverDesc\"=hex(1):" + utf16Hex("kdnic") + "\n" +
		"\"NetCfgInstanceId\"=hex(1):" + utf16Hex("{11111111-2222-3333-4444-555555555555}") + "\n" +
		"\"DeviceInstanceID\"=hex(1):" + utf16Hex(`PCI\VEN_8086&DEV_10D3&SUBSYS_00000000&REV_00\KDNIC`) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\" + class + "\\0002]\n" +
		"\"DriverDesc\"=hex(1):" + utf16Hex("vmxnet3 Ethernet Adapter") + "\n" +
		"\"NetCfgInstanceId\"=hex(1):" + utf16Hex(guid) + "\n" +
		"\"DeviceInstanceID\"=hex(1):" + utf16Hex(instance) + "\n" +
		"\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\" + instance + "]\n" +
		"\"Service\"=hex(1):" + utf16Hex("vmxnet3ndis6") + "\n"
	keys := parseReg(text)
	got, err := identifyDonor(keys)
	if err != nil {
		t.Fatal(err)
	}
	if got.GUID != guid || got.Instance != instance || got.ClassKey != "0002" {
		t.Fatalf("%+v", got)
	}
	service := ""
	for _, k := range keys {
		if strings.EqualFold(k.Path, prefix+`\ControlSet001\Enum\`+got.Instance) {
			service = hiveString(k.get("Service"))
		}
	}
	if service != "vmxnet3ndis6" {
		t.Fatalf("service = %q", service)
	}
}

func decodeHexMulti(t *testing.T, raw string) []string {
	t.Helper()
	_, body, ok := splitHex(raw)
	if !ok {
		t.Fatalf("not hex: %s", raw)
	}
	b, err := parseHexBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	return decodeMulti(b)
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// 样本取自 Server 2022 镜像里装好的 vmxnet3，期望值对照同一镜像里 Windows 自己装的 E1000E 和 .4 上真机进过桌面的原型。
func TestMaterializeDropsWhatOnlyBelongsToTheDonorNIC(t *testing.T) {
	const (
		cs          = `HKEY_LOCAL_MACHINE\SYSTEM\ControlSet001\`
		class       = `Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}\`
		oldInstance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
		newInstance = `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`
		oldGUID     = "{4ED073C7-D4E5-4E93-AD77-BD2AD624389B}"
		newGUID     = "{02D6E127-8926-01BA-C43B-CDFC55636E52}"
		oldMAC      = "00,50,56,2e,f6,4e"
	)
	key := func(path string, values ...string) string {
		return "[" + cs + path + "]\n" + strings.Join(values, "\n") + "\n\n"
	}
	sz := func(name, value string) string { return `"` + name + `"=hex(1):` + utf16Hex(value) }
	multi := func(name string, values ...string) string {
		return `"` + name + `"=hex(7):` + formatHex(encodeMulti(values))
	}
	prop := func(value string) string { return `@=hex(ffff0012):` + utf16Hex(value) }
	donor := key(class+"0000",
		sz("DriverDesc", "vmxnet3 以太网适配器"), sz("InfPath", "oem0.inf"), sz("InfSection", "vmxnet3.ndi"),
		sz("MatchingDeviceId", `PCI\VEN_15AD&DEV_07B0`), sz("ComponentId", `PCI\VEN_15AD&DEV_07B0`),
		sz("NetCfgInstanceId", oldGUID), `"NetLuidIndex"=dword:00008000`, sz("DeviceInstanceID", oldInstance)) +
		key(class+`0000\Ndi`, sz("Service", "vmxnet3ndis6")) +
		key(class+"0001", sz("DriverDesc", "Microsoft Kernel Debug Network Adapter"), sz("DeviceInstanceID", `ROOT\KDNIC\0000`), `"NetLuidIndex"=dword:00008001`) +
		key(class+"0005", sz("DriverDesc", "WAN Miniport (IP)"), sz("DeviceInstanceID", `SWD\MSRRAS\MS_NDISWANIP`), `"NetLuidIndex"=dword:00008006`) +
		key(`Enum\`+oldInstance,
			multi("HardwareID", `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01`, `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD`),
			multi("CompatibleIDs", `PCI\VEN_15AD&DEV_07B0&REV_01`, `PCI\VEN_15AD&DEV_07B0`, `PCI\VEN_15AD`),
			sz("DeviceDesc", "@oem0.inf,%loc.vmxnet3.devicedesc%;vmxnet3 以太网适配器"), sz("FriendlyName", "vmxnet3 以太网适配器"),
			sz("Mfg", "@oem0.inf,%vmware%;VMware, Inc."), sz("Service", "vmxnet3ndis6"),
			`"Driver"="{4d36e972-e325-11ce-bfc1-08002be10318}\\0000"`, `"ConfigFlags"=dword:00000000`) +
		key(`Enum\`+oldInstance+`\Properties\{a8b865dd-2e3d-4094-ad97-e593a70c75d6}\0005`, prop("oem0.inf")) +
		key(`Services\vmxnet3ndis6`, `"Start"=dword:00000000`, `"Type"=dword:00000001`, sz("Group", "NDIS"),
			multi("Owners", "oem0.inf"), multi("vwdk.installers", "Windows"),
			`"ImagePath"=hex(2):`+utf16Hex(`\SystemRoot\System32\drivers\vmxnet3.sys`)) +
		key(`Control\NetworkSetup2\Interfaces\`+oldGUID+`\Kernel`, `"NetLuidIndex"=dword:00008000`,
			`"CurrentAddress"=hex(3):`+oldMAC, `"PermanentAddress"=hex(3):`+oldMAC, sz("IfDescr", "vmxnet3 以太网适配器")) +
		key(`Control\NetworkSetup2\Interfaces\`+oldGUID+`\Properties\{a111f1f0-5923-47c0-9a68-d0bafb577901}\000a`, prop("vmxnet3ndis6")) +
		key(`Control\NetworkSetup2\Interfaces\`+oldGUID+`\Properties\{a111f1f4-5923-47c0-9a68-d0bafb577901}\0004`, `@=hex(ffff0009):00,00,00,00,80,00,06,00`) +
		key(`Services\Tcpip\Parameters\Interfaces\`+oldGUID, `"EnableDHCP"=dword:00000001`, sz("Domain", ""),
			sz("DhcpIPAddress", "192.168.10.10"), sz("DhcpServer", "192.168.10.4"), `"Lease"=dword:0000a8c0`, `"T1"=dword:6ad0db2d`)
	out, err := materialize(donor, nicTarget{
		ControlSet: "ControlSet001", ClassKey: "0011", Instance: newInstance, GUID: newGUID, MAC: "0050562EF650",
		Service: "e1i68x64", SysFile: "e1i68x64.sys", InfFile: "net1ix64.inf", Section: "E10D3.10.0.1",
		Description: "Intel(R) 82574L Gigabit Network Connection", DisplayName: "Intel(R) PRO/1000 PCI Express Network Connection Driver I",
		MatchingID: `PCI\VEN_8086&DEV_10D3`, StartType: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]regKey{}
	for _, k := range parseReg(out) {
		keys[strings.ToLower(strings.TrimPrefix(k.Path, cs))] = k
	}
	get := func(path, name string) string {
		k, ok := keys[strings.ToLower(path)]
		if !ok {
			t.Fatalf("missing key %s\n%s", path, out)
		}
		return k.get(name)
	}
	str := func(path, name string) string { return decodeRegString(get(path, name)) }
	propStr := func(path string) string { return strings.TrimRight(decodeUTF16(hexValue(get(path, "@"))), "\x00") }
	list := func(path, name string) []string { return decodeHexMulti(t, get(path, name)) }
	enum := `Enum\` + newInstance
	if hw := list(enum, "HardwareID"); len(hw) != 4 || hw[0] != `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00` || hw[3] != `PCI\VEN_8086&DEV_10D3&CC_0200` {
		t.Fatalf("HardwareID %v", hw)
	}
	if c := list(enum, "CompatibleIDs"); len(c) < 2 || c[0] != `PCI\VEN_8086&DEV_10D3&REV_00` || c[1] != `PCI\VEN_8086&DEV_10D3` {
		t.Fatalf("CompatibleIDs %v", c)
	}
	if got := str(enum, "DeviceDesc"); got != "Intel(R) 82574L Gigabit Network Connection" {
		t.Fatalf("DeviceDesc %q", got)
	}
	if got := propStr(enum + `\Properties\{a8b865dd-2e3d-4094-ad97-e593a70c75d6}\0005`); got != "net1ix64.inf" {
		t.Fatalf("driver inf property %q", got)
	}
	if got := str(class+"0011", "ComponentId"); got != `PCI\VEN_8086&DEV_10D3` {
		t.Fatalf("ComponentId %q", got)
	}
	if got := str(class+"0011", "DriverDesc"); got != "Intel(R) 82574L Gigabit Network Connection" {
		t.Fatalf("DriverDesc %q", got)
	}
	if got := str(class+`0011\Ndi`, "Service"); got != "e1i68x64" {
		t.Fatalf("Ndi Service %q", got)
	}
	// 新编号取现有最大值加一，类实例和 NetworkSetup2 里写同一个。
	kernel := `Control\NetworkSetup2\Interfaces\` + newGUID + `\Kernel`
	if a, b := get(class+"0011", "NetLuidIndex"), get(kernel, "NetLuidIndex"); a != "dword:00008007" || b != a {
		t.Fatalf("NetLuidIndex class %s kernel %s", a, b)
	}
	if got := get(`Control\NetworkSetup2\Interfaces\`+newGUID+`\Properties\{a111f1f4-5923-47c0-9a68-d0bafb577901}\0004`, "@"); got != "hex(ffff0009):00,00,00,07,80,00,06,00" {
		t.Fatalf("interface luid %s", got)
	}
	if got := propStr(`Control\NetworkSetup2\Interfaces\` + newGUID + `\Properties\{a111f1f0-5923-47c0-9a68-d0bafb577901}\000a`); got != "e1i68x64" {
		t.Fatalf("interface service %q", got)
	}
	if got := get(kernel, "PermanentAddress"); got != "hex(3):00,50,56,2e,f6,50" {
		t.Fatalf("kernel mac %s", got)
	}
	if got := str(kernel, "IfDescr"); got != "Intel(R) 82574L Gigabit Network Connection" {
		t.Fatalf("IfDescr %q", got)
	}
	svc := `Services\e1i68x64`
	if o := list(svc, "Owners"); len(o) != 1 || o[0] != "net1ix64.inf" {
		t.Fatalf("Owners %v", o)
	}
	if got := str(svc, "DisplayName"); got != "Intel(R) PRO/1000 PCI Express Network Connection Driver I" {
		t.Fatalf("DisplayName %q", got)
	}
	tcpip := `Services\Tcpip\Parameters\Interfaces\` + newGUID
	if get(tcpip, "EnableDHCP") != "dword:00000001" || get(tcpip, "DhcpIPAddress") != "" || get(tcpip, "Lease") != "" || get(tcpip, "T1") != "" {
		t.Fatalf("tcpip interface keeps the donor lease:\n%v", keys[strings.ToLower(tcpip)])
	}
	lower := strings.ToLower(out)
	for _, leak := range []string{"vmxnet3ndis6", "oem0.inf", "vwdk.installers", oldMAC, strings.ToLower(`005056FFFF2EF64E00`), "vmxnet3"} {
		if strings.Contains(lower, strings.ToLower(leak)) || strings.Contains(lower, strings.ToLower(utf16Hex(leak)[:len(utf16Hex(leak))-6])) {
			t.Fatalf("donor value %q leaked:\n%s", leak, out)
		}
	}
}

// 在装有 hivexregedit 的节点上拿真实镜像跑一遍现写模板。
// NEXBOOT_TEST_SYSTEM_HIVE 是镜像里的 SYSTEM，NEXBOOT_TEST_DRIVER_DIR 放目标网卡的 INF 与 sys，
// NEXBOOT_TEST_INSTANCE 是目标设备实例；NEXBOOT_TEST_PRESENT_INSTANCE 可选，是镜像里已装好的那块。
func TestApplyGeneratedOnARealHive(t *testing.T) {
	if _, err := exec.LookPath("hivexregedit"); err != nil {
		t.Skip("hivexregedit not installed")
	}
	source, drivers, instance := os.Getenv("NEXBOOT_TEST_SYSTEM_HIVE"), os.Getenv("NEXBOOT_TEST_DRIVER_DIR"), os.Getenv("NEXBOOT_TEST_INSTANCE")
	published := os.Getenv("NEXBOOT_TEST_PUBLISHED_INF")
	if source == "" || drivers == "" || instance == "" || published == "" {
		t.Skip("set NEXBOOT_TEST_SYSTEM_HIVE, NEXBOOT_TEST_DRIVER_DIR, NEXBOOT_TEST_INSTANCE and NEXBOOT_TEST_PUBLISHED_INF from the same Windows image")
	}
	ctx := context.Background()
	root := t.TempDir()
	hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
	pkg := filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", "target.inf_amd64_test")
	publishedDir := filepath.Join(root, "Windows", "INF")
	for _, dir := range []string{filepath.Dir(hive), pkg, publishedDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	copyFile := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	copyFile(source, hive)
	copyFile(published, filepath.Join(publishedDir, filepath.Base(published)))
	entries, err := os.ReadDir(drivers)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		copyFile(filepath.Join(drivers, e.Name()), filepath.Join(pkg, e.Name()))
	}
	spec := func(inst string) GenerateSpec {
		m := regexp.MustCompile(`(?i)VEN_([0-9a-f]{4})&DEV_([0-9a-f]{4})`).FindStringSubmatch(inst)
		return GenerateSpec{Instance: inst, GUID: "{02D6E127-8926-01BA-C43B-CDFC55636E52}", MAC: "0050562EF65F", Vendor: m[1], Device: m[2]}
	}
	if present := os.Getenv("NEXBOOT_TEST_PRESENT_INSTANCE"); present != "" {
		if err := ApplyGenerated(ctx, root, hive, spec(present)); !IsInstancePresent(err) {
			t.Fatalf("installed nic: %v", err)
		}
	}
	if err := ApplyGenerated(ctx, root, hive, spec(instance)); err != nil {
		t.Fatal(err)
	}
	cs, err := controlSet(ctx, hive)
	if err != nil {
		t.Fatal(err)
	}
	enum, err := export(ctx, hive, cs+`\Enum\`+instance)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(enum[:min(len(enum), 4000)])
	if err := ApplyGenerated(ctx, root, hive, spec(instance)); !IsInstancePresent(err) {
		t.Fatalf("written nic must count as installed next time: %v", err)
	}
	if out := os.Getenv("NEXBOOT_TEST_OUT"); out != "" {
		copyFile(hive, out)
	}
}

func locationProp(paths ...string) string {
	return `@=hex(ffff2012):` + formatHex(encodeMulti(paths))
}

// 同一父设备下其他 PCI 设备的位置路径能验证实例号末段的换算，验证通过才推出这块网卡的位置。
func TestDeriveLocationPathFromSiblingsInTheImage(t *testing.T) {
	const cs = `HKEY_LOCAL_MACHINE\SYSTEM\ControlSet001\Enum\PCI\`
	sibling := func(dev, serial string, paths ...string) string {
		return "[" + cs + dev + `\` + serial + `\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025]` + "\n" + locationProp(paths...) + "\n\n"
	}
	enum := sibling(`VEN_15AD&DEV_0774&SUBSYS_197615AD&REV_00`, `4&bbf9765&0&0088`, `PCIROOT(0)#PCI(1100)#PCI(0000)`, `ACPI(_SB_)#ACPI(PCI0)#ACPI(P2P0)#PCI(0000)`) +
		sibling(`VEN_15AD&DEV_0770&SUBSYS_077015AD&REV_00`, `4&bbf9765&0&1088`, `PCIROOT(0)#PCI(1100)#PCI(0200)`) +
		sibling(`VEN_15AD&DEV_07A0&SUBSYS_07A015AD&REV_01`, `3&61aaa01&0&A8`, `PCIROOT(0)#PCI(1500)`) +
		sibling(`VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01`, `005056FFFF2EF64E00`, `PCIROOT(0)#PCI(1500)#PCI(0000)`)
	if got := deriveLocationPath(enum, `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`); got != `PCIROOT(0)#PCI(1100)#PCI(0400)` {
		t.Fatalf("location %q", got)
	}
	if got := deriveLocationPath(enum, `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`); got != "" {
		t.Fatalf("mac-numbered instance has no slot to derive: %q", got)
	}
	broken := enum + sibling(`VEN_15AD&DEV_1977&SUBSYS_197715AD&REV_09`, `4&bbf9765&0&0888`, `PCIROOT(0)#PCI(1100)#PCI(0300)`)
	if got := deriveLocationPath(broken, `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`); got != "" {
		t.Fatalf("a sibling that disagrees must stop the guess: %q", got)
	}
	if got := deriveLocationPath(enum, `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&deadbeef&0&2088`); got != "" {
		t.Fatalf("no sibling to check against: %q", got)
	}
}

const crossDonor = "" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0000]\n" +
	"\"DriverDesc\"=\"vmxnet3 Ethernet Adapter\"\n\"InfPath\"=\"oem0.inf\"\n\"MatchingDeviceId\"=\"PCI\\\\VEN_15AD&DEV_07B0\"\n" +
	"\"NetCfgInstanceId\"=\"{4ED073C7-D4E5-4E93-AD77-BD2AD624389B}\"\n\"NetLuidIndex\"=dword:00008000\n\"MaxTxQueues\"=\"8\"\n" +
	"\"DeviceInstanceID\"=\"PCI\\\\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\\\\005056FFFF2EF64E00\"\n" +
	"\"InstallTimeStamp\"=hex(3):ea,07,0a,00\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0000\\Ndi\\params\\MaxTxQueues]\n\"default\"=\"8\"\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0000\\Linkage]\n" +
	"\"UpperBind\"=hex(7):" + "54,00,63,00,70,00,69,00,70,00,00,00,00,00" + "\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\PCI\\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\\005056FFFF2EF64E00]\n" +
	"\"Service\"=\"vmxnet3ndis6\"\n\"Address\"=dword:00000000\n\"UINumber\"=dword:000000a0\n\"Capabilities\"=dword:00000016\n\"ConfigFlags\"=dword:00000000\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\PCI\\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\\005056FFFF2EF64E00\\Control]\n\"AllocConfig\"=hex(8):01,00\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Enum\\PCI\\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\\005056FFFF2EF64E00\\Device Parameters\\Interrupt Management\\MessageSignaledInterruptProperties]\n\"MessageNumberLimit\"=dword:0000000a\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Services\\vmxnet3ndis6]\n\"Start\"=dword:00000000\n\"Group\"=\"NDIS\"\n\"Type\"=dword:00000001\n\n"

const crossINF = `
[version]
Class = Net
Provider = %Msft%
DriverVer = 03/23/2010,8.4.13.0
[Manufacturer]
%Intel% = Intel, NTamd64
[Intel.NTamd64]
%E100FGEN.DeviceDesc% = E100F, PCI\VEN_8086&DEV_100F
[E100F]
Characteristics = 0x84
BusType = 5
AddReg = Kodiak.reg
*IfType = 6
*MediaType = 0
*PhysicalMediaType = 14
[E100F.HW]
AddReg = Msi.reg
[E100F.Services]
AddService = E1G60, 2, e1000.Service
[e1000.Service]
ServiceBinary = %12%\E1G6032E.sys
[Kodiak.reg]
HKR, Ndi\Interfaces, UpperRange, 0, "ndis5"
HKR, Ndi, Service, 0, "E1G60"
HKR, Ndi\Params\*FlowControl, default, 0, "3"
HKR, Ndi\params\NetworkAddress, default, 0, ""
HKR, Ndi\params\NetworkAddress, Optional, 0, "1"
HKR,, ITR, 0, "65535"
[Msi.reg]
HKR, Interrupt Management\MessageSignaledInterruptProperties, MSISupported, 0x00010001, 0
[Strings]
Msft = "Microsoft"
Intel = "Intel"
E100FGEN.DeviceDesc = "Intel(R) PRO/1000 MT Network Connection"
`

func crossTarget(t *testing.T) nicTarget {
	t.Helper()
	inst := `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`
	hw, compat := pciIDs(inst)
	b, err := driverinf.Binding([]byte(crossINF), append(hw, compat...)...)
	if err != nil {
		t.Fatal(err)
	}
	return nicTarget{ControlSet: "ControlSet001", ClassKey: "0011", Instance: inst, GUID: "{02D6E127-8926-01BA-C43B-CDFC55636E52}", MAC: "0050562EF65F",
		Service: b.Service, SysFile: b.SysFile, InfFile: "nete1g3e.inf", Section: b.Section, Description: b.Description, DisplayName: b.DisplayName,
		MatchingID: b.MatchingID, StartType: b.StartType, INF: b, LocationPath: `PCIROOT(0)#PCI(1100)#PCI(0400)`}
}

// 跨型号时驱动键按目标 INF 写（和 Windows 装驱动时一样），样本网卡的参数、插槽资源、中断设置都不带过去。
func TestMaterializeAcrossModelsWritesTheTargetDriverKey(t *testing.T) {
	out, err := materialize(crossDonor, crossTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]regKey{}
	for _, k := range parseReg(out) {
		keys[strings.ToLower(strings.TrimPrefix(k.Path, `HKEY_LOCAL_MACHINE\SYSTEM\ControlSet001\`))] = k
	}
	has := func(path string) (regKey, bool) { k, ok := keys[strings.ToLower(path)]; return k, ok }
	class := `Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}\0011`
	root, ok := has(class)
	if !ok {
		t.Fatalf("no driver key\n%s", out)
	}
	for name, want := range map[string]string{"ITR": `"65535"`, "*FlowControl": `"3"`, "InfSection": `"E100F"`, "MatchingDeviceId": `"PCI\\VEN_8086&DEV_100F"`,
		"ProviderName": `"Microsoft"`, "DriverVersion": `"8.4.13.0"`, "Characteristics": "dword:00000084", "BusType": `"5"`, "*PhysicalMediaType": "dword:0000000e",
		"InstallTimeStamp": "hex(3):ea,07,0a,00"} {
		if got := root.get(name); got != want {
			t.Fatalf("%s = %s, want %s", name, got, want)
		}
	}
	if root.get("NetworkAddress") != "" || root.get("MaxTxQueues") != "" {
		t.Fatalf("optional or donor-only parameters written: %+v", root.Values)
	}
	if k, ok := has(class + `\Ndi`); !ok || k.get("Service") != `"E1G60"` {
		t.Fatalf("Ndi %+v", k)
	}
	if _, ok := has(class + `\Ndi\params\MaxTxQueues`); ok {
		t.Fatal("donor parameter definitions copied")
	}
	if k, ok := has(class + `\Linkage`); !ok || k.get("UpperBind") == "" {
		t.Fatalf("bindings from the image are kept: %+v", k)
	}
	enum := `Enum\PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`
	ek, _ := has(enum)
	if ek.get("Address") != "" || ek.get("UINumber") != "" || ek.get("Capabilities") != "" || ek.get("Mfg") != `"Intel"` || ek.get("ConfigFlags") != "dword:00000000" {
		t.Fatalf("enum %+v", ek.Values)
	}
	if _, ok := has(enum + `\Control`); ok {
		t.Fatal("donor slot resources copied")
	}
	msi, ok := has(enum + `\Device Parameters\Interrupt Management\MessageSignaledInterruptProperties`)
	if !ok || msi.get("MSISupported") != "dword:00000000" || msi.get("MessageNumberLimit") != "" {
		t.Fatalf("interrupt settings come from the target INF: %+v", msi)
	}
	loc, ok := has(`Control\DeviceLocations\PCIROOT(0)#PCI(1100)#PCI(0400)\PCI#VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\Configuration\Driver`)
	if !ok || loc.get("NetCfgInstanceId") != `"{02D6E127-8926-01BA-C43B-CDFC55636E52}"` || loc.get("AlwaysStartDevice") != "dword:00000001" {
		t.Fatalf("device location %+v\n%s", loc, out)
	}
	if _, ok := has(`Services\E1G60`); !ok {
		t.Fatal("service missing")
	}
}

// 同型号复用驱动配置，但不同设备实例不复用样本插槽的资源。
func TestMaterializeSameModelKeepsTheProvenCopy(t *testing.T) {
	tg := crossTarget(t)
	tg.Instance = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF65000`
	tg.Service, tg.SysFile, tg.MatchingID, tg.LocationPath = "vmxnet3ndis6", "vmxnet3.sys", `PCI\VEN_15AD&DEV_07B0`, ""
	out, err := materialize(crossDonor, tg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"MaxTxQueues"="8"`) || strings.Contains(out, `005056FFFF2EF65000\Control]`) || strings.Contains(out, "DeviceLocations") {
		t.Fatalf("same-model copy changed:\n%s", out)
	}
}

// hivex 合并时父键必须先存在：INF 里 Ndi\Interfaces 常写在 Ndi 前面，位置登记也要逐层建出来。
func TestMaterializeCreatesEveryParentBeforeItsChildren(t *testing.T) {
	out, err := materialize(crossDonor, crossTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, k := range parseReg(out) {
		low := strings.ToLower(k.Path)
		parent := low[:strings.LastIndex(low, `\`)]
		inside := strings.Contains(low, `\control\class\{4d36e972-e325-11ce-bfc1-08002be10318}\0011\`) || strings.Contains(low, `\enum\pci\ven_8086&dev_100f`) || strings.Contains(low, `\control\devicelocations\`)
		if inside && !seen[parent] && !strings.HasSuffix(parent, `\enum\pci`) && !strings.HasSuffix(parent, `{4d36e972-e325-11ce-bfc1-08002be10318}`) && !strings.HasSuffix(parent, `\control\devicelocations`) && !strings.HasSuffix(parent, `\control`) {
			t.Fatalf("%s comes before its parent\n%s", k.Path, out)
		}
		seen[low] = true
	}
}

func TestMaterializeRegistersTheTargetLocationInsteadOfDroppingIt(t *testing.T) {
	tg := crossTarget(t)
	property := prefix + `\ControlSet001\Enum\` + tg.Instance + `\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025`
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known", false: "unknown"}[known], func(t *testing.T) {
			target := tg
			if !known {
				target.LocationPath = ""
			}
			donor := crossDonor + "\n[" + prefix + `\ControlSet001\Enum\PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025]` + "\n" + locationProp(`PCIROOT(0)#PCI(1500)#PCI(0000)`) + "\n"
			out, err := materialize(donor, target)
			if err != nil {
				t.Fatal(err)
			}
			keys := parseReg(out)
			prop := findKey(keys, property)
			got := prop.get("@")
			if !known {
				if got != "" {
					t.Fatalf("unknown target location must not inherit a donor property: %s", got)
				}
				return
			}
			// Windows stores DEVPKEY_Device_LocationPaths as a property MULTI_SZ, not REG_MULTI_SZ.
			want := "hex(ffff2012):50,00,43,00,49,00,52,00,4f,00,4f,00,54,00,28,00,30,00,29,00,23,00,50,00,43,00,49,00,28,00,31,00,31,00,30,00,30,00,29,00,23,00,50,00,43,00,49,00,28,00,30,00,34,00,30,00,30,00,29,00,00,00,00,00"
			if got != want {
				t.Fatalf("target LocationPaths missing or wrong: got %s want %s", got, want)
			}
			registration := prefix + `\ControlSet001\Control\DeviceLocations\PCIROOT(0)#PCI(1100)#PCI(0400)\PCI#VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\Configuration\Driver`
			registered := findKey(keys, registration)
			if hiveString(registered.get("NetCfgInstanceId")) != target.GUID {
				t.Fatal("target Enum location and boot interface registration disagree")
			}
		})
	}
}

// A driver service can serve several PCI devices; neither shared service nor chip identity proves slot identity.
func TestMaterializeIsolatesHardwareAndSlotWithSharedService(t *testing.T) {
	for _, tt := range []struct {
		name, instance string
		wantDonorClass bool
	}{
		{"another-chip", `PCI\VEN_15AD&DEV_07B1&SUBSYS_07B015AD&REV_01\4&abc&0&00E0`, false},
		{"another-subsystem", `PCI\VEN_15AD&DEV_07B0&SUBSYS_999915AD&REV_01\4&abc&0&00E0`, false},
		{"another-slot", `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\4&abc&0&00E0`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tg := crossTarget(t)
			tg.Service, tg.SysFile, tg.Instance = "vmxnet3ndis6", "vmxnet3.sys", tt.instance
			donor := crossDonor + "\n[" + prefix + `\ControlSet001\Enum\PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025]` + "\n" + locationProp(`PCIROOT(0)#PCI(1500)#PCI(0000)`) + "\n"
			out, err := materialize(donor, tg)
			if err != nil {
				t.Fatal(err)
			}
			enum := prefix + `\ControlSet001\Enum\` + tt.instance
			keys := parseReg(out)
			root := findKey(keys, enum)
			for _, name := range []string{"Address", "UINumber", "Capabilities"} {
				if root.get(name) != "" {
					t.Fatalf("donor slot field %s leaked: %s", name, root.get(name))
				}
			}
			if k := findKey(keys, enum+`\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025`); k.get("@") != "hex(ffff2012):"+formatHex(encodeMulti([]string{tg.LocationPath})) {
				t.Fatal("target slot LocationPaths property missing or donor location leaked")
			}
			if k := findKey(keys, enum+`\Control`); k.Path != "" {
				t.Fatalf("donor slot resources leaked: %+v", k)
			}
			msi := findKey(keys, enum+`\Device Parameters\Interrupt Management\MessageSignaledInterruptProperties`)
			if msi.get("MessageNumberLimit") != "" {
				t.Fatal("donor interrupt limit leaked")
			}
			if strings.Contains(out, `"MaxTxQueues"="8"`) != tt.wantDonorClass {
				t.Fatalf("wrong driver class reuse:\n%s", out)
			}
		})
	}
}

func TestResolveDriverUsesPublishedOEMNameAndRepositoryPayload(t *testing.T) {
	root := t.TempDir()
	// Case-folded image paths are common when the Windows filesystem is mounted on Linux.
	pkg := filepath.Join(root, "windows", "system32", "driverstore", "filerepository", "rt640x64.inf_amd64_known")
	pub := filepath.Join(root, "windows", "inf")
	for _, p := range []string{pkg, pub} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	z := filepath.Join(t.TempDir(), "driver.zip")
	writeDriverZip(t, z, "rt640x64.inf", "rt640x64.sys", "staged")
	zr, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(filepath.Ext(f.Name), ".sys") {
			b = testDriverPE("")
		}
		if err := os.WriteFile(filepath.Join(pkg, f.Name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(pkg, "rt640x64.inf"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"OEM12.INF": raw, "rt640x64.inf": raw, "oem0.inf": []byte("different package")} {
		if err := os.WriteFile(filepath.Join(pub, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hwid := `PCI\VEN_10EC&DEV_8168`
	got, _, name, err := resolveDriver(root, hwid, []string{hwid})
	if err != nil || got != filepath.Join(pkg, "rt640x64.inf") || name != "OEM12.INF" {
		t.Fatalf("published driver: %s %s %v", got, name, err)
	}
	// A basename collision or another version must never be accepted as this package.
	for _, name := range []string{"OEM12.INF", "rt640x64.inf"} {
		if err := os.WriteFile(filepath.Join(pub, name), []byte("another version"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := resolveDriver(root, hwid, []string{hwid}); !IsSkipTemplate(err) {
		t.Fatalf("unpublished repository package accepted: %v", err)
	}
}

// 仅保留实际 rtCx21 INF 的安装指令与服务 AddReg 语法，不随测试分发驱动包。
const netCxRegistrationINF = `
[Version]
Class=Net
[Manufacturer]
Realtek=Realtek,NTamd64
[Realtek.NTamd64]
Realtek=RTL8168H.ndi,PCI\VEN_10EC&DEV_8168&REV_15
[RTL8168H.ndi.NT]
*IfType=6
*MediaType=0
*PhysicalMediaType=14
*IfConnectorPresent=1
*ConnectionType=1
*DirectionType=0
*AccessType=2
*HardwareLoopback=0
[RTL8168H.ndi.NT.Services]
AddService=%ServiceName%,2,%ServiceSection%
[rtcx21.Service]
ServiceBinary=%13%\rtcx21x64.sys
StartType=3
AddReg=rtcx21.Service.Reg
AddReg=DrvOption.reg,DrvMode.reg,DMAr.reg,Deep.reg
[rtcx21.Service.Reg]
HKR,,BootFlags,0x00010001,1
[DrvOption.reg]
HKR,Parameters,DrvOption,0x00010001,0
[DrvMode.reg]
HKR,Parameters,DrvMode,0x00010001,1
[DMAr.reg]
HKR,Parameters,DmaRemappingCompatible,0x00010001,2
[Deep.reg]
HKR,Parameters\Nested\Empty,,0x10
HKR,Parameters\Nested\Values,Label,0,"target"
[Strings]
ServiceName=rtcx21
ServiceSection=rtcx21.Service
`

func TestMaterializeUsesTargetServiceRegistration(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "actual_INF", true: "boot_override"}[override], func(t *testing.T) {
			inf := netCxRegistrationINF
			if override {
				inf = strings.Replace(inf, "HKR,,BootFlags,0x00010001,1", "HKR,,BootFlags,0x00010001,0\nHKR,,Start,0x00010001,3\nHKR,,ImagePath,0,bad.sys", 1)
			}
			b, err := driverinf.Binding([]byte(inf), `PCI\VEN_10EC&DEV_8168&REV_15`)
			if err != nil {
				t.Fatal(err)
			}
			tg := crossTarget(t)
			tg.INF, tg.Service, tg.SysFile, tg.Section, tg.MatchingID = b, b.Service, b.SysFile, b.Section, b.MatchingID
			tg.Instance = `PCI\VEN_10EC&DEV_8168&SUBSYS_012310EC&REV_15\01000000684CE00000`
			donor := crossDonor + "\n[" + prefix + `\ControlSet001\Services\vmxnet3ndis6\Parameters]` + "\n\"DrvOption\"=dword:00000009\n\"DrvMode\"=dword:00000009\n"
			out, err := materialize(donor, tg)
			if err != nil {
				t.Fatal(err)
			}
			keys := parseReg(out)
			base := prefix + `\ControlSet001\Services\rtcx21`
			root, params := findKey(keys, base), findKey(keys, base+`\Parameters`)
			for name, want := range map[string]string{"Start": "dword:00000000", "BootFlags": "dword:00000001", "ImagePath": "hex(2):" + utf16Hex(`\SystemRoot\System32\drivers\rtcx21x64.sys`)} {
				if got := root.get(name); got != want {
					t.Errorf("service %s=%s, want %s", name, got, want)
				}
			}
			for name, want := range map[string]string{"DrvOption": "dword:00000000", "DrvMode": "dword:00000001", "DmaRemappingCompatible": "dword:00000002"} {
				if got := params.get(name); got != want {
					t.Errorf("Parameters %s=%s, want %s", name, got, want)
				}
			}
			class := findKey(keys, prefix+`\ControlSet001\`+classRoot+`\0011`)
			for name, want := range map[string]string{"*IfConnectorPresent": "dword:00000001", "*ConnectionType": "dword:00000001", "*DirectionType": "dword:00000000", "*AccessType": "dword:00000002", "*HardwareLoopback": "dword:00000000"} {
				if got := class.get(name); got != want {
					t.Errorf("class %s=%s, want %s", name, got, want)
				}
			}
			seen := map[string]bool{}
			for _, k := range keys {
				low := strings.ToLower(k.Path)
				if seen[low] {
					t.Fatalf("duplicate output key: %s", k.Path)
				}
				if isUnderPath(k.Path, base+`\Parameters`) && !seen[strings.ToLower(k.Path[:strings.LastIndex(k.Path, `\`)])] {
					t.Errorf("service child before parent: %s", k.Path)
				}
				if !strings.EqualFold(k.Path, base+`\Parameters`) && (k.get("DrvOption") != "" || k.get("DrvMode") != "" || k.get("DmaRemappingCompatible") != "") {
					t.Errorf("service AddReg leaked into %s", k.Path)
				}
				seen[low] = true
			}
			nested := findKey(keys, base+`\Parameters\Nested\Values`)
			if !seen[strings.ToLower(base+`\Parameters\Nested\Empty`)] || nested.get("Label") != `"target"` {
				t.Error("nested service registration missing")
			}
		})
	}
}
