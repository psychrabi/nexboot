package assets

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// identityRecorder 给各删除流程的假存储补上身份层接口，按先后记下调用。
type identityRecorder struct {
	events []string
}

func (r *identityRecorder) CreateIdentityLayer(context.Context, storage.IdentityLayerReq) error {
	return nil
}

func (r *identityRecorder) DeleteIdentityLayer(_ context.Context, mac string) error {
	r.events = append(r.events, "清身份层 "+mac)
	return nil
}

func (r *identityRecorder) ReleaseIdentityLayers(_ context.Context, sources []string) ([]string, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("expected source required")
	}
	r.events = append(r.events, "清身份层 "+identityMAC)
	return []string{identityMAC}, nil
}

type imageStorageWithIdentity struct {
	*fakeImageStorage
	*identityRecorder
}

func (s imageStorageWithIdentity) DeleteImage(ctx context.Context, id string, configIDs []string) error {
	s.events = append(s.events, "删镜像")
	return s.fakeImageStorage.DeleteImage(ctx, id, configIDs)
}

type configStorageWithIdentity struct {
	*fakeConfigStorage
	*identityRecorder
}

func (s configStorageWithIdentity) DeleteConfig(ctx context.Context, id string) error {
	s.events = append(s.events, "删配置")
	return s.fakeConfigStorage.DeleteConfig(ctx, id)
}

func (s configStorageWithIdentity) MergeConfig(ctx context.Context, req storage.MergeConfigReq) (storage.MergeConfigResult, error) {
	s.events = append(s.events, "合并配置")
	return s.fakeConfigStorage.MergeConfig(ctx, req)
}

type reductionStorageWithIdentity struct {
	*fakeReductionStorage
	*identityRecorder
}

func (s reductionStorageWithIdentity) DeleteReduction(ctx context.Context, configID, name string) error {
	s.events = append(s.events, "删还原点")
	return s.fakeReductionStorage.DeleteReduction(ctx, configID, name)
}

func (s reductionStorageWithIdentity) MergeReduction(ctx context.Context, configID string, names []string) ([]string, error) {
	s.events = append(s.events, "合并还原点")
	return s.fakeReductionStorage.MergeReduction(ctx, configID, names)
}

const identityMAC = "0050562EF64E"

// seedSwitchedGroup 复现现场：分组已改用新镜像 img-new，客户机还留着旧镜像 img-old 上的身份层。
// 客户机一小时前关机、之后没再开过。
func seedSwitchedGroup(t *testing.T, st store.Store, state domain.TerminalState) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"img-old", "img-new"} {
		red := "red-" + id
		for _, create := range []func() error{
			func() error {
				return st.Images().Create(ctx, domain.Image{ID: id, Name: id, OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now})
			},
			func() error {
				return st.Configs().Create(ctx, domain.Config{ID: "cfg-" + id, ImageID: id, Name: "default", DefaultReductionID: &red, CreatedAt: now})
			},
			func() error {
				return st.Reductions().Create(ctx, domain.Reduction{ID: red, ConfigID: "cfg-" + id, Name: "@1", CreatedAt: now, Status: domain.ReductionStatusReady})
			},
		} {
			if err := create(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "windows", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-new", SystemConfigID: "cfg-img-new", SystemReductionID: "red-img-new"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: identityMAC, IP: "10.0.0.5", GroupID: "g-1", State: state, OfflineAt: ago(time.Hour), LastBootAt: ago(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func ago(d time.Duration) *time.Time {
	at := time.Now().UTC().Add(-d)
	return &at
}

// 身份层是开机时按还原点生成的缓存：客户机关着时，删除镜像顺手清掉它，而不是让操作者去找一个界面上没有的东西。
func TestDeleteImageClearsIdentityLayersOfPoweredOffClients(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	agent := imageStorageWithIdentity{
		&fakeImageStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
		&identityRecorder{},
	}

	if err := (ImageService{Store: st, Storage: agent}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "删镜像"}; !slices.Equal(agent.events, want) {
		t.Fatalf("events = %v, want %v", agent.events, want)
	}
}

// 清身份层会拆掉这台机器的克隆，守与离线回收相同的规则：开着、跑着 WinPE、刚关机或刚开机的都照常拦下并点名。
// 会话断开不等于关机，刚开机的机器还没登上 iSCSI，见 cloneGracePeriod。
func TestDeleteImageStillRefusesWhileTheClientIsInUse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     domain.TerminalState
		pe        bool
		offlineAt *time.Time
		bootAt    *time.Time
	}{
		{name: "online", state: domain.TerminalStateOnline},
		{name: "winpe", state: domain.TerminalStateOffline, pe: true},
		{name: "just shut down", state: domain.TerminalStateOffline, offlineAt: ago(time.Minute)},
		{name: "just booting", state: domain.TerminalStateOffline, bootAt: ago(10 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newImageTestStore(t)
			seedSwitchedGroup(t, st, tc.state)
			if tc.offlineAt != nil || tc.bootAt != nil {
				term, err := st.Terminals().Get(ctx, "term-1")
				if err != nil {
					t.Fatal(err)
				}
				if tc.offlineAt != nil {
					term.OfflineAt = tc.offlineAt
				}
				if tc.bootAt != nil {
					term.LastBootAt = tc.bootAt
				}
				if err := st.Terminals().Update(ctx, term); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pe {
				now := time.Now().UTC()
				if err := st.PESessions().Create(ctx, domain.PESession{ID: "pe-1", TerminalID: "term-1", TaskID: "task-pe", Token: "token", Mode: domain.PEModeAdapt, State: domain.PEStatePending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			agent := imageStorageWithIdentity{
				&fakeImageStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
				&identityRecorder{},
			}

			err := (ImageService{Store: st, Storage: agent}).DeleteImage(ctx, "img-old")
			if err == nil || !strings.Contains(err.Error(), "请先关闭这些客户机，关机满 5 分钟后重试：00:50:56:2E:F6:4E") {
				t.Fatalf("err = %v", err)
			}
			if len(agent.events) != 0 {
				t.Fatalf("events = %v, want nothing touched", agent.events)
			}
		})
	}
}

func TestDeleteConfigClearsIdentityLayersOfPoweredOffClients(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	// 同一镜像下另有配置，删的不是最后一个。
	now := time.Now().UTC()
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-img-old-2", ImageID: "img-old", Name: "其它", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := configStorageWithIdentity{
		&fakeConfigStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
		&identityRecorder{},
	}

	if _, err := (ConfigService{Store: st, Storage: agent}).Delete(ctx, "cfg-img-old"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "删配置"}; !slices.Equal(agent.events, want) {
		t.Fatalf("events = %v, want %v", agent.events, want)
	}
}

func TestMergeConfigClearsIdentityLayersOfPoweredOffClients(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	agent := configStorageWithIdentity{
		&fakeConfigStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
		&identityRecorder{},
	}

	if _, err := (ConfigService{Store: st, Storage: agent}).Merge(ctx, "cfg-img-old"); err != nil {
		t.Fatalf("merge refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "合并配置"}; !slices.Equal(agent.events, want) {
		t.Fatalf("events = %v, want %v", agent.events, want)
	}
}

// 删还原点：其它检查拦下时不能先把身份层清了，否则白白让这些机器下次开机重建。
func TestDeleteReductionClearsIdentityLayersOnlyAfterEveryCheckPasses(t *testing.T) {
	ctx := context.Background()
	st := seedReductionStore(t)
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "windows", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: "red-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: identityMAC, IP: "10.0.0.5", GroupID: "g-1", State: domain.TerminalStateOffline, OfflineAt: ago(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ident := "tank/run/IDENT-" + identityMAC

	applied := reductionStorageWithIdentity{&fakeReductionStorage{dependents: map[string][]string{"@0": {ident}}}, &identityRecorder{}}
	if _, err := (ReductionService{Store: st, Storage: applied}).Delete(ctx, "red-1"); err == nil {
		t.Fatal("deleting the applied reduction must be refused")
	}
	if len(applied.events) != 0 {
		t.Fatalf("events = %v, want nothing touched", applied.events)
	}

	old := reductionStorageWithIdentity{&fakeReductionStorage{dependents: map[string][]string{"@1": {ident}}}, &identityRecorder{}}
	if _, err := (ReductionService{Store: st, Storage: old}).Delete(ctx, "red-2"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "删还原点"}; !slices.Equal(old.events, want) {
		t.Fatalf("events = %v, want %v", old.events, want)
	}
}

func TestMergeReductionsClearsIdentityLayersOfPoweredOffClients(t *testing.T) {
	ctx := context.Background()
	st := seedReductionStore(t)
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "windows", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: "red-2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: identityMAC, IP: "10.0.0.5", GroupID: "g-1", State: domain.TerminalStateOffline, OfflineAt: ago(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	agent := reductionStorageWithIdentity{&fakeReductionStorage{dependents: map[string][]string{"@0": {"tank/run/IDENT-" + identityMAC}}}, &identityRecorder{}}

	if _, err := (ReductionService{Store: st, Storage: agent}).Merge(ctx, "cfg-1", MergeReductionsRequest{KeepReductionID: "red-2"}); err != nil {
		t.Fatalf("merge refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "合并还原点"}; !slices.Equal(agent.events, want) {
		t.Fatalf("events = %v, want %v", agent.events, want)
	}
}

// 清身份层会连带拆掉这台机器的超管盘：超管机关着也不能顺手清，要先结束超管模式。
func TestDeleteImageLeavesASuperClientsIdentityLayerAlone(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	term, err := st.Terminals().Get(ctx, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	term.IsSuper = true
	if err := st.Terminals().Update(ctx, term); err != nil {
		t.Fatal(err)
	}
	agent := imageStorageWithIdentity{
		&fakeImageStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
		&identityRecorder{},
	}

	err = (ImageService{Store: st, Storage: agent}).DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "请先结束超管机的超管模式（保存或放弃）：00:50:56:2E:F6:4E") {
		t.Fatalf("err = %v", err)
	}
	if len(agent.events) != 0 {
		t.Fatalf("events = %v, want nothing touched", agent.events)
	}
}

// 从没连上过的客户机没有关机时间（见迁移 0046），只看最近一次开机；否则这层永远清不掉。
func TestDeleteImageClearsIdentityLayerOfAClientThatNeverConnected(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	term, err := st.Terminals().Get(ctx, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	term.OfflineAt = nil
	if err := st.Terminals().Update(ctx, term); err != nil {
		t.Fatal(err)
	}
	agent := imageStorageWithIdentity{
		&fakeImageStorage{dependents: map[string][]string{"cfg-img-old": {"tank/run/IDENT-" + identityMAC}}},
		&identityRecorder{},
	}

	if err := (ImageService{Store: st, Storage: agent}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if want := []string{"清身份层 " + identityMAC, "删镜像"}; !slices.Equal(agent.events, want) {
		t.Fatalf("events = %v, want %v", agent.events, want)
	}
}
