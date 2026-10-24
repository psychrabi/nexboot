package identity

import (
	"context"
	"errors"
	"fmt"
	"github.com/tianwei/diskless/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestAdaptedBorrowedIdentityDoesNotCaptureForMissingCallback(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	e1000Model(t, st)
	a, _ := st.ModelAdaptations().Get(ctx, "model")
	a.TemplateInstanceID = slotInstance
	if err := st.ModelAdaptations().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
		t.Fatalf("first boot ready=%v err=%v", ready, err)
	}
	now = now.Add(2 * time.Minute)
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Kind != "system" {
		t.Errorf("unfinished borrowed identity must preview capture: %+v err=%v", view.NextBoot, err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("missing callback must not start capture: ready=%v err=%v builds=%d", ready, err, host.created)
	}
}

func TestPhysicalAdaptedModelDoesNotBorrowAnotherBoardsInstance(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			s, st, terminal, host, _ := newCaptureFixture(t)
			ctx := context.Background()
			a, _ := st.ModelAdaptations().Get(ctx, "model")
			a.TemplateInstanceID = slotInstance
			if err := st.ModelAdaptations().Update(ctx, a); err != nil {
				t.Fatal(err)
			}
			if cached {
				if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: slotInstance, Source: "model", LayerReductionRef: "red", LayerServerID: "a"}); err != nil {
					t.Fatal(err)
				}
			}
			view, err := s.Get(ctx, terminal.ID)
			if err != nil || view.NextBoot.Kind != "winpe" {
				t.Errorf("physical machine must preview own capture: %+v %v", view.NextBoot, err)
			}
			if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrCaptureStarted) || host.created != 0 {
				t.Fatalf("physical machine borrowed another board: ready=%v err=%v builds=%d", ready, err, host.created)
			}
		})
	}
}

func TestTemplateBorrowedInstanceDoesNotCaptureForMissingCallback(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: slotInstance, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	other := secondMachine(t, st, terminal)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	if ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("first borrowed boot: %v %v", ready, err)
	}
	now = now.Add(2 * time.Minute)
	before, _ := st.TerminalIdentities().Get(ctx, other.ID)
	view, err := s.Get(ctx, other.ID)
	if err != nil || view.NextBoot.Kind != "system" {
		t.Errorf("preview must capture after one unfinished boot: %+v %v", view.NextBoot, err)
	}
	after, _ := st.TerminalIdentities().Get(ctx, other.ID)
	if before != after {
		t.Fatal("preview changed persisted failure state")
	}
	if ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("borrowed template must capture after one unfinished boot: %v %v", ready, err)
	}
}

func TestPreviewIgnoresFailuresFromOlderGeneratorWithoutWriting(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	instance, _ := vmwareE1000E().instanceFor(terminal.MAC)
	old := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: instance, Source: "derived", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 2, TemplateGen: "older"}
	if err := st.TerminalIdentities().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Reason != "automatic_template" {
		t.Errorf("old generator failures must not block preview: %+v %v", view.NextBoot, err)
	}
	after, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if after != old {
		t.Fatal("read-only preview modified identity")
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("actual boot disagrees: %v %v", ready, err)
	}
}

func TestBackgroundRebuildRefreshesGeneratedRulesAndDriverStamp(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("initial build %v %v", ready, err)
	}
	current, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	current.TemplateGen = "older"
	if err := st.TerminalIdentities().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	first := host.created
	s.rebuild(ctx)
	if host.created != first+1 {
		t.Fatalf("new generator did not prebuild stale layer: builds=%d", host.created)
	}
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "new-driver", Name: "new E1000E", Category: domain.DriverCategoryNIC, OSType: domain.OSTypeWindows, Arch: "x64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_8086&DEV_10D3`}, StoragePath: "new-driver.zip"}); err != nil {
		t.Fatal(err)
	}
	s.rebuild(ctx)
	if host.created != first+2 || host.last.Template.DriverZip != "new-driver.zip" {
		t.Fatalf("changed driver did not prebuild: builds=%d request=%+v", host.created, host.last)
	}
	s.rebuild(ctx)
	if host.created != first+2 {
		t.Fatal("unchanged template rebuilt every sweep")
	}
	current, _ = st.TerminalIdentities().Get(ctx, terminal.ID)
	if current.TemplateStrikes != 0 || !current.TemplateBootAt.IsZero() {
		t.Fatalf("background counted a boot: %+v", current)
	}
}

func TestGeneratorFailureIsCachedButDriverChangesRetry(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	terminal.Name = "测试客户机22"
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	host.err = errors.New("网卡模板生成失败：注册表回读不一致")
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || err == nil {
		t.Fatalf("first generator error: %v %v", ready, err)
	}
	first := host.created
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrAdaptationRequired) || host.created != first {
		t.Fatalf("failed generation repeated: %v %v calls=%d", ready, err, host.created)
	}
	alarms, err := st.Alarms().List(ctx)
	if err != nil || len(alarms) != 1 || !strings.Contains(alarms[0].Message, terminal.Name) || !strings.Contains(alarms[0].Message, "测试e1000") {
		t.Fatalf("failure must name machine and profile: %+v %v", alarms, err)
	}
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "replacement", Category: domain.DriverCategoryNIC, OSType: domain.OSTypeWindows, Arch: "x64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_8086&DEV_10D3`}, StoragePath: "replacement.zip"}); err != nil {
		t.Fatal(err)
	}
	host.err = nil
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil || host.created != first+1 {
		t.Fatalf("new driver did not retry: %v %v calls=%d", ready, err, host.created)
	}
}

func TestBackgroundRebuildLeavesLinuxAndSuperClientsAlone(t *testing.T) {
	for _, mode := range []string{"linux", "super"} {
		t.Run(mode, func(t *testing.T) {
			s, st, terminal, host := newTemplateFixture(t)
			ctx := context.Background()
			if _, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil {
				t.Fatal(err)
			}
			id, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
			id.LayerReductionRef = "previous"
			if err := st.TerminalIdentities().Update(ctx, id); err != nil {
				t.Fatal(err)
			}
			if mode == "linux" {
				img, _ := st.Images().Get(ctx, "win")
				img.OSType = domain.OSTypeLinux
				if err := st.Images().Update(ctx, img); err != nil {
					t.Fatal(err)
				}
			} else {
				terminal.IsSuper = true
				if err := st.Terminals().Update(ctx, terminal); err != nil {
					t.Fatal(err)
				}
			}
			calls := host.created
			s.rebuild(ctx)
			if host.created != calls {
				t.Fatalf("background touched %s client", mode)
			}
		})
	}
}

func TestTransientLayerFailuresRemainRetryable(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("客户机仍有活跃会话"), errors.New("hivexregedit: temporary I/O failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, _, terminal, host := newTemplateFixture(t)
			s.Templates = nil
			host.err = failure
			if ready, err := s.Prepare(context.Background(), terminal, "cfg", "red", host, "a"); ready || err == nil {
				t.Fatalf("expected temporary failure: %v %v", ready, err)
			}
			host.err = nil
			if ready, err := s.Prepare(context.Background(), terminal, "cfg", "red", host, "a"); !ready || err != nil || host.created != 2 {
				t.Fatalf("temporary failure was cached: %v %v calls=%d", ready, err, host.created)
			}
		})
	}
}

func TestBackgroundRebuildTracksMACForCapturedSlotIdentity(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	p, _ := st.HardwareProfiles().Get(ctx, "e1000")
	p.Manufacturer = "Dell"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: slotInstance, Source: "captured"}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("first build %v %v", ready, err)
	}
	terminal.MAC = "0050562EF699"
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	s.rebuild(ctx)
	if host.created != 2 || host.last.MAC != terminal.MAC {
		t.Fatalf("new MAC did not prebuild captured identity: calls=%d request=%+v", host.created, host.last)
	}
}

func TestGeneratorFailureDoesNotRepeatForAnotherMACOfSameModel(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	host.err = errors.New("网卡模板生成失败：注册表回读不一致")
	if _, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err == nil {
		t.Fatal("expected initial generation error")
	}
	other := secondMachine(t, st, terminal)
	if ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrAdaptationRequired) || host.created != 1 {
		t.Fatalf("same model repeated failed generation: %v %v calls=%d", ready, err, host.created)
	}
}

func TestISCSIDriverSelectionRebuildsOlderGeneratedLayers(t *testing.T) {
	for _, oldGen := range []string{"5", "6"} {
		for _, background := range []bool{false, true} {
			t.Run(fmt.Sprint("gen_", oldGen, "_background_", background), func(t *testing.T) {
				s, st, terminal, host := newTemplateFixture(t)
				s.Templates = nil
				ctx := context.Background()
				if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
					t.Fatalf("initial prepare: %v %v", ready, err)
				}
				old, err := st.TerminalIdentities().Get(ctx, terminal.ID)
				if err != nil {
					t.Fatal(err)
				}
				old.TemplateGen = oldGen // 旧层可能缺安装项，或使用了不支持 iSCSI 的 NetAdapterCx。
				if err := st.TerminalIdentities().Update(ctx, old); err != nil {
					t.Fatal(err)
				}
				first := host.created
				view, err := s.Get(ctx, terminal.ID)
				if err != nil || !view.Stale || view.NextBoot.Kind != "system" {
					t.Errorf("preview must mark Gen5 stale without capture: %+v %v", view, err)
				}
				unchanged, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
				if unchanged != old || host.created != first {
					t.Fatal("preview wrote identity/storage")
				}
				if background {
					s.rebuild(ctx)
				} else if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
					t.Fatalf("rebuild: %v %v", ready, err)
				}
				current, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
				if host.created != first+1 || current.TemplateGen != "7" {
					t.Fatalf("Gen5 reused instead of rebuilt: builds=%d before=%d identity=%+v", host.created, first, current)
				}
			})
		}
	}
}

func TestNetAdapterCxGenerationFailureKeepsActionableReasonWhenCached(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	host.err = errors.New("网卡模板生成失败：网卡驱动 sample.sys 使用 NetAdapterCx，不支持 iSCSI 无盘启动；请安装传统 NDIS 驱动")
	for attempt := 0; attempt < 2; attempt++ {
		ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
		if ready || !errors.Is(err, ErrAdaptationRequired) || !strings.Contains(err.Error(), "NetAdapterCx") || !strings.Contains(err.Error(), "NDIS") {
			t.Errorf("attempt %d lost actionable cause or allowed raw boot: %v %v", attempt, ready, err)
		}
		if host.created != 1 {
			t.Fatalf("cached rejection repeated generator: calls=%d", host.created)
		}
	}
	alarms, err := st.Alarms().List(ctx)
	if err != nil || len(alarms) != 1 || !strings.Contains(alarms[0].Message, "NDIS") {
		t.Fatalf("cached alarm lost driver cause: %+v %v", alarms, err)
	}
}
