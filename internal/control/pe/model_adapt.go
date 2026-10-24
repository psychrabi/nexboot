package pe

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tianwei/diskless/internal/control/assets"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/hwprofile"
	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// ModelAdaptStorage keeps adaptation lifecycle on the storage seam.
type ModelAdaptStorage interface {
	ActiveClientMACs(context.Context) ([]string, error)
	CreateModelAdaptation(context.Context, storage.ModelAdaptReq) (storage.LUNInfo, error)
	SaveModelAdaptation(context.Context, storage.SuperStopReq) (domain.Reduction, error)
	CleanupModelAdaptation(context.Context, string) error
}
type ModelAdaptRequest struct {
	ConfigID string `json:"config_id"`
	Name     string `json:"name"`
	EnvID    string `json:"env_id,omitempty"`
}
type ModelAdaptService struct {
	PortalAddr string
	Place      *place.Router
	Store      store.Store
	Storage    ModelAdaptStorage
	PE         *SessionService
	Now        func() time.Time
	wg         sync.WaitGroup
	mu         sync.Mutex
}

func (s *ModelAdaptService) Wait() { s.wg.Wait() }
func (s *ModelAdaptService) Start(ctx context.Context, terminalID string, req ModelAdaptRequest) (StartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Terminals().Get(ctx, terminalID)
	if err != nil {
		return StartResult{}, err
	}
	if t.State == domain.TerminalStateOnline || t.IsSuper {
		return StartResult{}, errs.Conflict("请先关闭客户机「" + terminalLabel(t) + "」并退出超管模式")
	}
	if t.HardwareProfileID == "" {
		return StartResult{}, errs.Invalid("请先让客户机「" + terminalLabel(t) + "」从网络启动一次，采集机型")
	}
	hosts := []interface {
		ActiveClientMACs(context.Context) ([]string, error)
	}{s.Storage}
	if s.Place != nil {
		hosts = nil
		for _, h := range s.Place.AllHosts(ctx) {
			hosts = append(hosts, h.Agent)
		}
	}
	for _, host := range hosts {
		macs, err := host.ActiveClientMACs(ctx)
		if err != nil {
			return StartResult{}, err
		}
		for _, mac := range macs {
			if storage.NormalizeMAC(mac) == storage.NormalizeMAC(t.MAC) {
				return StartResult{}, errs.Conflict("客户机「" + terminalLabel(t) + "」仍有磁盘连接，请先关机")
			}
		}
	}

	cfg, err := s.Store.Configs().Get(ctx, req.ConfigID)
	if err != nil {
		return StartResult{}, err
	}
	release, err := assets.BeginModelAdaptation(ctx, s.Store, cfg.ImageID, cfg.ID)
	if err != nil {
		return StartResult{}, err
	}
	defer release()
	img, err := s.Store.Images().Get(ctx, cfg.ImageID)
	if err != nil {
		return StartResult{}, err
	}
	if img.OSType != domain.OSTypeWindows {
		return StartResult{}, errs.Invalid("配置「" + cfg.Name + "」不是 Windows 配置")
	}
	if cfg.DefaultReductionID == nil {
		return StartResult{}, errs.Invalid("配置「" + cfg.Name + "」没有当前还原点")
	}
	if strings.TrimSpace(req.Name) == "" {
		return StartResult{}, errs.Invalid("请填写还原点名称")
	}
	active, err := s.Store.PESessions().ListActive(ctx)
	if err != nil {
		return StartResult{}, err
	}
	for _, session := range active {
		if session.Mode == domain.PEModeAdapt {
			return StartResult{}, errs.Conflict("已有机型适配任务在执行，请等客户机「" + session.MAC + "」完成")
		}
	}
	reds, err := s.Store.Reductions().ListByConfig(ctx, cfg.ID)
	if err != nil {
		return StartResult{}, err
	}
	display, name, err := assets.ValidateReductionName(req.Name, reds)
	if err != nil {
		return StartResult{}, err
	}
	red, err := s.Store.Reductions().Get(ctx, *cfg.DefaultReductionID)
	if err != nil {
		return StartResult{}, err
	}
	// Select the target configuration's image, independent of the terminal's current group.
	envID := req.EnvID
	if envID == "" {
		env, err := s.PE.chooseEnvForImage(ctx, img.ID)
		if err != nil {
			return StartResult{}, err
		}
		envID = env.ID
	}
	res, err := s.PE.start(ctx, t, domain.PEModeAdapt, domain.TaskTypeModelAdapt, envID, "", 0)
	if err != nil {
		return StartResult{}, err
	}
	sess, _ := s.Store.PESessions().Get(ctx, res.SessionID)
	sess.ConfigID = cfg.ID
	sess.ReductionID = red.ID
	sess.ReductionName = name
	sess.ReductionDisplayName = display
	filesDir, err := s.PE.FilesDir(ctx)
	if err != nil {
		_ = s.PE.fail(ctx, sess, "准备适配文件目录失败："+err.Error())
		return StartResult{}, err
	}
	profile, _ := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	drvdir := filepath.Join(filesDir, "pe-sessions", sess.ID, "drivers")
	lun, err := s.Storage.CreateModelAdaptation(ctx, storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: t.MAC, IP: t.IP, System: storage.ClientSource{ConfigID: cfg.ID, SnapshotName: strings.TrimPrefix(red.Name, "@")}}, HWID: "PCI\\VEN_" + profile.NICVendor + "&DEV_" + profile.NICDevice, DriverDir: drvdir})
	if err != nil {
		_ = s.PE.fail(ctx, sess, "请检查配置「"+cfg.Name+"」的适配盘："+err.Error())
		return StartResult{}, err
	}
	packs, err := s.PE.StageUploadedDriver(ctx, "PCI\\VEN_"+profile.NICVendor+"&DEV_"+profile.NICDevice, drvdir)
	if err != nil {
		_ = s.PE.fail(ctx, sess, err.Error())
		return StartResult{}, err
	}
	sess.DriverPackIDs = packs
	sess.TargetPortal = s.PortalAddr
	if sess.TargetPortal == "" {
		sess.TargetPortal = lun.Server
	}
	sess.TargetIQN = lun.Target
	sess.State = domain.PEStatePending
	if err := s.Store.PESessions().Update(ctx, sess); err != nil {
		_ = s.PE.fail(ctx, sess, "记录机型适配会话失败："+err.Error())
		return StartResult{}, err
	}
	task, _ := s.Store.Tasks().Get(ctx, res.TaskID)
	task.TargetRef = t.HardwareProfileID
	task.Message = "引导客户机：请开启「" + terminalLabel(t) + "」，自动生成机型配置"
	_ = s.Store.Tasks().Update(ctx, task)
	return res, nil
}

var validClassKey = regexp.MustCompile(`^[0-9]{4}$`)
var validInterfaceGUID = regexp.MustCompile(`(?i)^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$`)
var validDeviceInstance = regexp.MustCompile(`(?i)^PCI\\VEN_[0-9a-f]{4}&DEV_[0-9a-f]{4}[0-9a-z&_.-]*\\[0-9a-z&_.-]+$`)

type NICIdentity struct {
	InstanceID       string `json:"instance_id"`
	HWID             string `json:"hwid"`
	ClassKey         string `json:"class_key"`
	NetCfgInstanceID string `json:"netcfg_instance_id"`
	LUID             string `json:"luid"`
}

func (s *ModelAdaptService) OnDone(ctx context.Context, sess domain.PESession, result PEResult) error {
	if sess.Mode != domain.PEModeAdapt {
		return nil
	}
	var nic NICIdentity
	if err := json.Unmarshal(result.Data, &nic); err != nil {
		return errs.Invalid("客户机未回报有效的网卡身份，未保存还原点")
	}
	if !validDeviceInstance.MatchString(nic.InstanceID) || !validClassKey.MatchString(nic.ClassKey) || !validInterfaceGUID.MatchString(nic.NetCfgInstanceID) {
		return errs.Invalid("客户机的网卡自检结果不完整，未保存还原点")
	}
	task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
	if err != nil {
		return err
	}
	profile, err := s.Store.HardwareProfiles().Get(ctx, task.TargetRef)
	if err != nil {
		return err
	}
	terminal, err := s.Store.Terminals().Get(ctx, sess.TerminalID)
	if err != nil {
		return err
	}
	want := "PCI\\VEN_" + profile.NICVendor + "&DEV_" + profile.NICDevice
	if terminal.HardwareProfileID != profile.ID || !strings.EqualFold(nic.HWID, want) || !strings.HasPrefix(strings.ToUpper(nic.InstanceID), strings.ToUpper(want)+"&") && !strings.HasPrefix(strings.ToUpper(nic.InstanceID), strings.ToUpper(want)+"\\") {
		return errs.Invalid("客户机网卡已变更或回报身份与任务机型不一致，请重新开机采集机型后适配")
	}
	sess.State = domain.PEStateFinalizing
	sess.UpdatedAt = s.PE.now()
	if err := s.Store.PESessions().Update(ctx, sess); err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done() // HTTP must return before WinPE shuts down and releases its disk.

		cctx, cancel := context.WithTimeout(context.Background(), sess.ExpiresAt.Sub(s.PE.now()))
		defer cancel()
		err := s.finish(cctx, sess, nic)
		if err != nil {
			task, _ := s.Store.Tasks().Get(context.Background(), sess.TaskID)
			_ = s.PE.runner().Fail(context.Background(), task, fmt.Errorf("保存还原点未完成，请重启服务重试保存（适配盘已保留）：%w", err))
		}
	}()
	return ErrFinalizing
}
func (s *ModelAdaptService) finish(ctx context.Context, sess domain.PESession, nic NICIdentity) error {
	for {
		macs, err := s.Storage.ActiveClientMACs(ctx)
		if err != nil {
			return err
		}
		active := false
		for _, mac := range macs {
			if storage.NormalizeMAC(mac) == sess.MAC {
				active = true
			}
		}
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("请关闭客户机「%s」，磁盘连接一直未断开", sess.MAC)
		case <-time.After(time.Second):
		}
	}
	task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
	if err != nil {
		return err
	}
	task.Message = "保存还原点：客户机已关机，正在保存机型配置"
	task.Progress = 90
	_ = s.Store.Tasks().Update(ctx, task)
	defer storage.ChangeCatalogue()()
	// Earlier sessions persisted "@@name"; remove one prefix so their saved
	// adaptation disks can resume through the existing storage normalization.
	red, err := s.Storage.SaveModelAdaptation(ctx, storage.SuperStopReq{MAC: sess.MAC, ConfigID: sess.ConfigID, ReductionName: strings.TrimPrefix(sess.ReductionName, "@")})
	if err != nil {
		return err
	}
	red.CreatedAt = s.PE.now()
	red.DisplayName = sess.ReductionDisplayName
	terminal, err := s.Store.Terminals().Get(ctx, sess.TerminalID)
	if err != nil {
		return err
	}
	err = s.Store.Tx(ctx, func(tx store.Store) error {
		if _, err := tx.Reductions().Get(ctx, red.ID); errs.IsNotFound(err) {
			if err = tx.Reductions().Create(ctx, red); err != nil {
				return err
			}
		}
		if err := hwprofile.InheritFrom(ctx, tx, sess.ReductionID, red, s.PE.now()); err != nil {
			return err
		}
		old, _ := tx.ModelAdaptations().ForProfileReduction(ctx, task.TargetRef, red.ID)
		if old.ID != "" {
			if err := tx.ModelAdaptations().Delete(ctx, old.ID); err != nil {
				return err
			}
		}
		a := domain.ModelAdaptation{ID: fmt.Sprintf("adapt-%s-%s", red.ID, task.TargetRef), ProfileID: task.TargetRef, ReductionID: red.ID, ConfigID: sess.ConfigID, Source: domain.AdaptationAdapted, TemplateInstanceID: nic.InstanceID, ClassKey: nic.ClassKey, NetCfgInstanceID: nic.NetCfgInstanceID, TaskID: sess.TaskID, TerminalID: terminal.ID, DriverPackIDs: sess.DriverPackIDs, CreatedAt: s.PE.now()}
		if err := tx.ModelAdaptations().Create(ctx, a); err != nil {
			return err
		}
		identity := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: task.TargetRef, InstanceID: nic.InstanceID, Source: "captured", UpdatedAt: s.PE.now()}
		if old, err := tx.TerminalIdentities().Get(ctx, identity.ID); errs.IsNotFound(err) {
			if err := tx.TerminalIdentities().Create(ctx, identity); err != nil {
				return err
			}
		} else if err == nil {
			identity.LayerServerID = old.LayerServerID
			if err := tx.TerminalIdentities().Update(ctx, identity); err != nil {
				return err
			}
		} else {
			return err
		}
		cfg, err := tx.Configs().Get(ctx, sess.ConfigID)
		if err != nil {
			return err
		}
		cfg.DefaultReductionID = &red.ID
		if err := tx.Configs().Update(ctx, cfg); err != nil {
			return err
		}
		groups, err := tx.Groups().List(ctx)
		if err != nil {
			return err
		}
		for _, g := range groups {
			if g.SystemConfigID == cfg.ID {
				g.SystemReductionID = red.ID
				if err := tx.Groups().Update(ctx, g); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sess.State = domain.PEStateDone
	sess.UpdatedAt = s.PE.now()
	task.Message = "机型适配成功，已设为当前还原点"
	return s.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.PESessions().Update(ctx, sess); err != nil {
			return err
		}
		return s.PE.runner().Finish(ctx, tx, task, task.Message)
	})
}
func (s *ModelAdaptService) OnFailure(ctx context.Context, sess domain.PESession) error {
	if sess.Mode == domain.PEModeAdapt && sess.State != domain.PEStateFinalizing {
		return s.Storage.CleanupModelAdaptation(ctx, sess.MAC)
	}
	return nil
}

// ResumeFinalizing resumes acknowledged PE results after a service interruption.
// The storage save is idempotent even across promote/rename/snapshot interruption.
func (s *ModelAdaptService) ResumeFinalizing(ctx context.Context) {
	active, err := s.Store.PESessions().ListActive(ctx)
	if err != nil {
		return
	}
	for _, sess := range active {
		if sess.Mode != domain.PEModeAdapt || sess.State != domain.PEStateFinalizing {
			continue
		}
		var result PEResult
		var nic NICIdentity
		if json.Unmarshal([]byte(sess.Result), &result) != nil || json.Unmarshal(result.Data, &nic) != nil {
			continue
		}
		sess.ExpiresAt = s.PE.now().Add(DefaultTTL)
		task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
		if err != nil {
			continue
		}
		task.Status = domain.TaskStatusRunning
		task.Error = ""
		task.FinishedAt = nil
		_ = s.Store.Tasks().Update(ctx, task)
		_ = s.Store.PESessions().Update(ctx, sess)
		cctx, cancel := context.WithTimeout(ctx, DefaultTTL)
		err = s.finish(cctx, sess, nic)
		cancel()
		if err != nil {
			_ = s.PE.runner().Fail(ctx, task, fmt.Errorf("继续保存机型适配失败，盘已保留：%w", err))
		}
	}
}
