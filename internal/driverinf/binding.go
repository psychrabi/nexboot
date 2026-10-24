package driverinf

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// NICBinding 是一份网卡 INF 里，某块设备对应的安装节、服务，以及 Windows 安装时写进驱动键和设备键的内容。
type NICBinding struct {
	Section     string // 安装节名，写入 InfSection
	MatchingID  string // INF 里配上的那个硬件 ID，写入 MatchingDeviceId
	Service     string
	SysFile     string // 服务二进制的文件名，不含目录
	Description string // 设备名，写进 DriverDesc、DeviceDesc
	DisplayName string // 服务显示名，INF 没写时同设备名
	StartType   int

	Provider, DriverDate, DriverVersion, Manufacturer string
	Characteristics                                   uint32
	BusType                                           string
	IfType, MediaType, PhysicalMediaType              uint32
	DriverReg                                         []RegEntry // 安装节 AddReg 里写到驱动键（类实例）下的
	DeviceReg                                         []RegEntry // .HW 节 AddReg 里写到设备键（Device Parameters）下的
	ServiceReg                                        []RegEntry // 所选服务安装节 AddReg，HKR 相对目标服务键
}

type RegKind int

const (
	RegSZ RegKind = iota
	RegExpandSZ
	RegMultiSZ
	RegDword
	RegBinary
	RegKeyOnly
)

// RegEntry 是 AddReg 里以 HKR 开头的一行，Key 相对所在安装上下文的驱动键、设备键或服务键。
type RegEntry struct {
	Key, Name string
	Kind      RegKind
	Strings   []string
	Dword     uint32
	Binary    []byte
}

// Binding 从 INF 里找出这块设备的安装节和服务。ids 按 Windows 的顺序给出设备的硬件 ID 和兼容 ID，
// 越靠前越具体，INF 里配上越靠前的那个就用哪个；只给一个 VEN/DEV 时，INF 里更细的子系统 ID 也算命中。
// 只认 Net 类、x64 型号节。
func Binding(data []byte, ids ...string) (NICBinding, error) {
	text, err := decode(data)
	if err != nil {
		return NICBinding{}, err
	}
	sections := parseSections(text)
	if len(sections) == 0 {
		return NICBinding{}, fmt.Errorf("不是有效的驱动文件")
	}
	table := stringsTable(sections)
	resolve := func(v string) string { return replaceTokens(v, table) }
	var b NICBinding
	className := ""
	for _, e := range sections["version"] {
		switch strings.ToLower(e.key) {
		case "class":
			className = resolve(first(e.values))
		case "provider":
			b.Provider = resolve(first(e.values))
		case "driverver":
			if len(e.values) > 0 {
				b.DriverDate = windowsDate(e.values[0])
			}
			if len(e.values) > 1 {
				b.DriverVersion = strings.TrimSpace(e.values[1])
			}
		}
	}
	if !strings.EqualFold(className, "Net") {
		return NICBinding{}, fmt.Errorf("驱动类不是 Net")
	}
	if len(ids) == 0 {
		return NICBinding{}, fmt.Errorf("没有设备硬件 ID")
	}
	for i := range ids {
		ids[i] = strings.ToUpper(strings.TrimSpace(ids[i]))
	}
	bases := map[string]string{}
	for _, e := range sections["manufacturer"] {
		if len(e.values) > 0 {
			if base := strings.ToLower(strings.TrimSpace(resolve(e.values[0]))); base != "" {
				bases[base] = resolve(e.key)
			}
		}
	}
	var names []string
	for base := range bases {
		names = append(names, base)
	}
	best := struct {
		rank, score     int
		section, desc   string
		matched, vendor string
	}{rank: len(ids) + 1}
	for name, entries := range sections {
		base := ""
		for _, candidate := range names {
			if _, ok := x64SectionBuild(name, []string{candidate}); ok {
				base = candidate
			}
		}
		if base == "" {
			continue
		}
		for _, e := range entries {
			if len(e.values) < 2 {
				continue
			}
			for _, raw := range e.values[1:] {
				id := strings.ToUpper(strings.TrimSpace(resolve(raw)))
				rank := len(ids)
				for i, want := range ids {
					if id == want {
						rank = i
						break
					}
				}
				// 只给 VEN/DEV 时沿用宽松匹配：INF 里带子系统号的更细 ID 也算。
				if rank == len(ids) && (len(ids) != 1 || !hwidListed(id, ids[0]) || len(id) < best.score) {
					continue
				}
				if rank > best.rank || (rank == best.rank && rank < len(ids)) {
					continue
				}
				best.rank, best.score = rank, len(id)
				best.section, best.desc, best.matched, best.vendor = strings.TrimSpace(resolve(e.values[0])), strings.TrimSpace(resolve(e.key)), id, bases[base]
			}
		}
	}
	if best.section == "" {
		return NICBinding{}, fmt.Errorf("驱动文件里没有 %s", ids[0])
	}
	svcName, svcSection := addServiceFor(sections, best.section)
	svcName = resolve(svcName)
	svcSection = resolve(svcSection)
	if svcName == "" {
		return NICBinding{}, fmt.Errorf("安装节 %s 没有 AddService", best.section)
	}
	svc := sections[strings.ToLower(svcSection)]
	binary := resolve(serviceValue(svc, "servicebinary"))
	if binary == "" {
		return NICBinding{}, fmt.Errorf("服务 %s 没有 ServiceBinary", svcName)
	}
	start := 3
	if raw := serviceValue(svc, "starttype"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			start = n
		}
	}
	display := best.desc
	if v := strings.TrimSpace(resolve(serviceValue(svc, "displayname"))); v != "" {
		display = v
	}
	b.Section, b.MatchingID, b.Service, b.SysFile, b.Description, b.DisplayName, b.StartType = best.section, best.matched, svcName, path.Base(strings.ReplaceAll(binary, `\`, "/")), best.desc, display, start
	b.Manufacturer = best.vendor
	for _, e := range svc {
		if strings.EqualFold(e.key, "addreg") {
			b.ServiceReg = append(b.ServiceReg, addReg(sections, e.values, resolve)...)
		}
	}
	install := decoratedSection(sections, best.section)
	for _, e := range sections[install] {
		v := resolve(first(e.values))
		switch strings.ToLower(e.key) {
		case "characteristics":
			b.Characteristics = parseNumber(v)
		case "bustype":
			b.BusType = v
		case "*iftype":
			b.IfType = parseNumber(v)
		case "*mediatype":
			b.MediaType = parseNumber(v)
		case "*physicalmediatype":
			b.PhysicalMediaType = parseNumber(v)
		case "*ifconnectorpresent", "*connectiontype", "*directiontype", "*accesstype", "*hardwareloopback":
			n, err := strconv.ParseUint(strings.TrimSpace(v), 0, 32)
			if err != nil {
				return NICBinding{}, fmt.Errorf("安装节 %s 的 %s 不是有效 DWORD：%q", best.section, e.key, v)
			}
			b.DriverReg = append(b.DriverReg, RegEntry{Name: e.key, Kind: RegDword, Dword: uint32(n)})
		case "addreg":
			b.DriverReg = append(b.DriverReg, addReg(sections, e.values, resolve)...)
		}
	}
	for _, e := range sections[install+".hw"] {
		if strings.EqualFold(e.key, "addreg") {
			b.DeviceReg = append(b.DeviceReg, addReg(sections, e.values, resolve)...)
		}
	}
	return b, nil
}

// decoratedSection 按 Windows 的选法取安装节：先 .NTamd64，再 .NT，最后不带修饰的。
func decoratedSection(sections map[string][]entry, install string) string {
	install = strings.ToLower(install)
	for _, name := range []string{install + ".ntamd64", install + ".nt"} {
		if _, ok := sections[name]; ok {
			return name
		}
	}
	return install
}

// addReg 读 AddReg 引用的各节，只收 HKR 开头的行；其他根键不属于这块设备。
func addReg(sections map[string][]entry, names []string, resolve func(string) string) []RegEntry {
	var out []RegEntry
	for _, name := range names {
		for _, e := range sections[strings.ToLower(strings.TrimSpace(name))] {
			if e.key != "" || len(e.values) < 1 || !strings.EqualFold(e.values[0], "HKR") {
				continue
			}
			field := func(i int) string {
				if i < len(e.values) {
					return resolve(e.values[i])
				}
				return ""
			}
			r := RegEntry{Key: field(1), Name: field(2)}
			flags := parseNumber(field(3))
			data := []string{}
			for i := 4; i < len(e.values); i++ {
				data = append(data, resolve(e.values[i]))
			}
			switch {
			case flags&0x10 != 0:
				r.Kind = RegKeyOnly
			case flags&0xffff0001 == 0x00010001:
				r.Kind = RegDword
				if len(data) == 1 {
					r.Dword = parseNumber(data[0])
				} else {
					for i, v := range data {
						if i < 4 {
							r.Dword |= parseNumber("0x"+v) << (8 * i)
						}
					}
				}
			case flags&0xffff0001 == 0x00010000:
				r.Kind, r.Strings = RegMultiSZ, data
			case flags&0xffff0001 == 0x00020000:
				r.Kind, r.Strings = RegExpandSZ, []string{first(data)}
			case flags&0xffff0001 == 0x00000001:
				r.Kind = RegBinary
				for _, v := range data {
					r.Binary = append(r.Binary, byte(parseNumber("0x"+v)))
				}
			case flags&0xffff0001 == 0:
				r.Kind, r.Strings = RegSZ, []string{first(data)}
			default:
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

func parseNumber(v string) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 0, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// windowsDate 把 DriverVer 的 03/23/2010 写成 Windows 驱动键里的 3-23-2010。
func windowsDate(v string) string {
	parts := strings.Split(strings.TrimSpace(v), "/")
	if len(parts) != 3 {
		return strings.TrimSpace(v)
	}
	for i, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			parts[i] = strconv.Itoa(n)
		}
	}
	return strings.Join(parts, "-")
}

func hwidListed(listed, want string) bool {
	if listed == "" || want == "" {
		return false
	}
	return listed == want || strings.HasPrefix(listed, want+"&") || strings.HasPrefix(want, listed+"&")
}

func addServiceFor(sections map[string][]entry, install string) (name, section string) {
	install = strings.ToLower(install)
	// Windows 把服务节写成 DDInstall.Services，或再加 .NT / .NTamd64。
	for _, suffix := range []string{".services", ".nt.services", ".ntamd64.services"} {
		if name, section := addService(sections[install+suffix]); name != "" {
			return name, section
		}
	}
	return "", ""
}

func addService(entries []entry) (name, section string) {
	for _, e := range entries {
		if !strings.EqualFold(e.key, "addservice") || len(e.values) == 0 {
			continue
		}
		name = strings.TrimSpace(e.values[0])
		section = name
		if len(e.values) >= 3 && strings.TrimSpace(e.values[2]) != "" {
			section = strings.TrimSpace(e.values[2])
		}
		return name, section
	}
	return "", ""
}

func serviceValue(entries []entry, key string) string {
	for _, e := range entries {
		if strings.EqualFold(e.key, key) {
			return first(e.values)
		}
	}
	return ""
}
