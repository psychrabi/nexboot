package identity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/winreg"
)

// SuffixVMwareMAC 表示设备实例末段由 MAC 推出（VMware 虚拟网卡）：前 3 字节 + FFFF + 后 3 字节 + 00。
const SuffixVMwareMAC = "vmware-mac"

const (
	// 同一机型有这么多台机器模板失败，就整型号停用。
	templateBreakerMachines = 2
)

// TemplateEntry 是允许名单里的一条：只有在这里登记、并在真机上开机通过的网卡才会走模板。
// 名单默认是空的；失败的机器会停止普通启动，操作者可在机型页完成 WinPE 完整适配。
type TemplateEntry struct {
	Vendor, Device string // 小写 4 位十六进制
	Manufacturer   string // 机型厂商包含该子串（不分大小写），空表示不限
	InstancePrefix string // 设备实例前缀，含末尾反斜杠
	SuffixRule     string // 末段怎么由 MAC 推出；空表示只能靠取证 WinPE 读
	Reg            string // 注册表模板，占位符见 winreg.RenderTemplate。空且 Generate 为真时由镜像和驱动文件现写
	BuiltinDriver  bool   // 驱动随系统自带；否则要有已上传的驱动包
	DriverHWID     string // 上传驱动包按它的前缀匹配
	Generate       bool   // 不使用事先写好的 reg，挂载镜像后现写
	DriverZip      string // 已上传驱动包的 zip；空表示只在镜像里找
	DriverStamp    string // 驱动包变化时身份层要重建
}

func (e TemplateEntry) matches(p domain.HardwareProfile) bool {
	if !strings.EqualFold(e.Vendor, p.NICVendor) || !strings.EqualFold(e.Device, p.NICDevice) {
		return false
	}
	return e.Manufacturer == "" || strings.Contains(strings.ToLower(p.Manufacturer), strings.ToLower(e.Manufacturer))
}

func (e TemplateEntry) instanceFor(mac string) (string, bool) {
	mac = storage.NormalizeMAC(mac)
	if e.SuffixRule != SuffixVMwareMAC || len(mac) != 12 || e.InstancePrefix == "" {
		return "", false
	}
	return e.InstancePrefix + mac[:6] + "FFFF" + mac[6:] + "00", true
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) templateEntry(p domain.HardwareProfile) (TemplateEntry, bool) {
	for _, e := range s.Templates {
		if e.matches(p) {
			return e, true
		}
	}
	return TemplateEntry{}, false
}

func (s *Service) driverReady(ctx context.Context, e TemplateEntry) (bool, error) {
	if e.BuiltinDriver {
		return true, nil
	}
	packs, err := s.Store.DriverPacks().List(ctx)
	if err != nil {
		return false, err
	}
	for _, pack := range packs {
		if pack.Status != domain.DriverPackEnabled || pack.OSType != domain.OSTypeWindows || !pack.Signed ||
			(pack.Arch != "" && !strings.EqualFold(pack.Arch, "x64") && !strings.EqualFold(pack.Arch, "amd64")) {
			continue
		}
		for _, id := range pack.HWIDs {
			if e.DriverHWID != "" && strings.HasPrefix(strings.ToUpper(id), strings.ToUpper(e.DriverHWID)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// breakerOpen 说明这个机型在该还原点上已有足够多的机器模板失败，不再对它自动套模板。换了新还原点就重新计。
func (s *Service) breakerOpen(ctx context.Context, profileID, reductionID string) (bool, error) {
	all, err := s.Store.TerminalIdentities().List(ctx)
	if err != nil {
		return false, err
	}
	n := 0
	for _, v := range all {
		if v.ProfileID == profileID && v.TemplateFailedRef == reductionID && v.TemplateStrikes == 0 && v.TemplateGen == winreg.GeneratorVersion {
			n++
		}
	}
	return n >= templateBreakerMachines, nil
}

type templatePlan struct {
	Kind     string // direct | template | capture | blocked
	Captured bool   // this terminal has a successful matching capture/adaptation
	Reason   string
	Entry    TemplateEntry
	Instance string
	Cause    error // 确定性生成错误的原始原因，缓存命中后仍需给出正确处理步骤。
}

// planTemplate 只读：机型没有适配记录时，这台机器这次能不能套模板。
func (s *Service) planTemplate(ctx context.Context, t domain.Terminal, reductionID string, v *domain.TerminalIdentity) (templatePlan, error) {
	direct := func(reason string) (templatePlan, error) { return templatePlan{Kind: "direct", Reason: reason}, nil }
	if len(s.Templates) == 0 && s.RestrictTemplates || t.HardwareProfileID == "" {
		return direct("no_entry")
	}
	p, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	if errs.IsNotFound(err) {
		return direct("no_entry")
	} else if err != nil {
		return templatePlan{}, err
	}
	e, ok := s.templateEntry(p)
	if !ok && len(s.Templates) == 0 && !s.RestrictTemplates {
		e, ok = s.builtinEntry(ctx, p)
	}
	if !ok || (e.Reg == "" && !e.Generate) {
		return direct("no_entry")
	}
	instance, derived := e.instanceFor(t.MAC)
	if !derived && v != nil && v.ProfileID == t.HardwareProfileID && (v.Source == "captured" || v.Source == "model") && v.InstanceID != "" {
		instance = v.InstanceID
	}
	owned := false
	if instance == "" {
		if instance, err = s.ownHistoricalInstance(ctx, t, p); err != nil {
			return templatePlan{}, err
		}
		owned = instance != ""
	}
	if instance == "" {
		if instance, err = s.modelInstance(ctx, t, p, ""); err != nil {
			return templatePlan{}, err
		}
	}
	// 失败记录只对当时写进去的那个实例算数；实例号换了（例如改为取证），旧记录不再挡路。
	if v != nil && v.ProfileID == t.HardwareProfileID && v.TemplateFailedRef == reductionID && instance != "" && strings.EqualFold(v.InstanceID, instance) {
		if v.Source == "model" {
			// 借来的实例号这台用不了，说明插槽和别的机器不一样，给它自己取证。
			return templatePlan{Kind: "capture", Entry: e}, nil
		}
		return templatePlan{Kind: "blocked", Reason: "failed"}, nil
	}
	if instance != "" && s.skipped(presentKey(reductionID, instance)) {
		return direct("installed")
	}
	if open, err := s.breakerOpen(ctx, p.ID, reductionID); err != nil {
		return templatePlan{}, err
	} else if open {
		return templatePlan{Kind: "blocked", Reason: "breaker"}, nil
	}
	if ready, err := s.driverReady(ctx, e); err != nil {
		return templatePlan{}, err
	} else if !ready {
		return direct("no_driver")
	}
	if e.Generate && e.Reg == "" {
		e.DriverZip, e.DriverStamp = s.driverZip(ctx, e.Vendor, e.Device)
		if s.driverMissed(reductionID, e) {
			return direct("no_driver")
		}
		if cause, blocked := s.skipReason(generationFailureKey(t, reductionID, instance, e)); blocked {
			return templatePlan{Kind: "blocked", Reason: "generation_failed", Cause: cause}, nil
		}
	}
	if instance != "" {
		return templatePlan{Kind: "template", Entry: e, Instance: instance, Captured: owned}, nil
	}
	return templatePlan{Kind: "capture", Entry: e}, nil
}

// modelInstance 找同机型别的机器已经取证到、或真实适配时记下的实例号。只借按插槽位置编的（如 4&bbf9765&0&2088）：
// 同型号、同插槽布局的机器这个号相同；按序列号编的只属于那一台。adapted 非空时优先用适配记录里的。
func (s *Service) modelInstance(ctx context.Context, t domain.Terminal, p domain.HardwareProfile, adapted string) (string, error) {
	// 机型不含主板型号，物理机即便芯片和总线位置相同也不能借用实例号。
	if !strings.Contains(strings.ToLower(p.Manufacturer), "vmware") {
		return "", nil
	}
	prefix := `PCI\VEN_` + strings.ToUpper(p.NICVendor) + `&DEV_` + strings.ToUpper(p.NICDevice)
	ok := func(instance string) bool {
		i := strings.LastIndex(instance, `\`)
		return i > 0 && strings.HasPrefix(strings.ToUpper(instance), prefix) && len(strings.Split(instance[i+1:], "&")) == 4
	}
	if ok(adapted) {
		return adapted, nil
	}
	all, err := s.Store.TerminalIdentities().List(ctx)
	if err != nil {
		return "", err
	}
	var best domain.TerminalIdentity
	for _, v := range all {
		if v.TerminalID != t.ID && v.ProfileID == p.ID && v.Source == "captured" && ok(v.InstanceID) && v.UpdatedAt.After(best.UpdatedAt) {
			best = v
		}
	}
	if best.InstanceID != "" {
		return best.InstanceID, nil
	}
	adaptations, err := s.Store.ModelAdaptations().List(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range adaptations {
		if a.ProfileID == p.ID && ok(a.TemplateInstanceID) {
			return a.TemplateInstanceID, nil
		}
	}
	return "", nil
}

// templateGUID 由 MAC 和模板内容决定：同一台机器同一份模板永远得到同一个 GUID，身份层才能复用。
func templateGUID(mac string, e TemplateEntry) string {
	sum := sha256.Sum256([]byte(storage.NormalizeMAC(mac) + "/" + e.Vendor + e.Device + "/" + e.Reg))
	h := fmt.Sprintf("%x", sum[:16])
	return "{" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32] + "}"
}

// 这两块是 VMware 的 PCIe 虚拟网卡，实例号末段由 MAC 推出，前缀是它们在 VMware 上的固定 PCI ID。
// E1000 是老式 PCI 设备，Windows 按插槽位置编号（如 4&bbf9765&0&2088），和其他网卡一样先取证再写模板。
func vmwareInstancePrefix(vendor, device string) (string, bool) {
	switch strings.ToLower(vendor) + ":" + strings.ToLower(device) {
	case "8086:10d3":
		return `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\`, true
	case "15ad:07b0":
		return `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\`, true
	default:
		return "", false
	}
}

func (s *Service) builtinEntry(_ context.Context, p domain.HardwareProfile) (TemplateEntry, bool) {
	ven, dev := strings.ToLower(p.NICVendor), strings.ToLower(p.NICDevice)
	if !pciID.MatchString(ven) || !pciID.MatchString(dev) {
		return TemplateEntry{}, false
	}
	e := TemplateEntry{Vendor: ven, Device: dev, Generate: true, BuiltinDriver: true}
	if strings.Contains(strings.ToLower(p.Manufacturer), "vmware") {
		if prefix, ok := vmwareInstancePrefix(ven, dev); ok {
			e.InstancePrefix, e.SuffixRule = prefix, SuffixVMwareMAC
		}
	}
	return e, true
}

func (s *Service) driverZip(ctx context.Context, vendor, device string) (string, string) {
	packs, err := s.Store.DriverPacks().List(ctx)
	if err != nil {
		return "", "inbox"
	}
	want := `PCI\VEN_` + strings.ToUpper(vendor) + `&DEV_` + strings.ToUpper(device)
	var best domain.DriverPack
	bestLen := 0
	for _, pack := range packs {
		if pack.Status != domain.DriverPackEnabled || pack.OSType != domain.OSTypeWindows || !pack.Signed ||
			(pack.Arch != "" && !strings.EqualFold(pack.Arch, "x64") && !strings.EqualFold(pack.Arch, "amd64")) {
			continue
		}
		for _, id := range pack.HWIDs {
			id = strings.ToUpper(id)
			if strings.HasPrefix(id, want) && len(id) > bestLen {
				best, bestLen = pack, len(id)
			}
		}
	}
	if best.ID == "" || best.StoragePath == "" {
		return "", "inbox"
	}
	stamp := best.ID
	if st, err := os.Stat(best.StoragePath); err == nil {
		stamp += fmt.Sprintf("/%d/%d", st.Size(), st.ModTime().UnixNano())
	}
	return best.StoragePath, stamp
}

func templateStamp(t domain.Terminal, serverID, reductionID, instance string, e TemplateEntry) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%s/%s/%#v", serverID, reductionID, t.HardwareProfileID, instance, storage.NormalizeMAC(t.MAC), winreg.GeneratorVersion, e)))
	return fmt.Sprintf("%x", sum)
}

func (s *Service) templateLayerStale(ctx context.Context, t domain.Terminal, v domain.TerminalIdentity, reductionID, serverID string) (bool, error) {
	if _, err := s.Store.ModelAdaptations().ForProfileReduction(ctx, t.HardwareProfileID, reductionID); err == nil {
		return false, nil
	} else if !errs.IsNotFound(err) {
		return false, err
	}
	state := s.bootState(t, v, reductionID, false)
	plan, err := s.planTemplate(ctx, t, reductionID, &state)
	if err != nil || plan.Kind != "template" {
		return false, err
	}
	stamp, ok := s.layerStamps.Load(t.ID)
	return v.TemplateGen != winreg.GeneratorVersion || !ok || stamp != templateStamp(t, serverID, reductionID, plan.Instance, plan.Entry), nil
}

func (s *Service) missKey(reductionID string, e TemplateEntry) string {
	return reductionID + "/" + strings.ToLower(e.Vendor) + "/" + strings.ToLower(e.Device) + "/" + e.DriverStamp + "/" + winreg.GeneratorVersion
}

func generationFailureKey(t domain.Terminal, reductionID, instance string, e TemplateEntry) string {
	// 芯片相同不代表同一子型号；完整硬件 ID 和机型位置都纳入失败隔离。
	if i := strings.LastIndex(instance, `\`); i >= 0 {
		instance = instance[:i]
	}
	t.MAC = ""
	return "generation/" + templateStamp(t, "", reductionID, instance, e)
}

// presentKey 记下「这个还原点里已经装着这台机器的网卡」，同一进程里不再为它挂盘检查。
func presentKey(reductionID, instance string) string {
	return reductionID + "/present/" + strings.ToUpper(instance)
}

func (s *Service) driverMissed(reductionID string, e TemplateEntry) bool {
	return s.skipped(s.missKey(reductionID, e))
}

func (s *Service) markDriverMiss(reductionID string, e TemplateEntry) {
	s.markSkipped(s.missKey(reductionID, e), nil)
}

func (s *Service) skipped(key string) bool {
	_, ok := s.skipReason(key)
	return ok
}

func (s *Service) skipReason(key string) (error, bool) {
	s.missMu.Lock()
	defer s.missMu.Unlock()
	cause, ok := s.driverMiss[key]
	return cause, ok
}

func (s *Service) markSkipped(key string, cause error) {
	s.missMu.Lock()
	defer s.missMu.Unlock()
	if s.driverMiss == nil {
		s.driverMiss = map[string]error{}
	}
	s.driverMiss[key] = cause
}

// bootState 同时供预览和开机使用；后台只更新规则，不把预建当成一次开机。
func (s *Service) bootState(t domain.Terminal, v domain.TerminalIdentity, reductionID string, boot bool) domain.TerminalIdentity {
	if v.TemplateGen != winreg.GeneratorVersion {
		v.TemplateBootRef, v.TemplateBootAt, v.TemplateOKRef, v.TemplateFailedRef, v.TemplateStrikes = "", time.Time{}, "", "", 0
		v.TemplateGen = winreg.GeneratorVersion
	}
	// Legacy positive strikes were inferred only from missing Windows callbacks.
	// Their absence cannot prove a failed driver, including in read-only previews.
	if v.TemplateStrikes > 0 {
		v.TemplateStrikes, v.TemplateFailedRef = 0, ""
	}
	return v
}

func (s *Service) updateIdentity(ctx context.Context, v *domain.TerminalIdentity) error {
	v.UpdatedAt = time.Now().UTC()
	return s.Store.TerminalIdentities().Update(ctx, *v)
}
