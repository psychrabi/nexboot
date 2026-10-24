package pe

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
	"strings"
	"testing"
)

type finishFailureStore struct {
	store.Store
	fail, failResume *bool
}

func (s finishFailureStore) Tasks() store.TaskRepo {
	return finishFailureTasks{TaskRepo: s.Store.Tasks(), fail: s.fail, failResume: s.failResume}
}

func (s finishFailureStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.Tx(ctx, func(tx store.Store) error {
		return fn(finishFailureStore{Store: tx, fail: s.fail, failResume: s.failResume})
	})
}

type finishFailureTasks struct {
	store.TaskRepo
	fail, failResume *bool
}

func (r finishFailureTasks) Update(ctx context.Context, task domain.Task) error {
	if task.Status == domain.TaskStatusSuccess && *r.fail {
		*r.fail = false
		*r.failResume = true
		return fmt.Errorf("task completion write interrupted")
	}
	if task.Status == domain.TaskStatusRunning && *r.failResume {
		*r.failResume = false
		return fmt.Errorf("task resume write interrupted")
	}
	return r.TaskRepo.Update(ctx, task)
}

func TestModelAdaptCompletionWriteFailureRemainsResumable(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	hp := domain.HardwareProfile{ID: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100F", CreatedAt: svc.now(), UpdatedAt: svc.now()}
	if err := st.HardwareProfiles().Create(ctx, hp); err != nil {
		t.Fatal(err)
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	terminal.HardwareProfileID = hp.ID
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	fail, failResume := true, false
	wrapped := finishFailureStore{Store: st, fail: &fail, failResume: &failResume}
	svc.Store = wrapped
	model := ModelAdaptService{Store: wrapped, Storage: &modelDiskFake{}, PE: &svc}
	svc.OnDone = model.OnDone
	res, err := model.Start(ctx, terminal.ID, ModelAdaptRequest{ConfigID: "win11_default", Name: "e1000"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	if err := svc.Complete(ctx, sess.Token, PEResult{OK: true, Data: json.RawMessage(`{"instance_id":"PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&123&0&0088","class_key":"0012","netcfg_instance_id":"{11111111-2222-3333-4444-555555555555}","hwid":"PCI\\VEN_8086&DEV_100F"}`)}); err != nil {
		t.Fatal(err)
	}
	model.Wait()
	sess, _ = st.PESessions().Get(ctx, res.SessionID)
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if sess.State != domain.PEStateFinalizing || task.Status != domain.TaskStatusFailed || !strings.Contains(task.Error, "task completion write interrupted") {
		t.Fatalf("completion failure must retain resumable session and error: session=%s task=%s error=%q", sess.State, task.Status, task.Error)
	}
	model.ResumeFinalizing(ctx)
	if failResume {
		t.Fatal("resume must exercise the interrupted running-state write")
	}
	sess, _ = st.PESessions().Get(ctx, res.SessionID)
	task, _ = st.Tasks().Get(ctx, res.TaskID)
	if sess.State != domain.PEStateDone || task.Status != domain.TaskStatusSuccess || task.Error != "" {
		t.Fatalf("restart must finish both states without an old error: session=%s task=%s error=%q", sess.State, task.Status, task.Error)
	}
}

type modelDiskFake struct{ created, cleaned, saved bool }

func (f *modelDiskFake) ActiveClientMACs(context.Context) ([]string, error) { return nil, nil }
func (f *modelDiskFake) CreateModelAdaptation(context.Context, storage.ModelAdaptReq) (storage.LUNInfo, error) {
	f.created = true
	return storage.LUNInfo{Server: "192.168.10.251", Target: "iqn.test:adapt"}, nil
}
func (f *modelDiskFake) CleanupModelAdaptation(context.Context, string) error {
	f.cleaned = true
	return nil
}
func (f *modelDiskFake) SaveModelAdaptation(_ context.Context, req storage.SuperStopReq) (domain.Reduction, error) {
	// The native ZFS save removes one display prefix before creating a snapshot.
	if strings.Contains(strings.TrimPrefix(req.ReductionName, "@"), "@") {
		return domain.Reduction{}, fmt.Errorf("multiple '@' delimiters in snapshot name %q", req.ReductionName)
	}
	f.saved = true
	return domain.Reduction{ID: "win11_default_e1000", ConfigID: req.ConfigID, Name: "@" + strings.TrimPrefix(req.ReductionName, "@"), Status: domain.ReductionStatusReady}, nil
}

func TestModelAdaptationCreatesTemplateAndCurrentPointAfterPECompletion(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	hp := domain.HardwareProfile{ID: "e1000", Name: "测试e1000", Fingerprint: "pci-e1000", NICVendor: "8086", NICDevice: "100F", Platform: "pcbios", CreatedAt: svc.now(), UpdatedAt: svc.now()}
	if err := st.HardwareProfiles().Create(ctx, hp); err != nil {
		t.Fatal(err)
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	terminal.HardwareProfileID = hp.ID
	_ = st.Terminals().Update(ctx, terminal)
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: hp.ID, InstanceID: "old", LayerServerID: "old-node", LayerReductionRef: "old-point", UpdatedAt: svc.now()}); err != nil {
		t.Fatal(err)
	}
	disk := &modelDiskFake{}
	model := ModelAdaptService{Store: st, Storage: disk, PE: &svc, Now: svc.Now}
	svc.OnDone = model.OnDone
	svc.OnFailure = model.OnFailure
	res, err := model.Start(ctx, "t1", ModelAdaptRequest{ConfigID: "win11_default", Name: "e1000"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if !disk.created || sess.Mode != domain.PEModeAdapt || task.TargetRef != hp.ID {
		t.Fatalf("unexpected session/task %+v %+v", sess, task)
	}
	ini, _ := svc.File(ctx, sess.Token, "ndpe.ini", "")
	if !strings.Contains(string(ini.Content), "hwid=PCI\\VEN_8086&DEV_100F") || !strings.Contains(string(ini.Content), "portal=192.168.10.251") {
		t.Fatalf("missing target: %s", ini.Content)
	}
	err = svc.Complete(ctx, sess.Token, PEResult{OK: true, Data: json.RawMessage(`{"instance_id":"PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&123&0&0088","class_key":"0012","netcfg_instance_id":"{11111111-2222-3333-4444-555555555555}","hwid":"PCI\\VEN_8086&DEV_100F"}`)})
	if err != nil {
		t.Fatal(err)
	}
	model.Wait()
	cfg, _ := st.Configs().Get(ctx, "win11_default")
	if cfg.DefaultReductionID == nil || *cfg.DefaultReductionID != "win11_default_e1000" {
		t.Fatalf("current point %+v", cfg)
	}
	adapt, err := st.ModelAdaptations().ForProfileReduction(ctx, hp.ID, *cfg.DefaultReductionID)
	if err != nil || adapt.Source != domain.AdaptationAdapted || adapt.ClassKey != "0012" {
		t.Fatalf("template %+v %v", adapt, err)
	}
	task, _ = st.Tasks().Get(ctx, res.TaskID)
	if !disk.saved || task.Status != domain.TaskStatusSuccess {
		t.Fatalf("completion %+v", task)
	}
	if task.Message != "机型适配成功，已设为当前还原点" {
		t.Fatalf("completed task must show the final outcome, got %q", task.Message)
	}
	identity, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || identity.LayerServerID != "old-node" || identity.LayerReductionRef != "" {
		t.Fatalf("old identity layer must remain discoverable until rebuilt: %+v %v", identity, err)
	}
	t.Run("resume snapshot naming from previous build", func(t *testing.T) {
		// A previous build persisted the duplicate prefix before the native save
		// failed after promote/rename. Restart must finish that preserved disk.
		sess, _ := st.PESessions().Get(ctx, res.SessionID)
		sess.State = domain.PEStateFinalizing
		sess.ReductionName = "@@e1000"
		if err := st.PESessions().Update(ctx, sess); err != nil {
			t.Fatal(err)
		}
		task.Status = domain.TaskStatusFailed
		task.Error = "multiple '@' delimiters in snapshot name"
		if err := st.Tasks().Update(ctx, task); err != nil {
			t.Fatal(err)
		}
		disk.saved = false
		model.ResumeFinalizing(ctx)
		task, _ := st.Tasks().Get(ctx, res.TaskID)
		if !disk.saved || task.Status != domain.TaskStatusSuccess {
			t.Fatalf("recovery %+v", task)
		}
	})
}

func TestModelAdaptUsesUploadedADKWhenTargetImageHasNoWinRE(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	env, _ := st.WinPEEnvs().Get(ctx, "winpe-win11")
	_ = st.WinPEEnvs().Delete(ctx, env.ID)
	env.ID = "adk-current"
	env.Kind = domain.WinPEEnvADK
	env.ImageID = ""
	_ = st.WinPEEnvs().Create(ctx, env)
	svc.EnsureEnv = func(context.Context, string) (domain.WinPEEnv, error) {
		return domain.WinPEEnv{}, fmt.Errorf("WinRE is absent")
	}
	_ = st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100f", CreatedAt: svc.now(), UpdatedAt: svc.now()})
	t1, _ := st.Terminals().Get(ctx, "t1")
	t1.HardwareProfileID = "e1000"
	_ = st.Terminals().Update(ctx, t1)
	model := ModelAdaptService{Store: st, Storage: &modelDiskFake{}, PE: &svc}
	res, err := model.Start(ctx, t1.ID, ModelAdaptRequest{ConfigID: "win11_default", Name: "adapter"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	if sess.EnvID != "adk-current" {
		t.Fatalf("env=%s", sess.EnvID)
	}
}
