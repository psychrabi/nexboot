package winreg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/tianwei/diskless/internal/driverinf"
)

// 镜像里没有可用驱动，或没有已安装网卡可当样本时，开机改为从原还原点直接启动，不记模板失败。
const skipTemplatePrefix = "跳过网卡模板："

// IsSkipTemplate 说明这次不该写模板。远程存储节点把错误收成字符串后，前缀仍在。
func IsSkipTemplate(err error) bool {
	return err != nil && strings.Contains(err.Error(), skipTemplatePrefix)
}

func skipTemplate(reason string) error {
	return fmt.Errorf("%s%s", skipTemplatePrefix, reason)
}

// InstancePresentPrefix 表示镜像里已经装着这台机器自己的网卡（例如它就是装系统的那台），原样开机即可。
const InstancePresentPrefix = "镜像里已装好这台机器的网卡："

func IsInstancePresent(err error) bool {
	return err != nil && strings.Contains(err.Error(), InstancePresentPrefix)
}

// GeneratorVersion 进身份层的标记：生成规则改了，按旧规则写的身份层要重建。
const GeneratorVersion = "7"

// GenerateSpec 是这台机器和目标网卡；驱动只复用目标镜像已发布的包。
type GenerateSpec struct {
	Instance  string
	GUID      string
	MAC       string
	Vendor    string
	Device    string
	DriverZip string // 保留调用兼容；新增驱动必须先由 WinPE/DISM 登记。
}

type nicTarget struct {
	ControlSet  string
	ClassKey    string
	Instance    string
	GUID        string
	MAC         string
	Service     string
	SysFile     string
	InfFile     string
	Section     string
	Description string
	DisplayName string
	MatchingID  string
	StartType   int
	// INF 是目标驱动里这块设备的安装信息；跨型号时类实例和设备参数按它写，不沿用样本网卡的。
	INF driverinf.NICBinding
	// LocationPath 是这块网卡的 PCI 位置路径（如 PCIROOT(0)#PCI(1100)#PCI(0400)），推不出时为空。
	LocationPath string
}

// ApplyGenerated 用镜像里已安装的那块网卡当样本，按目标网卡的驱动文件改写成这台机器的配置，合并进 SYSTEM。
// 只复用镜像已发布的驱动包；未登记的上传包需先走 WinPE/DISM 适配，Linux 不伪造驱动库登记。
func ApplyGenerated(ctx context.Context, windowsRoot, hive string, spec GenerateSpec) error {
	if !validInstance.MatchString(spec.Instance) || !validGUID.MatchString(spec.GUID) || !validMAC.MatchString(spec.MAC) {
		return generationFailure(fmt.Errorf("网卡模板参数无效"))
	}
	ven := strings.ToUpper(spec.Vendor)
	dev := strings.ToUpper(spec.Device)
	hwid := `PCI\VEN_` + ven + `&DEV_` + dev
	hardware, compatible := pciIDs(spec.Instance)
	infPath, binding, publishedName, err := resolveDriver(windowsRoot, hwid, append(hardware, compatible...))
	if err != nil {
		return err
	}
	if err := copyServiceBinary(windowsRoot, infPath, binding.SysFile, false); err != nil {
		return err
	}
	name, cs, classKey, err := stageHive(ctx, hive)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if text, err := export(ctx, name, cs+"\\Enum\\"+spec.Instance); err == nil && strings.Contains(strings.ToLower(text), `"service"=`) {
		return fmt.Errorf("%s%s", InstancePresentPrefix, spec.Instance)
	}
	donorText, err := exportDonor(ctx, name, cs)
	if err != nil {
		return err
	}
	location := ""
	if pci, err := export(ctx, name, cs+"\\Enum\\PCI"); err == nil {
		location = deriveLocationPath(pci, spec.Instance)
	}
	matching := binding.MatchingID
	if matching == "" {
		matching = hwid
	}
	reg, err := materialize(donorText, nicTarget{
		ControlSet: cs, ClassKey: classKey, Instance: spec.Instance, GUID: strings.ToUpper(spec.GUID), MAC: strings.ToUpper(spec.MAC),
		Service: binding.Service, SysFile: binding.SysFile, InfFile: publishedName, Section: binding.Section,
		Description: binding.Description, DisplayName: binding.DisplayName, MatchingID: matching, StartType: binding.StartType,
		INF: binding, LocationPath: location,
	})
	if err != nil {
		if IsSkipTemplate(err) {
			return err
		}
		return generationFailure(err)
	}
	if err := commitMerged(ctx, name, reg, spec.Instance, cs, classKey); err != nil {
		return err
	}
	if err := os.Chmod(name, 0600); err != nil {
		return err
	}
	return os.Rename(name, hive)
}

func resolveDriver(windowsRoot, hwid string, ids []string) (string, driverinf.NICBinding, string, error) {
	store, err := foldPath(windowsRoot, []string{"Windows", "System32", "DriverStore", "FileRepository"})
	if err == nil {
		published := publishedINFs(windowsRoot)
		p, b, err := searchINF(windowsRoot, store, ids, func(p string) bool { return publishedINF(published, p) != "" })
		if err != nil {
			var ioError *os.PathError
			if errors.As(err, &ioError) {
				return "", driverinf.NICBinding{}, "", err
			}
			return "", driverinf.NICBinding{}, "", generationFailure(err)
		}
		if p != "" {
			return p, b, publishedINF(published, p), nil
		}
	}
	return "", driverinf.NICBinding{}, "", skipTemplate("请为网卡 " + hwid + " 做机型适配：镜像缺少已登记的驱动包，请在「机型」页做完整适配；上传驱动后仍需由 Windows/DISM 登记")
}

// Published INF content must match the repository package, not merely its original basename.
func publishedINFs(root string) map[[32]byte][]string {
	out := map[[32]byte][]string{}
	dir, err := foldPath(root, []string{"Windows", "INF"})
	if err != nil {
		return out
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".inf") {
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				sum := sha256.Sum256(data)
				out[sum] = append(out[sum], e.Name())
			}
		}
	}
	return out
}

var publishedOEM = regexp.MustCompile(`(?i)^oem[0-9]+\.inf$`)

func publishedINF(published map[[32]byte][]string, infPath string) string {
	data, err := os.ReadFile(infPath)
	if err != nil {
		return ""
	}
	var names []string
	for _, name := range published[sha256.Sum256(data)] {
		if publishedOEM.MatchString(name) || strings.EqualFold(name, filepath.Base(infPath)) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := publishedOEM.MatchString(names[i]), publishedOEM.MatchString(names[j])
		if a != b {
			return a
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// searchINF 在目录里找配得上这块设备的 INF：配上的硬件 ID 越具体越好，同样具体时用驱动库里的 amd64 包。
func searchINF(windowsRoot, root string, ids []string, accept ...func(string) bool) (string, driverinf.NICBinding, error) {
	var bestPath string
	var best driverinf.NICBinding
	var rejected error
	var transient error
	bestScore := -1
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".inf") {
			return nil
		}
		if len(accept) > 0 && !accept[0](p) {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		b, err := driverinf.Binding(raw, append([]string(nil), ids...)...)
		if err != nil {
			return nil
		}
		binaryPath, err := systemBootBinary(windowsRoot, p, b.SysFile)
		cx := false
		if err == nil {
			cx, err = bindsNetAdapterCx(binaryPath)
		}
		if err != nil {
			reason := fmt.Errorf("请检查网卡驱动 %s：无法确认系统启动驱动：%w", b.SysFile, err)
			var ioError *os.PathError
			if errors.As(err, &ioError) {
				transient = reason
			} else {
				rejected = reason
			}
			return nil
		}
		if cx {
			rejected = fmt.Errorf("网卡驱动 %s 使用 NetAdapterCx，不支持 iSCSI 无盘启动；请安装此网卡兼容的传统 NDIS 驱动，再做完整机型适配", b.SysFile)
			return nil
		}
		score := 0
		for i, id := range ids {
			if strings.EqualFold(id, b.MatchingID) {
				score = (len(ids) - i) * 10
				break
			}
		}
		if strings.Contains(strings.ToLower(p), "_amd64_") {
			score++
		}
		if score > bestScore {
			bestScore = score
			bestPath = p
			best = b
		}
		return nil
	})
	if bestPath != "" {
		return bestPath, best, nil
	}
	if transient != nil {
		return "", best, transient
	}
	return "", best, rejected
}

func copyServiceBinary(windowsRoot, infPath, sysFile string, overwrite bool) error {
	if !sysName.MatchString(sysFile) {
		return fmt.Errorf("驱动服务文件名无效：%s", sysFile)
	}
	src, err := findNamedFile(filepath.Dir(infPath), sysFile, 2)
	if err != nil {
		return fmt.Errorf("驱动目录里没有 %s", sysFile)
	}
	drivers, err := ensureDrivers(windowsRoot)
	if err != nil {
		return err
	}
	if !overwrite {
		if _, err := findNamedFile(drivers, sysFile, 0); err == nil {
			return nil
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(drivers, sysFile), data, 0644)
}

func findNamedFile(dir, name string, depth int) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	if depth <= 0 {
		return "", fmt.Errorf("missing %s", name)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, err := findNamedFile(filepath.Join(dir, e.Name()), name, depth-1); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("missing %s", name)
}

func ensureDrivers(root string) (string, error) {
	sys, err := foldPath(root, []string{"Windows", "System32"})
	if err != nil {
		sys = filepath.Join(root, "Windows", "System32")
	}
	if entries, err := os.ReadDir(sys); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), "drivers") {
				return filepath.Join(sys, e.Name()), nil
			}
		}
	}
	dir := filepath.Join(sys, "drivers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func foldPath(root string, parts []string) (string, error) {
	cur := root
	for _, part := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", err
		}
		found := ""
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), part) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return "", fmt.Errorf("缺少目录 %s", part)
		}
		cur = filepath.Join(cur, found)
	}
	return cur, nil
}

var sysName = regexp.MustCompile(`(?i)^[A-Za-z0-9_.-]+\.sys$`)
var serviceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

type donorNIC struct {
	ControlSet string
	ClassKey   string
	ClassPath  string
	EnumPath   string
	Service    string
	Instance   string
	GUID       string
}

type regKey struct {
	Path   string
	Values []regValue
}

type regValue struct {
	Name string
	Raw  string
}

func exportDonor(ctx context.Context, hive, cs string) (string, error) {
	classText, err := export(ctx, hive, cs+"\\"+classRoot)
	if err != nil {
		return "", err
	}
	keys := parseReg(classText)
	donor, err := identifyDonor(keys)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(classText)
	enumText, err := export(ctx, hive, cs+"\\Enum\\"+donor.Instance)
	if err != nil {
		return "", skipTemplate("这张镜像里没有已安装的网卡，写不出模板")
	}
	b.WriteString("\n")
	b.WriteString(enumText)
	keys = parseReg(enumText)
	service := ""
	for _, k := range keys {
		if strings.EqualFold(k.Path, prefix+"\\"+cs+"\\Enum\\"+donor.Instance) {
			service = hiveString(k.get("Service"))
		}
	}
	if !serviceName.MatchString(service) {
		return "", skipTemplate("已安装的网卡没有服务名，写不出模板")
	}
	svc, err := export(ctx, hive, cs+"\\Services\\"+service)
	if err != nil {
		return "", skipTemplate("已安装的网卡服务导出失败，写不出模板")
	}
	b.WriteString("\n")
	b.WriteString(svc)
	for _, rel := range []string{
		cs + "\\Services\\Tcpip\\Linkage",
		cs + "\\Services\\Tcpip6\\Linkage",
		cs + "\\Services\\RasPppoe\\Linkage",
		cs + "\\Services\\NetBT\\Parameters\\Interfaces",
		cs + "\\Services\\Tcpip\\Parameters\\Interfaces\\" + donor.GUID,
		cs + "\\Services\\Tcpip6\\Parameters\\Interfaces\\" + donor.GUID,
		cs + "\\Services\\Tcpip\\Parameters\\Adapters\\" + donor.GUID,
		cs + "\\Control\\Network\\{4D36E972-E325-11CE-BFC1-08002BE10318}\\" + donor.GUID,
		cs + "\\Control\\NetworkSetup2",
	} {
		if text, err := export(ctx, hive, rel); err == nil {
			b.WriteString("\n")
			b.WriteString(text)
		}
	}
	return b.String(), nil
}

func identifyDonor(keys []regKey) (donorNIC, error) {
	marker := `{4d36e972-e325-11ce-bfc1-08002be10318}`
	bestN := 10000
	var best regKey
	var bestKey string
	for _, k := range keys {
		lower := strings.ToLower(k.Path)
		i := strings.Index(lower, marker)
		if i < 0 {
			continue
		}
		rest := k.Path[i+len(marker):]
		rest = strings.TrimPrefix(rest, `\`)
		seg, tail, _ := strings.Cut(rest, `\`)
		if tail != "" || !validClass.MatchString(seg) {
			continue
		}
		instance := decodeRegString(k.get("DeviceInstanceID"))
		if !strings.HasPrefix(strings.ToUpper(instance), `PCI\`) {
			continue
		}
		desc := strings.ToLower(hiveString(k.get("DriverDesc")))
		if strings.Contains(desc, "kdnic") {
			continue
		}
		n, _ := strconv.Atoi(seg)
		if n < bestN {
			bestN = n
			best = k
			bestKey = seg
		}
	}
	if best.Path == "" {
		return donorNIC{}, skipTemplate("这张镜像里没有已安装的网卡，写不出模板")
	}
	guid := hiveString(best.get("NetCfgInstanceId"))
	instance := hiveString(best.get("DeviceInstanceID"))
	if !validGUID.MatchString(guid) || !validInstance.MatchString(instance) {
		return donorNIC{}, skipTemplate("已安装的网卡配置不完整，写不出模板")
	}
	cs := ""
	if m := regexp.MustCompile(`\\(ControlSet[0-9]{3})\\`).FindStringSubmatch(best.Path); m != nil {
		cs = m[1]
	}
	return donorNIC{ControlSet: cs, ClassKey: bestKey, ClassPath: best.Path, Instance: instance, GUID: guid}, nil
}

func materialize(donorExport string, t nicTarget) (string, error) {
	if !validCS.MatchString(t.ControlSet) || !validClass.MatchString(t.ClassKey) || !serviceName.MatchString(t.Service) || !sysName.MatchString(t.SysFile) {
		return "", fmt.Errorf("网卡模板参数无效")
	}
	keys := parseReg(donorExport)
	donor, err := identifyDonor(keys)
	if err != nil {
		return "", err
	}
	donor.EnumPath = prefix + `\` + donor.ControlSet + `\Enum\` + donor.Instance
	for _, k := range keys {
		if strings.HasSuffix(strings.ToLower(k.Path), strings.ToLower(`\Enum\`+donor.Instance)) {
			donor.EnumPath = k.Path
			donor.Service = hiveString(k.get("Service"))
		}
	}
	if donor.Service == "" {
		donor.Service = t.Service
	}
	r := newRewriter(keys, donor, t)
	// 跨型号：类实例、设备参数和资源记录都属于样本那块网卡的驱动和插槽，按目标驱动的 INF 重写。
	cross := t.INF.Section != "" && (!strings.EqualFold(donor.Service, t.Service) || !strings.EqualFold(hardwarePrefix(donor.Instance), hardwarePrefix(t.Instance)))
	relocated := !strings.EqualFold(donor.Instance, t.Instance)
	var out []regKey
	if cross {
		out = append(out, driverKeys(t, r, findKey(keys, donor.ClassPath))...)
	}
	for _, k := range keys {
		if skipGeneratedKey(k.Path) {
			continue
		}
		switch {
		case isUnderPath(k.Path, donor.ClassPath):
			if cross && !isUnderPath(k.Path, donor.ClassPath+`\Linkage`) {
				continue
			}
			out = append(out, rewriteClass(k, donor, t, r))
		case isUnderPath(k.Path, donor.EnumPath):
			if relocated && dropsWithTheSlot(k.Path, donor.EnumPath) {
				continue
			}
			out = append(out, rewriteEnum(k, donor, t, r, relocated))
		case isUnderPath(k.Path, prefix+`\`+donor.ControlSet+`\Services\`+donor.Service):
			out = append(out, rewriteService(k, donor, t, r))
		case isLinkage(k.Path) && valueHasGUID(k, donor.GUID):
			out = append(out, rewriteLinkage(k, donor, t))
		case strings.Contains(strings.ToLower(k.Path), strings.ToLower(donor.GUID)):
			out = append(out, rewriteGUIDKey(k, donor, t, r))
		}
	}
	if !hasServiceKey(out, t) {
		return "", skipTemplate("已安装的网卡服务导出失败，写不出模板")
	}
	out = serviceRegistry(out, t)
	if relocated && t.INF.Section != "" {
		out = append(out, deviceParameters(t)...)
	}
	out = append(out, deviceLocation(t)...)
	return formatReg(ensureParents(out)), nil
}

// dropsWithTheSlot 说明样本设备实例下这棵子键记的是它自己那个插槽的资源与中断，换了网卡和插槽就不对。
func dropsWithTheSlot(path, enumPath string) bool {
	rel := strings.ToLower(strings.TrimPrefix(strings.ToLower(path), strings.ToLower(enumPath)))
	for _, property := range []string{
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\000f`, // LocationInfo
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0010`, // PDOName
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0011`, // Capabilities
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0012`, // UINumber
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0017`, // BusNumber
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\001e`, // Address
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\001f`, // UINumberDescFormat
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0025`, // LocationPaths
		`{a45c254e-df1c-4efd-8020-67d146a850e0}\0026`, // BaseContainerId
		`{8c7ed206-3f8a-4827-b3ab-ae9e1faefc6c}\0002`, // ContainerId
		`{540b947e-8b40-45bc-a8a2-6a0b894cbda2}\0009`, // PhysicalDeviceLocation
		`{540b947e-8b40-45bc-a8a2-6a0b894cbda2}\000a`, // BiosDeviceName
	} {
		if isUnderPath(rel, `\properties\`+property) {
			return true
		}
	}
	return isUnderPath(rel, `\control`) || isUnderPath(rel, `\logconf`) || isUnderPath(rel, `\device parameters`)
}

// driverKeys 按 Windows 安装驱动时的写法生成类实例：INF 安装节的值和 AddReg，加上各高级参数的默认值（可选参数除外）。
func driverKeys(t nicTarget, r rewriter, donorClass regKey) []regKey {
	base := prefix + `\` + t.ControlSet + `\` + classRoot + `\` + t.ClassKey
	root := regKey{Path: base}
	subs := map[string]*regKey{}
	var order []string
	sub := func(rel string) *regKey {
		low := strings.ToLower(rel)
		if k, ok := subs[low]; ok {
			return k
		}
		subs[low] = &regKey{Path: base + `\` + rel}
		order = append(order, low)
		return subs[low]
	}
	for _, e := range t.INF.DriverReg {
		k := &root
		if e.Key != "" {
			k = sub(e.Key)
		}
		if e.Kind == driverinf.RegKeyOnly {
			continue
		}
		k.set(regName(e.Name), regRaw(e))
	}
	// 安装时 Windows 把每个非可选高级参数的默认值写到驱动键根下。
	for _, low := range order {
		k := subs[low]
		name, ok := paramName(k.Path)
		if !ok || strings.EqualFold(unquoteReg(k.get("Optional")), "1") || strings.EqualFold(unquoteReg(k.get("optional")), "1") {
			continue
		}
		if def := k.get("default"); def != "" {
			if root.get(name) == "" {
				root.set(name, def)
			}
		}
	}
	mac := formatHex(macBytes(t.MAC))
	root.set("Characteristics", fmt.Sprintf("dword:%08x", t.INF.Characteristics))
	if t.INF.BusType != "" {
		root.set("BusType", quoteReg(t.INF.BusType))
	}
	root.set("*IfType", fmt.Sprintf("dword:%08x", t.INF.IfType))
	root.set("IfTypePreStart", fmt.Sprintf("dword:%08x", t.INF.IfType))
	root.set("*MediaType", fmt.Sprintf("dword:%08x", t.INF.MediaType))
	root.set("*PhysicalMediaType", fmt.Sprintf("dword:%08x", t.INF.PhysicalMediaType))
	root.set("ProviderName", quoteReg(t.INF.Provider))
	root.set("DriverDate", quoteReg(t.INF.DriverDate))
	if when, err := time.Parse("1-2-2006", t.INF.DriverDate); err == nil {
		ft := uint64(when.Unix()+11644473600) * 10000000
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, ft)
		root.set("DriverDateData", "hex(3):"+formatHex(b))
	}
	// 安装时间取镜像里那块网卡的，Windows 只拿它做记录。
	for _, name := range []string{"InstallTimeStamp", "NetworkInterfaceInstallTimestamp"} {
		if v := donorClass.get(name); v != "" {
			root.set(name, v)
		}
	}
	root.set("DriverVersion", quoteReg(t.INF.DriverVersion))
	root.set("DriverDesc", quoteReg(t.Description))
	root.set("InfPath", quoteReg(t.InfFile))
	root.set("InfSection", quoteReg(t.Section))
	root.set("MatchingDeviceId", quoteReg(t.MatchingID))
	root.set("ComponentId", quoteReg(t.MatchingID))
	root.set("NetCfgInstanceId", quoteReg(t.GUID))
	root.set("NetLuidIndex", fmt.Sprintf("dword:%08x", r.luid))
	root.set("DeviceInstanceID", "hex(1):"+utf16Hex(t.Instance))
	root.set("CurrentAddress", "hex:"+mac)
	root.set("PermanentAddress", "hex:"+mac)
	out := []regKey{root}
	for _, low := range order {
		out = append(out, *subs[low])
	}
	return out
}

// serviceRegistry 最后合并目标服务的 AddReg，避免被样本服务的参数覆盖。
func serviceRegistry(keys []regKey, t nicTarget) []regKey {
	base := prefix + `\` + t.ControlSet + `\Services\` + t.Service
	for _, e := range t.INF.ServiceReg {
		path := base
		if e.Key != "" {
			path += `\` + e.Key
		}
		i := 0
		for i < len(keys) && !strings.EqualFold(keys[i].Path, path) {
			i++
		}
		if i == len(keys) {
			keys = append(keys, regKey{Path: path})
		}
		if e.Kind != driverinf.RegKeyOnly {
			keys[i].set(regName(e.Name), regRaw(e))
		}
	}
	for i := range keys {
		if strings.EqualFold(keys[i].Path, base) {
			setBootService(&keys[i], t)
		}
	}
	return keys
}

// deviceParameters 是 INF .HW 节写到设备键的值（如是否用 MSI 中断）。
func deviceParameters(t nicTarget) []regKey {
	base := prefix + `\` + t.ControlSet + `\Enum\` + t.Instance + `\Device Parameters`
	keys := map[string]*regKey{}
	var order []string
	for _, e := range t.INF.DeviceReg {
		path := base
		if e.Key != "" {
			path += `\` + e.Key
		}
		low := strings.ToLower(path)
		if _, ok := keys[low]; !ok {
			keys[low] = &regKey{Path: path}
			order = append(order, low)
		}
		if e.Kind != driverinf.RegKeyOnly {
			keys[low].set(regName(e.Name), regRaw(e))
		}
	}
	var out []regKey
	for _, low := range order {
		out = append(out, *keys[low])
	}
	return out
}

// deviceLocation 按插槽位置登记这块网卡用哪个接口，和 Windows 装网卡时写的一样；位置推不出时不写。
func deviceLocation(t nicTarget) []regKey {
	if t.LocationPath == "" {
		return nil
	}
	base := prefix + `\` + t.ControlSet + `\Control\DeviceLocations\` + t.LocationPath + `\` + strings.ReplaceAll(hardwarePrefix(t.Instance), `\`, "#")
	return []regKey{
		{Path: base + `\Configuration\Driver`, Values: []regValue{{Name: "AlwaysStartDevice", Raw: "dword:00000001"}, {Name: "NetCfgInstanceId", Raw: quoteReg(t.GUID)}}},
		{Path: base + `\Configuration\Status`, Values: []regValue{{Name: "ClassConfigured", Raw: "dword:00000001"}}},
		// Boot-time PnP also needs the device property; dropping the donor location requires a target replacement.
		{Path: prefix + `\` + t.ControlSet + `\Enum\` + t.Instance + `\Properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025`, Values: []regValue{{Name: "@", Raw: "hex(ffff2012):" + formatHex(encodeMulti([]string{t.LocationPath}))}}},
	}
}

func paramName(path string) (string, bool) {
	low := strings.ToLower(path)
	i := strings.Index(low, `\ndi\params\`)
	if i < 0 {
		return "", false
	}
	rest := path[i+len(`\ndi\params\`):]
	if rest == "" || strings.Contains(rest, `\`) {
		return "", false
	}
	return rest, true
}

func regName(name string) string {
	if name == "" {
		return "@"
	}
	return name
}

func regRaw(e driverinf.RegEntry) string {
	switch e.Kind {
	case driverinf.RegDword:
		return fmt.Sprintf("dword:%08x", e.Dword)
	case driverinf.RegMultiSZ:
		return multiHex(e.Strings)
	case driverinf.RegExpandSZ:
		return "hex(2):" + utf16Hex(firstNonEmpty(e.Strings...))
	case driverinf.RegBinary:
		return "hex:" + formatHex(e.Binary)
	}
	if len(e.Strings) == 0 {
		return quoteReg("")
	}
	return quoteReg(e.Strings[0])
}

// deriveLocationPath 由镜像里同一父设备下其他 PCI 设备的位置路径推出这块网卡的。实例号末段编码了插槽，
// 先拿同父的已知设备验证这条换算，全部对上才用；对不上或没有可参照的就返回空。
func deriveLocationPath(enumExport, instance string) string {
	parent, slot, ok := splitLocationInstance(instance[strings.LastIndex(instance, `\`)+1:])
	if !ok {
		return ""
	}
	want, ok := slotElement(slot)
	if !ok {
		return ""
	}
	const prop = `\properties\{a45c254e-df1c-4efd-8020-67d146a850e0}\0025`
	base := ""
	for _, k := range parseReg(enumExport) {
		low := strings.ToLower(k.Path)
		if !strings.HasSuffix(low, prop) {
			continue
		}
		device := k.Path[:len(k.Path)-len(prop)]
		p, s, ok := splitLocationInstance(device[strings.LastIndex(device, `\`)+1:])
		if !ok || !strings.EqualFold(p, parent) {
			continue
		}
		elem, ok := slotElement(s)
		if !ok {
			return ""
		}
		path := ""
		for _, v := range decodeMulti(hexValue(k.get("@"))) {
			if strings.HasPrefix(strings.ToUpper(v), "PCIROOT(") {
				path = v
				break
			}
		}
		i := strings.LastIndex(path, "#")
		if i < 0 || !strings.EqualFold(path[i+1:], elem) {
			return ""
		}
		if base != "" && !strings.EqualFold(base, path[:i]) {
			return ""
		}
		base = path[:i]
	}
	if base == "" {
		return ""
	}
	return base + "#" + want
}

// splitLocationInstance 把 4&bbf9765&0&2088 拆成同父前缀 4&bbf9765&0 和插槽 2088；按 MAC 编号的不是这种格式。
func splitLocationInstance(serial string) (parent, slot string, ok bool) {
	parts := strings.Split(serial, "&")
	if len(parts) != 4 {
		return "", "", false
	}
	return strings.Join(parts[:3], "&"), parts[3], true
}

// slotElement 把插槽换成位置路径的最后一段：两位时整个是 设备号<<3|功能号，四位时取高两位。
func slotElement(slot string) (string, bool) {
	if len(slot) != 2 && len(slot) != 4 {
		return "", false
	}
	n, err := strconv.ParseUint(slot[:2], 16, 8)
	if err != nil {
		return "", false
	}
	if _, err := strconv.ParseUint(slot, 16, 16); err != nil {
		return "", false
	}
	return fmt.Sprintf("PCI(%02X%02X)", n>>3, n&7), true
}

// rewriter 把样本网卡留在注册表里的身份和驱动信息换成目标网卡的：
// 字符串里按子串换实例号、GUID、硬件 ID、INF 文件名、安装节、设备名、服务名；二进制里换 MAC 和接口 LUID。
type rewriter struct {
	subs [][2]string
	bins [][2][]byte
	luid uint32
}

func newRewriter(keys []regKey, donor donorNIC, t nicTarget) rewriter {
	var r rewriter
	add := func(old, new string) {
		if old != "" && new != "" && !strings.EqualFold(old, new) {
			r.subs = append(r.subs, [2]string{old, new})
		}
	}
	class := findKey(keys, donor.ClassPath)
	add(donor.Instance, t.Instance)
	add(strings.ReplaceAll(donor.Instance, `\`, "#"), strings.ReplaceAll(t.Instance, `\`, "#"))
	add(donor.GUID, t.GUID)
	add(hardwarePrefix(donor.Instance), hardwarePrefix(t.Instance))
	add(hiveString(class.get("MatchingDeviceId")), t.MatchingID)
	add(hiveString(class.get("ComponentId")), t.MatchingID)
	add(hiveString(class.get("InfPath")), t.InfFile)
	add(hiveString(class.get("InfSection")), t.Section)
	add(hiveString(class.get("DriverDesc")), t.Description)
	add(donor.Service, t.Service)
	// 长的先换：实例号里含着硬件 ID 前缀，前缀里又含着匹配 ID。
	sort.SliceStable(r.subs, func(i, j int) bool { return len(r.subs[i][0]) > len(r.subs[j][0]) })
	kernel := findKey(keys, prefix+`\`+donor.ControlSet+`\Control\NetworkSetup2\Interfaces\`+donor.GUID+`\Kernel`)
	if mac := hexValue(kernel.get("PermanentAddress")); len(mac) == 6 {
		r.bins = append(r.bins, [2][]byte{mac, macBytes(t.MAC)})
	}
	// 接口 LUID 编号取现有最大值加一，类实例和 NetworkSetup2 写同一个，免得与样本网卡撞号。
	r.luid = 0x8000
	if n, err := strconv.Atoi(t.ClassKey); err == nil {
		r.luid += uint32(n)
	}
	max := uint32(0)
	for _, k := range keys {
		low := strings.ToLower(k.Path)
		if isClassChild(k.Path) || (strings.Contains(low, `\control\networksetup2\interfaces\`) && strings.HasSuffix(low, `\kernel`)) {
			if n := dwordValue(k.get("NetLuidIndex")); n > max {
				max = n
			}
		}
	}
	if max > 0 {
		r.luid = max + 1
	}
	if old := dwordValue(class.get("NetLuidIndex")); old != 0 {
		r.bins = append(r.bins, [2][]byte{luidBytes(old), luidBytes(r.luid)})
	}
	return r
}

func (r rewriter) text(s string) string {
	for _, p := range r.subs {
		s = replaceFold(s, p[0], p[1])
	}
	return s
}

func (r rewriter) values(values []regValue) []regValue {
	out := make([]regValue, len(values))
	for i, v := range values {
		v.Raw = r.raw(v.Raw)
		out[i] = v
	}
	return out
}

func (r rewriter) raw(raw string) string {
	if strings.HasPrefix(raw, `"`) {
		return quoteReg(r.text(unquoteReg(raw)))
	}
	kind, body, ok := splitHex(raw)
	if !ok {
		return raw
	}
	b, err := parseHexBytes(body)
	if err != nil {
		return raw
	}
	switch kind {
	case "hex(7)", "hex(ffff2012)":
		elems := decodeMulti(b)
		changed := false
		for i, e := range elems {
			if n := r.text(e); n != e {
				elems[i], changed = n, true
			}
		}
		if !changed {
			return raw
		}
		return kind + ":" + formatHex(encodeMulti(elems))
	case "hex(1)", "hex(2)", "hex(ffff0012)":
		s := strings.TrimRight(decodeUTF16(b), "\x00")
		if n := r.text(s); n != s {
			return kind + ":" + utf16Hex(n)
		}
		return raw
	}
	for _, p := range r.bins {
		b = bytes.ReplaceAll(b, p[0], p[1])
	}
	return kind + ":" + formatHex(b)
}

func findKey(keys []regKey, path string) regKey {
	for _, k := range keys {
		if strings.EqualFold(k.Path, path) {
			return k
		}
	}
	return regKey{}
}

func isClassChild(path string) bool {
	marker := `{4d36e972-e325-11ce-bfc1-08002be10318}\`
	i := strings.Index(strings.ToLower(path), marker)
	return i >= 0 && validClass.MatchString(path[i+len(marker):])
}

func hardwarePrefix(instance string) string {
	if i := strings.LastIndex(instance, `\`); i > 0 {
		return instance[:i]
	}
	return ""
}

// pciIDs 按 Windows 给 PCI 网卡生成的格式，由设备实例算出硬件 ID 和兼容 ID。
func pciIDs(instance string) (hardware, compatible []string) {
	var ven, dev, subsys, rev string
	for _, part := range strings.Split(strings.TrimPrefix(hardwarePrefix(instance), `PCI\`), "&") {
		switch up := strings.ToUpper(part); {
		case strings.HasPrefix(up, "VEN_"):
			ven = part
		case strings.HasPrefix(up, "DEV_"):
			dev = part
		case strings.HasPrefix(up, "SUBSYS_"):
			subsys = part
		case strings.HasPrefix(up, "REV_"):
			rev = part
		}
	}
	base := `PCI\` + ven + "&" + dev
	if subsys != "" && rev != "" {
		hardware = append(hardware, base+"&"+subsys+"&"+rev)
	}
	if subsys != "" {
		hardware = append(hardware, base+"&"+subsys)
	}
	hardware = append(hardware, base+"&CC_020000", base+"&CC_0200")
	if rev != "" {
		compatible = append(compatible, base+"&"+rev)
	}
	compatible = append(compatible, base, `PCI\`+ven+"&CC_020000", `PCI\`+ven+"&CC_0200", `PCI\`+ven,
		`PCI\CC_020000&DT_0`, `PCI\CC_020000`, `PCI\CC_0200&DT_0`, `PCI\CC_0200`)
	return hardware, compatible
}

func hexValue(raw string) []byte {
	_, body, ok := splitHex(raw)
	if !ok {
		return nil
	}
	b, err := parseHexBytes(body)
	if err != nil {
		return nil
	}
	return b
}

func dwordValue(raw string) uint32 {
	if !strings.HasPrefix(raw, "dword:") {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(raw, "dword:"), 16, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// luidBytes 是以太网接口 NET_LUID 的小端字节：24 位保留、24 位编号、16 位接口类型（6）。
func luidBytes(index uint32) []byte {
	return []byte{0, 0, 0, byte(index), byte(index >> 8), byte(index >> 16), 6, 0}
}

func multiHex(values []string) string {
	return "hex(7):" + formatHex(encodeMulti(values))
}

// ensureParents 补上新建子树（类实例、设备实例、位置登记）里缺的上级键，并按层级排好。
// hivex 不会凭空创建不存在的父键，缺一层整个合并就失败，客户机只能拿原盘启动。
func ensureParents(keys []regKey) []regKey {
	have := map[string]bool{}
	for _, k := range keys {
		have[strings.ToLower(k.Path)] = true
	}
	class := strings.ToLower(`\` + classRoot)
	var extra []regKey
	for _, k := range keys {
		low := strings.ToLower(k.Path)
		if !strings.Contains(low, `\enum\`) && !strings.Contains(low, class+`\`) && !strings.Contains(low, `\control\devicelocations\`) && !strings.Contains(low, `\services\`) {
			continue
		}
		path := k.Path
		for {
			slash := strings.LastIndex(path, `\`)
			if slash < 0 {
				break
			}
			path = path[:slash]
			low := strings.ToLower(path)
			if have[low] || strings.HasSuffix(low, `\enum`) || strings.HasSuffix(low, class) || strings.HasSuffix(low, `\control`) || strings.HasSuffix(low, `\services`) {
				break
			}
			extra = append(extra, regKey{Path: path})
			have[low] = true
		}
	}
	all := append(extra, keys...)
	sort.SliceStable(all, func(i, j int) bool {
		return strings.Count(all[i].Path, `\`) < strings.Count(all[j].Path, `\`)
	})
	return all
}

func skipGeneratedKey(path string) bool {
	lower := strings.ToLower(path)
	for _, part := range []string{`\logconf`, `\lanmanserver\`, `\lanmanworkstation\`, `\devicelocations`, `\devicecontainers`} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func isUnderPath(path, parent string) bool {
	path, parent = strings.ToLower(path), strings.ToLower(parent)
	return path == parent || strings.HasPrefix(path, parent+`\`)
}

func isLinkage(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), `\linkage`)
}

func hasServiceKey(keys []regKey, t nicTarget) bool {
	want := strings.ToLower(prefix + `\` + t.ControlSet + `\Services\` + t.Service)
	for _, k := range keys {
		if strings.ToLower(k.Path) == want {
			return true
		}
	}
	return false
}

func rewriteClass(k regKey, donor donorNIC, t nicTarget, r rewriter) regKey {
	k.Path = retargetClassPath(swapControlSet(k.Path, donor.ControlSet, t.ControlSet), donor.ClassKey, t.ClassKey)
	k.Values = r.values(k.Values)
	if strings.EqualFold(k.Path, prefix+`\`+t.ControlSet+`\`+classRoot+`\`+t.ClassKey) {
		mac := formatHex(macBytes(t.MAC))
		k.set("DriverDesc", quoteReg(t.Description))
		k.set("InfPath", quoteReg(t.InfFile))
		k.set("InfSection", quoteReg(t.Section))
		k.set("MatchingDeviceId", quoteReg(t.MatchingID))
		k.set("ComponentId", quoteReg(t.MatchingID))
		k.set("NetCfgInstanceId", quoteReg(t.GUID))
		k.set("NetLuidIndex", fmt.Sprintf("dword:%08x", r.luid))
		k.set("DeviceInstanceID", "hex(1):"+utf16Hex(t.Instance))
		k.set("CurrentAddress", "hex:"+mac)
		k.set("PermanentAddress", "hex:"+mac)
	}
	return k
}

func rewriteEnum(k regKey, donor donorNIC, t nicTarget, r rewriter, cross bool) regKey {
	k.Path = swapControlSet(k.Path, donor.ControlSet, t.ControlSet)
	k.Path = replaceFold(k.Path, donor.Instance, t.Instance)
	k.Values = r.values(k.Values)
	if strings.HasSuffix(strings.ToLower(k.Path), strings.ToLower(`\Enum\`+t.Instance)) {
		if cross {
			// 这几个值由总线按插槽报上来，样本网卡的不对；新设备没有它们，开机枚举时 Windows 自己写。
			for _, name := range []string{"Address", "UINumber", "LocationInformation", "Capabilities", "ContainerID"} {
				k.del(name)
			}
			if t.INF.Manufacturer != "" {
				k.set("Mfg", quoteReg(t.INF.Manufacturer))
			}
		}
		hardware, compatible := pciIDs(t.Instance)
		k.set("HardwareID", multiHex(hardware))
		k.set("CompatibleIDs", multiHex(compatible))
		k.set("DeviceDesc", quoteReg(t.Description))
		k.set("FriendlyName", quoteReg(t.Description))
		if !cross || t.INF.Manufacturer == "" {
			k.del("Mfg")
		}
		k.set("Service", quoteReg(t.Service))
		k.set("Driver", quoteReg(`{4d36e972-e325-11ce-bfc1-08002be10318}\`+t.ClassKey))
	}
	return k
}

func rewriteService(k regKey, donor donorNIC, t nicTarget, r rewriter) regKey {
	k.Path = swapControlSet(k.Path, donor.ControlSet, t.ControlSet)
	k.Path = replaceServiceSegment(k.Path, donor.Service, t.Service)
	k.Values = r.values(k.Values)
	if strings.EqualFold(k.Path, prefix+`\`+t.ControlSet+`\Services\`+t.Service) {
		setBootService(&k, t)
		if display := firstNonEmpty(t.DisplayName, t.Description); display != "" {
			k.set("DisplayName", quoteReg(display))
		}
		if !strings.EqualFold(donor.Service, t.Service) {
			// VMware Tools 留下的安装记录和驱动运行时写的版本号只属于样本网卡的驱动。
			for _, name := range []string{"vwdk.installers", "DriverMajorVersion", "DriverMinorVersion", "NdisMajorVersion", "NdisMinorVersion"} {
				k.del(name)
			}
		}
	}
	return k
}

func setBootService(k *regKey, t nicTarget) {
	// INF 通常按需启动；iSCSI 系统盘必须在内核挂盘之前加载目标网卡。
	k.set("Start", "dword:00000000")
	k.set("BootFlags", "dword:00000001")
	k.set("ImagePath", "hex(2):"+utf16Hex(`\SystemRoot\System32\drivers\`+t.SysFile))
}

func rewriteLinkage(k regKey, donor donorNIC, t nicTarget) regKey {
	k.Path = swapControlSet(k.Path, donor.ControlSet, t.ControlSet)
	for i, v := range k.Values {
		kind, body, ok := splitHex(v.Raw)
		if !ok || kind != "hex(7)" {
			continue
		}
		raw, err := parseHexBytes(body)
		if err != nil {
			continue
		}
		k.Values[i].Raw = "hex(7):" + formatHex(appendGUID(raw, donor.GUID, t.GUID))
	}
	return k
}

// 样本网卡的 DHCP 租约和静态地址属于那台机器，新网卡从头取地址。
var leaseValue = regexp.MustCompile(`(?i)^(dhcp.*|lease.*|t1|t2|addresstype|isservernapaware|ipaddress|subnetmask|defaultgateway|defaultgatewaymetric)$`)

func rewriteGUIDKey(k regKey, donor donorNIC, t nicTarget, r rewriter) regKey {
	k.Path = swapControlSet(k.Path, donor.ControlSet, t.ControlSet)
	k.Path = replaceFold(k.Path, donor.GUID, t.GUID)
	k.Values = r.values(k.Values)
	low := strings.ToLower(k.Path)
	switch {
	case strings.HasSuffix(low, strings.ToLower(`\Control\NetworkSetup2\Interfaces\`+t.GUID+`\Kernel`)):
		k.set("NetLuidIndex", fmt.Sprintf("dword:%08x", r.luid))
		k.set("IfDescr", quoteReg(t.Description))
	case strings.HasSuffix(low, strings.ToLower(`\Services\Tcpip\Parameters\Interfaces\`+t.GUID)):
		kept := k.Values[:0]
		for _, v := range k.Values {
			if !leaseValue.MatchString(v.Name) {
				kept = append(kept, v)
			}
		}
		k.Values = kept
	}
	return k
}

func valueHasGUID(k regKey, guid string) bool {
	for _, v := range k.Values {
		if strings.Contains(strings.ToLower(v.Raw), strings.ToLower(guid)) {
			return true
		}
		_, body, ok := splitHex(v.Raw)
		if !ok {
			continue
		}
		b, err := parseHexBytes(body)
		if err != nil {
			continue
		}
		if bytes.Contains(bytes.ToLower(b), bytes.ToLower(utf16Raw(guid))) {
			return true
		}
	}
	return false
}

func appendGUID(b []byte, oldGUID, newGUID string) []byte {
	elems := decodeMulti(b)
	seen := map[string]bool{}
	for _, e := range elems {
		seen[strings.ToLower(e)] = true
	}
	var extra []string
	for _, e := range elems {
		if !strings.Contains(strings.ToLower(e), strings.ToLower(oldGUID)) {
			continue
		}
		n := replaceFold(e, oldGUID, newGUID)
		if !seen[strings.ToLower(n)] {
			extra = append(extra, n)
			seen[strings.ToLower(n)] = true
		}
	}
	return encodeMulti(append(elems, extra...))
}

func retargetClassPath(path, oldKey, newKey string) string {
	marker := `{4d36e972-e325-11ce-bfc1-08002be10318}\`
	lower := strings.ToLower(path)
	i := strings.Index(lower, marker)
	if i < 0 {
		return path
	}
	rest := path[i+len(marker):]
	seg, tail, _ := strings.Cut(rest, `\`)
	if !strings.EqualFold(seg, oldKey) {
		return path
	}
	head := path[:i+len(marker)]
	if tail == "" {
		return head + newKey
	}
	return head + newKey + `\` + tail
}

func swapControlSet(path, oldCS, newCS string) string {
	if oldCS == "" || strings.EqualFold(oldCS, newCS) {
		return path
	}
	return replaceFold(path, `\SYSTEM\`+oldCS+`\`, `\SYSTEM\`+newCS+`\`)
}

func replaceServiceSegment(path, oldName, newName string) string {
	if strings.EqualFold(oldName, newName) {
		return path
	}
	lower := strings.ToLower(path)
	needle := `\services\` + strings.ToLower(oldName)
	i := strings.Index(lower, needle)
	if i < 0 {
		return path
	}
	end := i + len(needle)
	if end != len(path) && path[end] != '\\' {
		return path
	}
	return path[:i] + `\Services\` + newName + path[end:]
}

func replaceFold(s, old, new string) string {
	if old == "" {
		return s
	}
	lower := strings.ToLower(s)
	oldL := strings.ToLower(old)
	var b strings.Builder
	for {
		i := strings.Index(lower, oldL)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(new)
		s = s[i+len(old):]
		lower = lower[i+len(old):]
	}
}

func (k *regKey) get(name string) string {
	for _, v := range k.Values {
		if strings.EqualFold(v.Name, name) {
			return v.Raw
		}
	}
	return ""
}

func (k *regKey) del(name string) {
	kept := k.Values[:0]
	for _, v := range k.Values {
		if !strings.EqualFold(v.Name, name) {
			kept = append(kept, v)
		}
	}
	k.Values = kept
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (k *regKey) set(name, raw string) {
	for i := range k.Values {
		if strings.EqualFold(k.Values[i].Name, name) {
			k.Values[i].Raw = raw
			return
		}
	}
	k.Values = append(k.Values, regValue{Name: name, Raw: raw})
}

func parseReg(text string) []regKey {
	text = continuation.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	var keys []regKey
	var cur *regKey
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Windows Registry") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			keys = append(keys, regKey{Path: line[1 : len(line)-1]})
			cur = &keys[len(keys)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "@=") {
			cur.Values = append(cur.Values, regValue{Name: "@", Raw: line[2:]})
			continue
		}
		if strings.HasPrefix(line, `"`) {
			if i := strings.Index(line, `"=`); i > 0 {
				cur.Values = append(cur.Values, regValue{Name: line[1:i], Raw: line[i+2:]})
			}
		}
	}
	return keys
}

func formatReg(keys []regKey) string {
	var b strings.Builder
	b.WriteString("Windows Registry Editor Version 5.00\n")
	for _, k := range keys {
		b.WriteString("\n[")
		b.WriteString(k.Path)
		b.WriteString("]\n")
		for _, v := range k.Values {
			line := "@=" + v.Raw
			if v.Name != "@" {
				line = `"` + v.Name + `"=` + v.Raw
			}
			b.WriteString(wrapRegLine(line))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func wrapRegLine(line string) string {
	if len(line) <= 3500 {
		return line
	}
	var b strings.Builder
	for len(line) > 3500 {
		cut := 3500
		if i := strings.LastIndex(line[:cut], ","); i > 0 {
			cut = i + 1
		}
		b.WriteString(line[:cut])
		b.WriteString("\\\n  ")
		line = line[cut:]
	}
	b.WriteString(line)
	return b.String()
}

func quoteReg(s string) string {
	// hivex treats quoted UTF-8 as individual bytes; explicit UTF-16 keeps non-ASCII strings intact.
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "hex(1):" + utf16Hex(s)
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// hiveString 读取 hivex 导出的字符串。REG_SZ 是带引号的文本，这张镜像里的
// DeviceInstanceID、NetCfgInstanceId、DriverDesc、Service 实际是 hex(1) 的 UTF-16。
func hiveString(raw string) string {
	return decodeRegString(raw)
}

func unquoteReg(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' {
		return ""
	}
	body := raw[1:]
	if i := strings.LastIndex(body, `"`); i >= 0 {
		body = body[:i]
	}
	return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(body)
}

func decodeRegString(raw string) string {
	if s := unquoteReg(raw); s != "" {
		return s
	}
	kind, body, ok := splitHex(raw)
	if !ok || (kind != "hex(1)" && kind != "hex(2)") {
		return ""
	}
	b, err := parseHexBytes(body)
	if err != nil {
		return ""
	}
	return strings.TrimRight(decodeUTF16(b), "\x00")
}

func splitHex(raw string) (kind, body string, ok bool) {
	if !strings.HasPrefix(raw, "hex") {
		return "", "", false
	}
	i := strings.IndexByte(raw, ':')
	if i < 0 {
		return "", "", false
	}
	return raw[:i], raw[i+1:], true
}

func parseHexBytes(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]byte, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return nil, err
		}
		out = append(out, byte(v))
	}
	return out, nil
}

func formatHex(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02x", c)
	}
	return strings.Join(parts, ",")
}

func utf16Raw(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func utf16Hex(s string) string {
	return formatHex(append(utf16Raw(s), 0, 0))
}

func decodeUTF16(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	return string(utf16.Decode(u))
}

func decodeMulti(b []byte) []string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	var out []string
	var cur []uint16
	for i := 0; i+1 < len(b); i += 2 {
		u := uint16(b[i]) | uint16(b[i+1])<<8
		if u != 0 {
			cur = append(cur, u)
			continue
		}
		if len(cur) == 0 {
			break
		}
		out = append(out, string(utf16.Decode(cur)))
		cur = nil
	}
	return out
}

func encodeMulti(elems []string) []byte {
	var b []byte
	for _, s := range elems {
		b = append(b, utf16Raw(s)...)
		b = append(b, 0, 0)
	}
	b = append(b, 0, 0)
	return b
}

func macBytes(mac string) []byte {
	b := make([]byte, 6)
	for i := 0; i < 6; i++ {
		v, _ := strconv.ParseUint(mac[i*2:i*2+2], 16, 8)
		b[i] = byte(v)
	}
	return b
}

func stageHive(ctx context.Context, hive string) (name, cs, classKey string, err error) {
	original, err := os.ReadFile(hive)
	if err != nil {
		return "", "", "", err
	}
	if err := ValidateHeader(original); err != nil {
		return "", "", "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(hive), "ndtemplate-SYSTEM-*")
	if err != nil {
		return "", "", "", err
	}
	name = tmp.Name()
	if _, err = tmp.Write(original); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", "", "", err
	}
	if err = tmp.Close(); err != nil {
		os.Remove(name)
		return "", "", "", err
	}
	if cs, err = controlSet(ctx, name); err != nil {
		os.Remove(name)
		return "", "", "", err
	}
	classes, err := export(ctx, name, cs+"\\"+classRoot)
	if err != nil {
		os.Remove(name)
		return "", "", "", err
	}
	if classKey, err = freeClassKey(classes); err != nil {
		os.Remove(name)
		return "", "", "", err
	}
	return name, cs, classKey, nil
}

func commitMerged(ctx context.Context, hive, reg, instance, cs, classKey string) error {
	pointer := utf16Hex(instance)
	patch := reg + "\n[" + prefix + "\\" + cs + "\\" + classRoot + "\\" + classKey + "]\n\"DeviceInstanceID\"=hex(1):" + pointer + "\n"
	f, err := os.CreateTemp(filepath.Dir(hive), "ndtemplate-*.reg")
	if err != nil {
		return err
	}
	regname := f.Name()
	defer os.Remove(regname)
	if _, err = f.WriteString(patch); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err := run(ctx, "--merge", "--prefix", prefix, hive, regname); err != nil {
		return err
	}
	if err := verifyMerged(ctx, hive, patch, cs); err != nil {
		return err
	}
	b, err := os.ReadFile(hive)
	if err != nil {
		return err
	}
	return ValidateHeader(b)
}
