package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
	"github.com/tianwei/diskless/internal/winreg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrCaptureStarted = errors.New("terminal identity capture started")

var ErrAdaptationRequired = errors.New("需要 WinPE 完整机型适配")

type Service struct {
	Store   store.Store
	PE      *pe.SessionService
	Place   *place.Router
	Storage storage.ClientHostAgent
	NodeID  string
	Logger  *slog.Logger
	// Templates 非空时只允许这些型号。nil 表示画像里每块 PCI 网卡都按镜像和驱动文件现写模板。
	Templates []TemplateEntry
	// RestrictTemplates 为真且 Templates 为空：名单文件无效，一台也不套模板。
	RestrictTemplates bool
	Now               func() time.Time
	missMu            sync.Mutex
	driverMiss        map[string]error
	layerStamps       sync.Map
}
type View struct {
	Identity *domain.TerminalIdentity `json:"identity"`
	Stale    bool                     `json:"stale"`
	NextBoot NextBoot                 `json:"next_boot"`
}

func ComputedInstance(p domain.HardwareProfile, mac, template string) (string, bool) {
	if !strings.Contains(strings.ToLower(p.Manufacturer), "vmware") {
		return "", false
	}
	prefix, ok := vmwareInstancePrefix(p.NICVendor, p.NICDevice)
	i := strings.LastIndex(template, `\`)
	if !ok || i < 0 || !strings.EqualFold(template[:i+1], prefix) {
		return "", false
	}
	return (TemplateEntry{InstancePrefix: prefix, SuffixRule: SuffixVMwareMAC}).instanceFor(mac)
}

func (s *Service) computedStale(ctx context.Context, t domain.Terminal, v domain.TerminalIdentity) (bool, error) {
	if v.Source != "computed" {
		return false, nil
	}
	if v.ProfileID != t.HardwareProfileID {
		return true, nil
	}
	p, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	if err != nil {
		return false, err
	}
	expected, ok := ComputedInstance(p, t.MAC, v.InstanceID)
	return !ok || !strings.EqualFold(expected, v.InstanceID), nil
}
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	t, err := s.Store.Terminals().Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	v := View{}
	identity, err := s.Store.TerminalIdentities().Get(ctx, id)
	if err != nil && !errs.IsNotFound(err) {
		return v, err
	}
	if err == nil {
		v.Identity = &identity
	}
	v.NextBoot, err = s.nextBoot(ctx, t, v.Identity)
	if err != nil {
		return v, err
	}
	if v.Identity == nil {
		return v, nil
	}
	node := s.NodeID
	if t.StorageServerID != nil {
		node = *t.StorageServerID
	}
	v.Stale = identity.LayerServerID != node || v.NextBoot.ReductionID == "" || identity.LayerReductionRef != v.NextBoot.ReductionID || identity.ProfileID != t.HardwareProfileID
	computedStale, err := s.computedStale(ctx, t, identity)
	if err != nil {
		return v, err
	}
	v.Stale = v.Stale || computedStale
	if v.NextBoot.Kind == "system" && v.NextBoot.Reason != "linux" && v.NextBoot.Reason != "super" {
		templateStale, err := s.templateLayerStale(ctx, t, identity, v.NextBoot.ReductionID, node)
		if err != nil {
			return v, err
		}
		v.Stale = v.Stale || templateStale
	}
	return v, nil
}
func (s *Service) StartCapture(ctx context.Context, id string) (pe.StartResult, error) {
	t, err := s.Store.Terminals().Get(ctx, id)
	if err != nil {
		return pe.StartResult{}, err
	}
	if t.State == domain.TerminalStateOnline || t.IsSuper {
		return pe.StartResult{}, errs.Conflict("请先关闭客户机「" + t.Name + "」并退出超管模式后重新取证")
	}
	g, err := s.Store.Groups().Get(ctx, t.GroupID)
	if err != nil {
		return pe.StartResult{}, err
	}
	cfg, err := s.Store.Configs().Get(ctx, g.SystemConfigID)
	if err != nil {
		return pe.StartResult{}, err
	}
	var driverSession string
	if cfg.DefaultReductionID != nil {
		template, err := s.Store.ModelAdaptations().ForProfileReduction(ctx, t.HardwareProfileID, *cfg.DefaultReductionID)
		if err == nil && template.TaskID != "" {
			sessions, err := s.Store.PESessions().List(ctx)
			if err != nil {
				return pe.StartResult{}, err
			}
			for _, old := range sessions {
				if old.TaskID == template.TaskID {
					driverSession = old.ID
				}
			}
		}
	}
	profile, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	if err != nil {
		return pe.StartResult{}, err
	}
	filesDir, err := s.PE.FilesDir(ctx)
	if err != nil {
		return pe.StartResult{}, err
	}
	res, err := s.PE.StartIdentity(ctx, id)
	if err != nil {
		return pe.StartResult{}, err
	}
	if driverSession != "" {
		if err := s.copyDriverFiles(ctx, driverSession, res.SessionID); err != nil {
			_ = s.PE.Fail(ctx, res.SessionID, "准备客户机网卡取证驱动失败："+err.Error())
			return pe.StartResult{}, err
		}
	}
	packs, err := s.PE.StageUploadedDriver(ctx, "PCI\\VEN_"+profile.NICVendor+"&DEV_"+profile.NICDevice, filepath.Join(filesDir, "pe-sessions", res.SessionID, "drivers"))
	if err != nil {
		_ = s.PE.Fail(ctx, res.SessionID, "准备客户机网卡取证驱动失败："+err.Error())
		return pe.StartResult{}, err
	}
	sess, err := s.Store.PESessions().Get(ctx, res.SessionID)
	if err == nil {
		sess.DriverPackIDs = packs
		sess.State = domain.PEStatePending
		err = s.Store.PESessions().Update(ctx, sess)
	}
	if err != nil {
		_ = s.PE.Fail(ctx, res.SessionID, "记录客户机网卡取证驱动失败："+err.Error())
		return pe.StartResult{}, err
	}
	return res, nil
}
func (s *Service) copyDriverFiles(ctx context.Context, from, to string) error {
	dir, err := s.PE.FilesDir(ctx)
	if err != nil {
		return err
	}
	base := filepath.Join(dir, "pe-sessions", from, "drivers")
	return filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, "pe-sessions", to, "drivers", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0600)
	})
}
func (s *Service) OnDone(ctx context.Context, sess domain.PESession, result pe.PEResult) error {
	if sess.Mode != domain.PEModeIdentity {
		return nil
	}
	var nic pe.NICIdentity
	if err := json.Unmarshal(result.Data, &nic); err != nil || nic.InstanceID == "" {
		return errs.Invalid("客户机未回报网卡设备实例，请重新取证")
	}
	t, err := s.Store.Terminals().Get(ctx, sess.TerminalID)
	if err != nil {
		return err
	}
	p, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	if err != nil {
		return err
	}
	want := "PCI\\VEN_" + p.NICVendor + "&DEV_" + p.NICDevice
	if !strings.HasPrefix(strings.ToUpper(nic.InstanceID), strings.ToUpper(want)) {
		return errs.Invalid("客户机回报的网卡与当前机型不一致，请重新开机采集机型")
	}
	v := domain.TerminalIdentity{ID: t.ID, TerminalID: t.ID, ProfileID: t.HardwareProfileID, InstanceID: nic.InstanceID, Source: "captured", UpdatedAt: time.Now().UTC()}
	return s.put(ctx, &v)
}
func (s *Service) put(ctx context.Context, v *domain.TerminalIdentity) error {
	return s.Store.Tx(ctx, func(tx store.Store) error {
		old, err := tx.TerminalIdentities().Get(ctx, v.ID)
		if errs.IsNotFound(err) {
			return tx.TerminalIdentities().Create(ctx, *v)
		}
		if err != nil {
			return err
		}
		if old.ProfileID != v.ProfileID || !strings.EqualFold(old.InstanceID, v.InstanceID) {
			if err := archiveCaptured(ctx, tx, old); err != nil {
				return err
			}
		}
		if v.LayerServerID == "" {
			v.LayerServerID = old.LayerServerID
		}
		// Failure state belongs to this profile and instance, never a different NIC in the same slot.
		if old.ProfileID == v.ProfileID && strings.EqualFold(old.InstanceID, v.InstanceID) && v.TemplateBootRef == "" && v.TemplateOKRef == "" && v.TemplateFailedRef == "" && v.TemplateStrikes == 0 && v.TemplateBootAt.IsZero() {
			v.TemplateBootRef, v.TemplateBootAt, v.TemplateOKRef, v.TemplateFailedRef, v.TemplateStrikes, v.TemplateGen = old.TemplateBootRef, old.TemplateBootAt, old.TemplateOKRef, old.TemplateFailedRef, old.TemplateStrikes, old.TemplateGen
		}
		return tx.TerminalIdentities().Update(ctx, *v)
	})
}

// Prepare is shared by synchronous boot and the bounded background sweep.
func (s *Service) Prepare(ctx context.Context, t domain.Terminal, configID, reductionID string, agent storage.ClientHostAgent, serverID string) (bool, error) {
	return s.prepare(ctx, t, configID, reductionID, agent, serverID, false)
}
func (s *Service) prepare(ctx context.Context, t domain.Terminal, configID, reductionID string, agent storage.ClientHostAgent, serverID string, background bool) (bool, error) {
	if t.IsSuper || t.HardwareProfileID == "" {
		return false, nil
	}
	a, err := s.Store.ModelAdaptations().ForProfileReduction(ctx, t.HardwareProfileID, reductionID)
	if errs.IsNotFound(err) {
		return s.prepareTemplate(ctx, t, configID, reductionID, agent, serverID, background)
	}
	if err != nil {
		return false, err
	}
	if !hasTemplate(a) {
		// 已有开机成功的记录（observed）：这个机型在该还原点上不需要模板，直接开机。
		return false, nil
	}
	v, err := s.Store.TerminalIdentities().Get(ctx, t.ID)
	missing := errs.IsNotFound(err)
	if err != nil && !missing {
		return false, err
	}
	var current *domain.TerminalIdentity
	if !missing {
		current = &v
	}
	decision, instance, err := s.planIdentity(ctx, t, a, current)
	if err != nil {
		return false, err
	}
	if decision == "no_template" {
		return false, nil
	}
	if decision == "capture" {
		if background {
			return false, nil
		}
		if _, err := s.StartCapture(ctx, t.ID); err != nil {
			return false, err
		}
		return false, ErrCaptureStarted
	}
	if decision == "compute" || decision == "reuse" || decision == "history" {
		source := "computed"
		if decision == "reuse" {
			source = "model"
		}
		if decision == "history" {
			source = "captured"
		}
		v = domain.TerminalIdentity{ID: t.ID, TerminalID: t.ID, ProfileID: t.HardwareProfileID, InstanceID: instance, Source: source, UpdatedAt: time.Now().UTC()}
		if err := s.put(ctx, &v); err != nil {
			return false, err
		}
	}
	red, err := s.Store.Reductions().Get(ctx, reductionID)
	if err != nil {
		return false, err
	}
	ready, err := s.buildLayer(ctx, t, &v, agent, serverID, background, reductionID, storage.IdentityLayerReq{MAC: t.MAC, Background: background, ConfigID: configID, SnapshotName: strings.TrimPrefix(red.Name, "@"), TemplateInstanceID: a.TemplateInstanceID, InstanceID: v.InstanceID, ClassKey: a.ClassKey})
	if err == nil && ready && !background && v.Source == "model" {
		v.TemplateBootRef, v.TemplateBootAt, v.TemplateGen = reductionID, s.now().UTC(), winreg.GeneratorVersion
		err = s.updateIdentity(ctx, &v)
	}
	return ready, err
}

// prepareTemplate 处理「这个机型在该还原点上没有适配记录」的开机。
// 没有单独的名单文件时，画像里每块 PCI 网卡都按镜像里已装的网卡和驱动文件现写模板。
func (s *Service) prepareTemplate(ctx context.Context, t domain.Terminal, configID, reductionID string, agent storage.ClientHostAgent, serverID string, background bool) (bool, error) {
	cur, err := s.Store.TerminalIdentities().Get(ctx, t.ID)
	missing := errs.IsNotFound(err)
	if err != nil && !missing {
		return false, err
	}
	var v *domain.TerminalIdentity
	if !missing {
		v = &cur
	}
	if v != nil && v.ProfileID == t.HardwareProfileID {
		state := s.bootState(t, *v, reductionID, !background)
		if state != *v {
			*v = state
			if err := s.updateIdentity(ctx, v); err != nil {
				return false, err
			}
		}
	}
	plan, err := s.planTemplate(ctx, t, reductionID, v)
	if err != nil {
		return false, err
	}
	switch plan.Kind {
	case "blocked":
		err := s.adaptationRequired(ctx, t, configID, reductionID)
		if plan.Cause != nil {
			err = fmt.Errorf("%w：%v", err, plan.Cause)
		}
		s.FailureAlarm(ctx, t, err)
		return false, err
	case "direct":
		return false, nil
	case "capture":
		if background {
			return false, nil
		}
		if _, err := s.StartCapture(ctx, t.ID); err != nil {
			return false, err
		}
		return false, ErrCaptureStarted
	}
	source := "derived"
	if _, ok := plan.Entry.instanceFor(t.MAC); !ok {
		// 不是这台自己取证得到的，就是借的同机型别的机器的。
		source = "model"
		if plan.Captured || v != nil && v.ProfileID == t.HardwareProfileID && v.Source == "captured" && strings.EqualFold(v.InstanceID, plan.Instance) {
			source = "captured"
		}
	}
	nv := domain.TerminalIdentity{ID: t.ID, TerminalID: t.ID, ProfileID: t.HardwareProfileID, InstanceID: plan.Instance, Source: source, UpdatedAt: time.Now().UTC()}
	if v != nil {
		nv.LayerReductionRef, nv.LayerServerID = v.LayerReductionRef, v.LayerServerID
		if v.ProfileID == nv.ProfileID && strings.EqualFold(v.InstanceID, nv.InstanceID) {
			nv.TemplateBootRef, nv.TemplateBootAt, nv.TemplateOKRef, nv.TemplateFailedRef, nv.TemplateStrikes, nv.TemplateGen = v.TemplateBootRef, v.TemplateBootAt, v.TemplateOKRef, v.TemplateFailedRef, v.TemplateStrikes, v.TemplateGen
		}
	}
	if v == nil || v.InstanceID != nv.InstanceID || v.ProfileID != nv.ProfileID || v.Source != nv.Source {
		if err := s.put(ctx, &nv); err != nil {
			return false, err
		}
	}
	red, err := s.Store.Reductions().Get(ctx, reductionID)
	if err != nil {
		return false, err
	}
	req := storage.IdentityLayerReq{MAC: t.MAC, Background: background, ConfigID: configID, SnapshotName: strings.TrimPrefix(red.Name, "@"), InstanceID: nv.InstanceID,
		Template: &storage.NICTemplate{Reg: plan.Entry.Reg, GUID: templateGUID(t.MAC, plan.Entry), Generate: plan.Entry.Generate && plan.Entry.Reg == "", Vendor: plan.Entry.Vendor, Device: plan.Entry.Device, DriverZip: plan.Entry.DriverZip, DriverStamp: plan.Entry.DriverStamp}}
	ready, err := s.buildLayer(ctx, t, &nv, agent, serverID, background, reductionID, req)
	if err == nil && ready && nv.LayerServerID == serverID && nv.LayerReductionRef == reductionID {
		s.layerStamps.Store(t.ID, templateStamp(t, serverID, reductionID, nv.InstanceID, plan.Entry))
	}
	if winreg.IsInstancePresent(err) {
		s.markSkipped(presentKey(reductionID, nv.InstanceID), nil)
		return false, nil
	}
	if winreg.IsSkipTemplate(err) {
		s.markDriverMiss(reductionID, plan.Entry)
		s.FailureAlarm(ctx, t, err)
		if s.Logger != nil {
			s.Logger.Warn("网卡模板没有写进身份层，这次从原还原点直接启动", "terminal", t.ID, "error", err)
		}
		return false, nil
	}
	if winreg.IsGenerationFailure(err) {
		s.markSkipped(generationFailureKey(t, reductionID, nv.InstanceID, plan.Entry), err)
	}
	// 远端暂态错误仍可由 BootService 回退一次；最终本机未准备好身份时不能导出裸原点。
	if err != nil && (winreg.IsGenerationFailure(err) || agent == storage.ClientHostAgent(s.Storage)) {
		err = fmt.Errorf("%w：%v", s.adaptationRequired(ctx, t, configID, reductionID), err)
		s.FailureAlarm(ctx, t, err)
	}
	if err != nil || !ready || background {
		return ready, err
	}
	// 层建好了才算「这次是套模板开机」，回调成功与否由此判断。
	latest, gerr := s.Store.TerminalIdentities().Get(ctx, t.ID)
	if gerr != nil {
		if !errs.IsNotFound(gerr) {
			return ready, gerr
		}
		latest = nv
	}
	latest.TemplateBootRef, latest.TemplateBootAt, latest.TemplateGen = reductionID, s.now().UTC(), winreg.GeneratorVersion
	if missing || errs.IsNotFound(gerr) {
		return ready, s.put(ctx, &latest)
	}
	return ready, s.updateIdentity(ctx, &latest)
}

// buildLayer 建（或复用）这台机器的身份层，并在旧节点清掉旧层后记下新层所在。
func (s *Service) buildLayer(ctx context.Context, t domain.Terminal, v *domain.TerminalIdentity, agent storage.ClientHostAgent, serverID string, background bool, reductionID string, req storage.IdentityLayerReq) (bool, error) {
	oldNode := v.LayerServerID
	host, ok := agent.(storage.IdentityHostAgent)
	if !ok {
		return false, storage.ErrNotImplemented
	}
	started := time.Now()
	err := host.CreateIdentityLayer(ctx, req)
	if s.Logger != nil {
		s.Logger.Info("identity layer rebuild", "terminal", t.ID, "node", serverID, "template", req.Template != nil, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	}
	if err != nil {
		return false, err
	}
	if oldNode != "" && oldNode != serverID && s.Place != nil {
		found := false
		for _, p := range s.Place.AllHosts(ctx) {
			if p.ServerID == oldNode {
				found = true
				if old, ok := p.Agent.(storage.IdentityHostAgent); ok {
					if err := old.DeleteIdentityLayer(ctx, t.MAC); err != nil {
						s.FailureAlarm(ctx, t, fmt.Errorf("旧节点身份层未清理：%w", err))
						return true, nil
					}
				}
			}
		}
		if !found {
			// 旧节点只是离线：先不记新层，等它回来清掉旧层；已摘出集群的没有可清的，照常记下。
			if _, err := s.Store.Servers().Get(ctx, oldNode); !errs.IsNotFound(err) {
				return true, nil
			}
		}
	}
	v.LayerReductionRef = reductionID
	v.LayerServerID = serverID
	v.UpdatedAt = time.Now().UTC()
	if err := s.put(ctx, v); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Service) RunRebuilder(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.rebuild(ctx)
		}
	}
}
func (s *Service) rebuild(ctx context.Context) {
	terminals, err := s.Store.Terminals().List(ctx)
	if err != nil {
		return
	}
	fresh := map[string]bool{}
	if s.Place != nil {
		for _, p := range s.Place.AllHosts(ctx) {
			fresh[p.ServerID] = true
		}
	}
	n := 0
	for _, t := range terminals {
		if n >= 10 {
			return
		}
		if t.IsSuper || t.State == domain.TerminalStateOnline {
			continue
		}
		v, err := s.Store.TerminalIdentities().Get(ctx, t.ID)
		if err != nil {
			continue
		}
		g, err := s.Store.Groups().Get(ctx, t.GroupID)
		if err != nil {
			continue
		}
		img, err := s.Store.Images().Get(ctx, g.SystemImageID)
		if err != nil || img.OSType != domain.OSTypeWindows {
			continue
		}
		cfg, err := s.Store.Configs().Get(ctx, g.SystemConfigID)
		if err != nil || cfg.DefaultReductionID == nil {
			continue
		}
		agent, node := s.Storage, s.NodeID
		if s.Place != nil {
			p := s.Place.ForTerminal(ctx, t)
			if !p.Healthy {
				continue
			}
			agent, node = p.Agent, p.ServerID
		}
		// 旧层所在节点离线时清不掉旧层、新层也记不下，重建了也白建；等它回来再做。
		if s.Place != nil && v.LayerServerID != "" && v.LayerServerID != node && !fresh[v.LayerServerID] {
			if _, err := s.Store.Servers().Get(ctx, v.LayerServerID); err == nil {
				continue
			}
		}
		computedStale, err := s.computedStale(ctx, t, v)
		if err != nil {
			continue
		}
		templateStale, err := s.templateLayerStale(ctx, t, v, *cfg.DefaultReductionID, node)
		if err != nil {
			continue
		}
		if !computedStale && !templateStale && v.LayerReductionRef == *cfg.DefaultReductionID && v.LayerServerID == node && v.ProfileID == t.HardwareProfileID {
			continue
		}
		n++
		_, _ = s.prepare(ctx, t, cfg.ID, *cfg.DefaultReductionID, agent, node, true)
	}
}
func (s *Service) FailureAlarm(ctx context.Context, t domain.Terminal, err error) {
	id := fmt.Sprintf("identity-%s", t.ID)
	now := time.Now().UTC()
	model := t.HardwareProfileID
	if p, e := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID); e == nil && p.Name != "" {
		model = p.Name
	}
	message := fmt.Sprintf("请先关闭客户机「%s」（%s），再到「机型」页处理机型「%s」；身份层未能完成，请检查驱动并按提示取证或适配：%s", t.Name, t.MAC, model, err.Error())
	a := domain.Alarm{ID: id, AlarmKey: id, Source: "image", Resource: t.ID, Status: "active", Type: "机型适配", Severity: "warn", Message: message, CreatedAt: now, UpdatedAt: now}
	if existing, e := s.Store.Alarms().Get(ctx, id); e == nil {
		a.CreatedAt = existing.CreatedAt
		_ = s.Store.Alarms().Update(ctx, a)
	} else {
		_ = s.Store.Alarms().Create(ctx, a)
	}
}

// adaptationRequired blocks ordinary boot after a known template failure; another identity capture cannot repair the image.
func (s *Service) adaptationRequired(ctx context.Context, t domain.Terminal, configID, reductionID string) error {
	model, point := t.HardwareProfileID, reductionID
	if p, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID); err == nil && p.Name != "" {
		model = p.Name
	}
	if r, err := s.Store.Reductions().Get(ctx, reductionID); err == nil {
		point = r.Name + "（" + r.ID + "）"
	}
	return fmt.Errorf("请先关闭客户机「%s」（%s），再到机型页为机型「%s」、配置「%s」、还原点「%s」执行完整适配；已停止普通启动：%w", t.Name, t.MAC, model, configID, point, ErrAdaptationRequired)
}
