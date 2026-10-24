package hwprofile

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// Status 是机型在界面上的状态。
type Status string

// 只有 Windows 开机成功回报算能开机；完整适配写过、从上个还原点沿用的记录都不算。
const (
	StatusOK       Status = "ok"       // 机器用到的每个当前还原点都收到过这个机型的开机成功回报
	StatusFailed   Status = "failed"   // 有机器在当前还原点上没进 Windows
	StatusWaiting  Status = "waiting"  // 还没有开机结果，下次开机自动准备
	StatusAdapting Status = "adapting" // 有进行中的完整适配任务
	StatusIdle     Status = "idle"     // 当前没有 Windows 客户机，只因历史记录或任务列出
)

// failureEvidenceFor 之内的开机失败才算数。
const failureEvidenceFor = 7 * 24 * time.Hour

// PointBrief 是这个机型的机器正在用的一个当前还原点，以及它们在这个点上的开机结果。
type PointBrief struct {
	ConfigID      string `json:"config_id"`
	ConfigName    string `json:"config_name"`
	ReductionID   string `json:"reduction_id"`
	ReductionName string `json:"reduction_name"`
	Terminals     int    `json:"terminals"`
	OK            int    `json:"ok"`
	Failed        int    `json:"failed"`
	Waiting       int    `json:"waiting"`
	Verified      bool   `json:"verified"` // 这个机型在此点上收到过开机成功回报
}

// Failing 是一个分组里开机失败的机型，分组卡片提示用。
type Failing struct {
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	ConfigID    string `json:"config_id"` // 失败机器所用的配置，完整适配预选它
	Terminals   int    `json:"terminals"`
}

// TerminalBrief 是机型详情里的一台机器。
type TerminalBrief struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	MAC       string               `json:"mac"`
	IP        string               `json:"ip"`
	GroupID   string               `json:"group_id"`
	GroupName string               `json:"group_name"`
	State     domain.TerminalState `json:"state"`
	Boot      Status               `json:"boot"`              // ok | failed | waiting
	BootAt    *time.Time           `json:"boot_at,omitempty"` // 最近一次成功回报
}

// ReductionBrief 给详情页提供真实目录名称；保留原 Adaptations 供已有调用方使用。
type ReductionBrief struct {
	ID            string                       `json:"id"`
	ImageID       string                       `json:"image_id"`
	ImageName     string                       `json:"image_name"`
	ConfigID      string                       `json:"config_id"`
	ConfigName    string                       `json:"config_name"`
	ReductionID   string                       `json:"reduction_id"`
	ReductionName string                       `json:"reduction_name"`
	Source        domain.ModelAdaptationSource `json:"source"`
	TaskID        string                       `json:"task_id"`
	Current       bool                         `json:"current"`
	CreatedAt     time.Time                    `json:"created_at"`
	Unavailable   string                       `json:"unavailable,omitempty"`
}

// View 是机型页的一行。
type View struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	NICLabel      string                       `json:"nic_label"`
	NICName       string                       `json:"nic_name"`
	NICVendor     string                       `json:"nic_vendor"`
	NICDevice     string                       `json:"nic_device"`
	NICSubsys     string                       `json:"nic_subsys"`
	BusLoc        string                       `json:"bus_loc"`
	Platform      string                       `json:"platform"`
	Manufacturer  string                       `json:"manufacturer"`
	Product       string                       `json:"product"`
	Terminals     int                          `json:"terminals"`
	Status        Status                       `json:"status"`
	Source        domain.ModelAdaptationSource `json:"source,omitempty"` // 只在还原点详情里给：这条记录从哪来
	BootOK        int                          `json:"boot_ok"`
	BootFailed    int                          `json:"boot_failed"`
	BootWaiting   int                          `json:"boot_waiting"`
	Points        []PointBrief                 `json:"points"`
	Adaptations   []domain.ModelAdaptation     `json:"adaptations,omitempty"`
	ReductionList []ReductionBrief             `json:"reduction_list,omitempty"`
	TerminalList  []TerminalBrief              `json:"terminal_list,omitempty"`
	CreatedAt     time.Time                    `json:"created_at"`
}

// TerminalStatus 是客户机详情里的机型字段。
type TerminalStatus struct {
	ProfileID      string               `json:"profile_id"`
	ProfileName    string               `json:"profile_name"`
	NICName        string               `json:"nic_name,omitempty"`
	ConfigID       string               `json:"config_id,omitempty"`
	ReductionID    string               `json:"reduction_id"`
	Status         Status               `json:"status"`
	BootVerifiedAt *time.Time           `json:"boot_verified_at,omitempty"`
	Failure        *TerminalBootFailure `json:"failure,omitempty"`
}

// TerminalBootFailure 是本机当前还原点上的失败依据；缺少 Windows 回报不等于确认蓝屏。
type TerminalBootFailure struct {
	Stage string    `json:"stage"`
	Code  string    `json:"code,omitempty"`
	At    time.Time `json:"at"`
}

// List 返回 Windows 客户机使用、有 Windows 记录或正在适配的机型。
func (s Service) List(ctx context.Context) ([]View, error) {
	w, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(w.profiles))
	for _, p := range w.profiles {
		v := w.view(p)
		if v.Terminals > 0 || w.windowsHistory[p.ID] || w.adapting[p.ID] {
			views = append(views, v)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

// Get 返回一个机型的详情：机器列表与历次适配记录。
func (s Service) Get(ctx context.Context, id string) (View, error) {
	p, err := s.Store.HardwareProfiles().Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	w, err := s.load(ctx)
	if err != nil {
		return View{}, err
	}
	v := w.view(p)
	adaptations, err := s.Store.ModelAdaptations().ByProfile(ctx, id)
	if err != nil {
		return View{}, err
	}
	for _, a := range adaptations {
		windows, err := w.windowsAdaptation(ctx, a)
		if err != nil {
			return View{}, err
		}
		if windows {
			v.Adaptations = append(v.Adaptations, a)
		}
	}
	for _, a := range v.Adaptations {
		r := ReductionBrief{ID: a.ID, ReductionID: a.ReductionID, ConfigID: a.ConfigID, Source: a.Source, TaskID: a.TaskID, CreatedAt: a.CreatedAt}
		red, redOK := w.reductions[a.ReductionID]
		if !redOK {
			r.Unavailable = "还原点已不存在"
		} else {
			r.ReductionName = red.DisplayName
			if r.ReductionName == "" {
				r.ReductionName = red.Name
			}
			r.ConfigID = red.ConfigID
			cfg, cfgOK := w.configs[red.ConfigID]
			if !cfgOK {
				r.Unavailable = "配置已不存在"
			} else {
				r.ConfigName, r.ImageID = cfg.Name, cfg.ImageID
				img, imgOK := w.images[cfg.ImageID]
				if !imgOK {
					r.Unavailable = "镜像已不存在"
				} else {
					r.ImageName = img.Name
					r.Current = cfg.DefaultReductionID != nil && *cfg.DefaultReductionID == red.ID
				}
			}
		}
		v.ReductionList = append(v.ReductionList, r)
	}
	sort.SliceStable(v.ReductionList, func(i, j int) bool { return v.ReductionList[i].CreatedAt.After(v.ReductionList[j].CreatedAt) })
	for _, t := range w.terminalsOf[p.ID] {
		if !w.windowsTerminal(t) {
			continue
		}
		brief := TerminalBrief{ID: t.ID, Name: t.Name, MAC: t.MAC, IP: t.IP, GroupID: t.GroupID, GroupName: w.groups[t.GroupID].Name, State: t.State, Boot: StatusWaiting}
		if red, ok := w.currentReduction(t); ok && red.Status == domain.ReductionStatusReady {
			brief.Boot, brief.BootAt, _ = w.machineBoot(t, p, red)
		}
		v.TerminalList = append(v.TerminalList, brief)
	}
	return v, nil
}

// FailingByGroup 按分组列出有机器开机失败的机型，分组卡片提示用。
func (s Service) FailingByGroup(ctx context.Context) (map[string][]Failing, error) {
	w, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]Failing{}
	for _, p := range w.profiles {
		byGroup, configOf := map[string]int{}, map[string]string{}
		var order []string
		for _, t := range w.terminalsOf[p.ID] {
			red, ok := w.currentReduction(t)
			if !ok || red.Status != domain.ReductionStatusReady {
				continue
			}
			if state, _, _ := w.machineBoot(t, p, red); state != StatusFailed {
				continue
			}
			if byGroup[t.GroupID] == 0 {
				order = append(order, t.GroupID)
				configOf[t.GroupID] = red.ConfigID
			}
			byGroup[t.GroupID]++
		}
		for _, gid := range order {
			out[gid] = append(out[gid], Failing{ProfileID: p.ID, ProfileName: w.name(p), ConfigID: configOf[gid], Terminals: byGroup[gid]})
		}
	}
	return out, nil
}

// ForTerminal 回答这台机器在它分组当前还原点上的开机情况。没开过机的终端没有机型。
func (s Service) ForTerminal(ctx context.Context, t domain.Terminal) (TerminalStatus, error) {
	if t.HardwareProfileID == "" {
		return TerminalStatus{}, nil
	}
	w, err := s.load(ctx)
	if err != nil {
		return TerminalStatus{}, err
	}
	p, ok := w.profileByID[t.HardwareProfileID]
	if !ok {
		return TerminalStatus{}, nil
	}
	out := TerminalStatus{ProfileID: p.ID, ProfileName: w.name(p), NICName: NICName(p.NICVendor, p.NICDevice), Status: StatusWaiting}
	if red, ok := w.currentReduction(t); ok {
		out.ConfigID, out.ReductionID = red.ConfigID, red.ID
		// 成功回报只证明本机最近一次启动；旧点或旧网卡的回报不作为当前点依据。
		out.Status, out.BootVerifiedAt, out.Failure = w.machineBoot(t, p, red)
	}
	return out, nil
}

// world 是一次性读出的目录快照，状态都从它算。
type world struct {
	profiles       []domain.HardwareProfile
	profileByID    map[string]domain.HardwareProfile
	terminalsOf    map[string][]domain.Terminal
	groups         map[string]domain.Group
	configs        map[string]domain.Config
	images         map[string]domain.Image
	reductions     map[string]domain.Reduction
	adapted        map[string]domain.ModelAdaptation
	adapting       map[string]bool
	modelTasks     map[string]string
	tasks          store.TaskRepo
	windowsHistory map[string]bool
	failures       map[string]domain.BootFailure
	identities     map[string]domain.TerminalIdentity
	sameName       map[string]int // 默认名字 → 用这个名字的机型数
	now            time.Time
}

func (s Service) load(ctx context.Context) (*world, error) {
	w := &world{
		profileByID: map[string]domain.HardwareProfile{}, terminalsOf: map[string][]domain.Terminal{},
		groups: map[string]domain.Group{}, configs: map[string]domain.Config{}, images: map[string]domain.Image{},
		reductions: map[string]domain.Reduction{}, adapted: map[string]domain.ModelAdaptation{}, adapting: map[string]bool{},
		modelTasks: map[string]string{}, tasks: s.Store.Tasks(), windowsHistory: map[string]bool{},
		failures: map[string]domain.BootFailure{}, identities: map[string]domain.TerminalIdentity{}, sameName: map[string]int{}, now: s.now(),
	}
	var err error
	if w.profiles, err = s.Store.HardwareProfiles().List(ctx); err != nil {
		return nil, err
	}
	for _, p := range w.profiles {
		w.profileByID[p.ID] = p
		w.sameName[DisplayName(p)]++
	}
	terminals, err := s.Store.Terminals().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range terminals {
		if t.HardwareProfileID != "" {
			w.terminalsOf[t.HardwareProfileID] = append(w.terminalsOf[t.HardwareProfileID], t)
		}
	}
	groups, err := s.Store.Groups().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		w.groups[g.ID] = g
	}
	configs, err := s.Store.Configs().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range configs {
		w.configs[c.ID] = c
	}
	images, err := s.Store.Images().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		w.images[img.ID] = img
	}
	reductions, err := s.Store.Reductions().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range reductions {
		w.reductions[r.ID] = r
	}
	adaptations, err := s.Store.ModelAdaptations().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range adaptations {
		w.adapted[a.ProfileID+"|"+a.ReductionID] = a
	}
	tasks, err := s.Store.Tasks().ListActive(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.Type == domain.TaskTypeModelAdapt {
			w.modelTasks[task.ID] = task.TargetRef
			if task.Status == domain.TaskStatusPending || task.Status == domain.TaskStatusRunning {
				w.adapting[task.TargetRef] = true
			}
		}
	}
	for _, a := range adaptations {
		windows, err := w.windowsAdaptation(ctx, a)
		if err != nil {
			return nil, err
		}
		if windows {
			w.windowsHistory[a.ProfileID] = true
		}
	}
	failures, err := s.Store.BootFailures().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range failures {
		w.failures[f.MAC] = f
	}
	identities, err := s.Store.TerminalIdentities().List(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range identities {
		w.identities[v.TerminalID] = v
	}
	return w, nil
}

// name 是界面上的机型名：同一块网卡在 BIOS 和 UEFI 下是两个机型，默认名字撞了就补上固件；运维改过的名字不动。
func (w *world) name(p domain.HardwareProfile) string {
	name := DisplayName(p)
	if name != DefaultName(p) || w.sameName[name] < 2 {
		return name
	}
	switch p.Platform {
	case "efi":
		return name + " · UEFI"
	case "pcbios":
		return name + " · BIOS"
	}
	return name
}

// currentReduction 是这台机器开机会用的系统盘还原点：配置的应用点，退回分组缓存。Linux 分组不参与。
func (w *world) currentReduction(t domain.Terminal) (domain.Reduction, bool) {
	g, ok := w.groups[t.GroupID]
	if !ok {
		return domain.Reduction{}, false
	}
	if img, ok := w.images[g.SystemImageID]; !ok || img.OSType != domain.OSTypeWindows {
		return domain.Reduction{}, false
	}
	id := g.SystemReductionID
	if cfg, ok := w.configs[g.SystemConfigID]; ok && cfg.DefaultReductionID != nil && *cfg.DefaultReductionID != "" {
		id = *cfg.DefaultReductionID
	}
	red, ok := w.reductions[id]
	return red, ok
}

func (w *world) windowsTerminal(t domain.Terminal) bool {
	g, ok := w.groups[t.GroupID]
	img, imageOK := w.images[g.SystemImageID]
	return ok && imageOK && img.OSType == domain.OSTypeWindows
}

func (w *world) windowsAdaptation(ctx context.Context, a domain.ModelAdaptation) (bool, error) {
	configID := a.ConfigID
	if red, ok := w.reductions[a.ReductionID]; ok {
		configID = red.ConfigID
	}
	if cfg, ok := w.configs[configID]; ok {
		if img, ok := w.images[cfg.ImageID]; ok {
			return img.OSType == domain.OSTypeWindows, nil
		}
	}
	// 目录已删除时，Windows 专用适配来源仍能证明记录范围；未知 observed 不猜 OS。
	if a.Source == domain.AdaptationAdapted || (a.TemplateInstanceID != "" && a.ClassKey != "" && a.NetCfgInstanceID != "") {
		return true, nil
	}
	if a.TaskID == "" {
		return false, nil
	}
	if profileID, cached := w.modelTasks[a.TaskID]; cached {
		return profileID == a.ProfileID, nil
	}
	task, err := w.tasks.Get(ctx, a.TaskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	w.modelTasks[a.TaskID] = ""
	if err == nil && task.Type == domain.TaskTypeModelAdapt {
		w.modelTasks[a.TaskID] = task.TargetRef
	}
	return w.modelTasks[a.TaskID] == a.ProfileID, nil
}

func (w *world) view(p domain.HardwareProfile) View {
	v := View{
		ID: p.ID, Name: w.name(p), NICLabel: NICLabel(p), NICName: NICName(p.NICVendor, p.NICDevice), NICVendor: p.NICVendor, NICDevice: p.NICDevice, NICSubsys: p.NICSubsys,
		BusLoc: FormatBusLoc(p.BusLoc), Platform: p.Platform, Manufacturer: p.Manufacturer, Product: p.Product,
		Points: []PointBrief{}, CreatedAt: p.CreatedAt,
	}
	pointAt := map[string]int{}
	allVerified := true
	for _, t := range w.terminalsOf[p.ID] {
		if !w.windowsTerminal(t) {
			continue
		}
		v.Terminals++
		red, ok := w.currentReduction(t)
		if !ok || red.Status != domain.ReductionStatusReady {
			allVerified = false
			v.BootWaiting++
			continue
		}
		i, seen := pointAt[red.ID]
		if !seen {
			name := red.DisplayName
			if name == "" {
				name = red.Name
			}
			i = len(v.Points)
			pointAt[red.ID] = i
			v.Points = append(v.Points, PointBrief{ConfigID: red.ConfigID, ConfigName: w.configs[red.ConfigID].Name, ReductionID: red.ID, ReductionName: name, Verified: w.verified(p.ID, red.ID)})
		}
		pt := &v.Points[i]
		pt.Terminals++
		state, _, _ := w.machineBoot(t, p, red)
		switch state {
		case StatusOK:
			v.BootOK++
			pt.OK++
		case StatusFailed:
			v.BootFailed++
			pt.Failed++
		default:
			v.BootWaiting++
			pt.Waiting++
		}
		if !pt.Verified {
			allVerified = false
		}
	}
	switch {
	case w.adapting[p.ID]:
		v.Status = StatusAdapting
	case v.BootFailed > 0:
		v.Status = StatusFailed
	case v.Terminals == 0:
		v.Status = StatusIdle
	case allVerified:
		v.Status = StatusOK
	default:
		v.Status = StatusWaiting
	}
	return v
}

// machineBoot 是这台机器在当前还原点上的开机结果。最近一次开机成功回报之后的失败才算数；
// 失败依据（任何阶段）原样带出，但只有没进 Windows 才算这个机型开机失败。
func (w *world) machineBoot(t domain.Terminal, p domain.HardwareProfile, red domain.Reduction) (Status, *time.Time, *TerminalBootFailure) {
	var okAt *time.Time
	if bootedAfter(t) && t.LastBootReductionID == red.ID && t.LastBootProfileID == p.ID {
		okAt = t.LastBootOKAt
	}
	if f, ok := w.failures[storage.NormalizeMAC(t.MAC)]; ok && f.ReductionID == red.ID && f.ProfileID == p.ID &&
		!f.At.After(w.now) && w.now.Sub(f.At) <= failureEvidenceFor && (okAt == nil || okAt.Before(f.At)) {
		failure := &TerminalBootFailure{Stage: f.Stage, Code: f.Code, At: f.At}
		// Historical Windows-stage rows were inferred solely from missing callbacks.
		// Preserve the evidence time without claiming that Windows failed.
		return StatusWaiting, nil, failure
	}
	if okAt != nil {
		return StatusOK, okAt, nil
	}
	if id, ok := w.identities[t.ID]; ok && id.ProfileID == p.ID && id.TemplateFailedRef == red.ID && id.TemplateStrikes == 0 {
		return StatusFailed, nil, &TerminalBootFailure{Stage: BootStageTemplate, At: id.UpdatedAt}
	}
	return StatusWaiting, nil, nil
}

// BootStageTemplate 是「网卡模板连续两次没进系统」的失败依据；不来自 boot_failures。
const BootStageTemplate = "template"

// verified 说明这个机型在该还原点上收到过开机成功回报：普通开机（observed）、
// 本机型某台机器最近一次在此点开机成功，或某台用网卡模板在此点开机成功。
func (w *world) verified(profileID, reductionID string) bool {
	if a, ok := w.adapted[profileID+"|"+reductionID]; ok && a.Source == domain.AdaptationObserved {
		return true
	}
	for _, t := range w.terminalsOf[profileID] {
		if bootedAfter(t) && t.LastBootReductionID == reductionID && t.LastBootProfileID == profileID {
			return true
		}
	}
	for _, id := range w.identities {
		if id.ProfileID == profileID && id.TemplateOKRef == reductionID && (id.TemplateFailedRef != reductionID || id.TemplateStrikes > 0) {
			return true
		}
	}
	return false
}
