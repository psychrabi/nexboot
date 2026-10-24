package hwprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

var vmxnet3 = Hints{Platform: "efi", BusID: "01:15:ad:07:b0", BusLoc: "1024", Manufacturer: "VMware, Inc.", Product: "VMware Virtual Platform"}
var e1000e = Hints{Platform: "efi", BusID: "01:80:86:10:d3", BusLoc: "1024", Manufacturer: "VMware, Inc.", Product: "VMware Virtual Platform"}

type fixture struct {
	st  *store.SQLStore
	svc Service
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "hw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, now: time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)}
	f.svc = Service{Store: st, Now: func() time.Time { return f.now }}
	red1, red2 := "red-1", "red-2"
	for _, step := range []struct {
		name string
		err  error
	}{
		{"image", st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: f.now})},
		{"linux", st.Images().Create(ctx, domain.Image{ID: "img-l", Name: "ubuntu", OSType: domain.OSTypeLinux, State: domain.ImageStateNormal, CreatedAt: f.now})},
		{"config", st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", DefaultReductionID: &red1, CreatedAt: f.now})},
		{"config2", st.Configs().Create(ctx, domain.Config{ID: "cfg-2", ImageID: "img-1", Name: "other", DefaultReductionID: &red2, CreatedAt: f.now})},
		{"configl", st.Configs().Create(ctx, domain.Config{ID: "cfg-l", ImageID: "img-l", Name: "linux", CreatedAt: f.now})},
		{"red1", st.Reductions().Create(ctx, domain.Reduction{ID: red1, ConfigID: "cfg-1", Name: "@0", DisplayName: "0", CreatedAt: f.now, Status: domain.ReductionStatusReady})},
		{"red2", st.Reductions().Create(ctx, domain.Reduction{ID: red2, ConfigID: "cfg-2", Name: "@0", DisplayName: "0", CreatedAt: f.now, Status: domain.ReductionStatusReady})},
		{"redl", st.Reductions().Create(ctx, domain.Reduction{ID: "red-l", ConfigID: "cfg-l", Name: "@0", CreatedAt: f.now, Status: domain.ReductionStatusReady})},
		{"group", st.Groups().Create(ctx, domain.Group{ID: "g1", Name: "电竞区", StartIP: "10.0.0.2", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: red1})},
		{"group2", st.Groups().Create(ctx, domain.Group{ID: "g2", Name: "普通区", StartIP: "10.0.1.2", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-2", SystemReductionID: red2})},
		{"groupl", st.Groups().Create(ctx, domain.Group{ID: "gl", Name: "Linux", StartIP: "10.0.2.2", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "img-l", SystemConfigID: "cfg-l", SystemReductionID: "red-l"})},
		{"t1", st.Terminals().Create(ctx, domain.Terminal{ID: "t1", Name: "A01", MAC: "AABBCCDDEE01", IP: "10.0.0.2", GroupID: "g1", State: domain.TerminalStateUnknown})},
		{"t2", st.Terminals().Create(ctx, domain.Terminal{ID: "t2", Name: "A02", MAC: "AABBCCDDEE02", IP: "10.0.0.3", GroupID: "g1", State: domain.TerminalStateUnknown})},
		{"t3", st.Terminals().Create(ctx, domain.Terminal{ID: "t3", Name: "B01", MAC: "AABBCCDDEE03", IP: "10.0.1.2", GroupID: "g2", State: domain.TerminalStateUnknown})},
		{"tl", st.Terminals().Create(ctx, domain.Terminal{ID: "tl", Name: "L01", MAC: "AABBCCDDEE04", IP: "10.0.2.2", GroupID: "gl", State: domain.TerminalStateUnknown})},
	} {
		if step.err != nil {
			t.Fatalf("%s: %v", step.name, step.err)
		}
	}
	return f
}

func (f *fixture) terminal(t *testing.T, id string) domain.Terminal {
	t.Helper()
	term, err := f.st.Terminals().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

func TestWindowsModelListHidesLinuxOnlyHardwareWithoutLosingTerminalProfile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "tl"), e1000e)
	if err != nil {
		t.Fatal(err)
	}
	views, err := f.svc.List(ctx)
	if err != nil || len(views) != 0 {
		t.Fatalf("Linux-only hardware must not appear in Windows adaptation: %+v err=%v", views, err)
	}
	view, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || view.Terminals != 0 || len(view.TerminalList) != 0 || view.Status != StatusIdle {
		t.Fatalf("no Windows clients or records cannot mean ok: %+v err=%v", view, err)
	}
	status, err := f.svc.ForTerminal(ctx, f.terminal(t, "tl"))
	if err != nil || status.ProfileID != term.HardwareProfileID || status.ProfileName == "" {
		t.Fatalf("Linux terminal keeps its hardware identity: %+v err=%v", status, err)
	}
}

func TestWindowsModelDetailCountsMixedHardwareWithoutAUsableCurrentPoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Observe(ctx, f.terminal(t, "tl"), vmxnet3); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.st.Configs().Get(ctx, "cfg-1")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := "not-ready-yet"
	cfg.DefaultReductionID = &unavailable
	if err := f.st.Configs().Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	views, err := f.svc.List(ctx)
	if err != nil || len(views) != 1 || views[0].ID != term.HardwareProfileID || views[0].Terminals != 1 || views[0].Status != StatusWaiting {
		t.Fatalf("Windows membership does not depend on a usable point: %+v err=%v", views, err)
	}
	view, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || view.Terminals != 1 || len(view.TerminalList) != 1 || view.TerminalList[0].ID != "t1" || view.Status != StatusWaiting {
		t.Fatalf("mixed hardware counts only its Windows client without claiming ok: %+v err=%v", view, err)
	}
}

func TestWindowsModelHistoryAndActiveTasksRemainVisibleAfterClientsMoveToLinux(t *testing.T) {
	for _, tc := range []struct {
		name        string
		adaptation  domain.ModelAdaptation
		taskStatus  domain.TaskStatus
		visible     bool
		record      bool
		status      Status
		unavailable string
	}{
		{name: "Windows observed history", adaptation: domain.ModelAdaptation{ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationObserved}, visible: true, record: true, status: StatusIdle},
		{name: "deleted Windows point", adaptation: domain.ModelAdaptation{ReductionID: "deleted-point", ConfigID: "cfg-1", Source: domain.AdaptationInherited}, visible: true, record: true, status: StatusIdle, unavailable: "还原点已不存在"},
		{name: "known Linux overrides adapted source", adaptation: domain.ModelAdaptation{ReductionID: "red-l", ConfigID: "cfg-1", Source: domain.AdaptationAdapted}, status: StatusIdle},
		{name: "unknown observed history", adaptation: domain.ModelAdaptation{ReductionID: "deleted-point", ConfigID: "deleted-config", Source: domain.AdaptationObserved}, status: StatusIdle},
		{name: "adapted history without directory", adaptation: domain.ModelAdaptation{ReductionID: "deleted-point", ConfigID: "deleted-config", Source: domain.AdaptationAdapted}, visible: true, record: true, status: StatusIdle, unavailable: "还原点已不存在"},
		{name: "inherited Windows template without directory", adaptation: domain.ModelAdaptation{ReductionID: "deleted-point", ConfigID: "deleted-config", Source: domain.AdaptationInherited, TemplateInstanceID: "PCI\\VEN_8086&DEV_10D3\\INSTANCE", ClassKey: "0001", NetCfgInstanceID: "interface-guid"}, visible: true, record: true, status: StatusIdle, unavailable: "还原点已不存在"},
		{name: "inherited completed Windows task", adaptation: domain.ModelAdaptation{ReductionID: "deleted-point", ConfigID: "deleted-config", Source: domain.AdaptationInherited, TaskID: "adapt-task"}, taskStatus: domain.TaskStatusSuccess, visible: true, record: true, status: StatusIdle, unavailable: "还原点已不存在"},
		{name: "active Windows task with only Linux history", adaptation: domain.ModelAdaptation{ReductionID: "red-l", ConfigID: "cfg-l", Source: domain.AdaptationObserved}, taskStatus: domain.TaskStatusPending, visible: true, status: StatusAdapting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
			if err != nil {
				t.Fatal(err)
			}
			term.GroupID = "gl"
			if err := f.st.Terminals().Update(ctx, term); err != nil {
				t.Fatal(err)
			}
			a := tc.adaptation
			a.ID, a.ProfileID, a.CreatedAt = "history", term.HardwareProfileID, f.now
			if err := f.st.ModelAdaptations().Create(ctx, a); err != nil {
				t.Fatal(err)
			}
			if tc.taskStatus != "" {
				if err := f.st.Tasks().Create(ctx, domain.Task{ID: "adapt-task", Type: domain.TaskTypeModelAdapt, TargetRef: term.HardwareProfileID, Status: tc.taskStatus, CreatedAt: f.now}); err != nil {
					t.Fatal(err)
				}
			}
			views, err := f.svc.List(ctx)
			if err != nil || (len(views) == 1) != tc.visible || len(views) > 1 {
				t.Fatalf("list visibility must use Windows history or active adaptation: %+v err=%v", views, err)
			}
			if tc.visible && (views[0].ID != term.HardwareProfileID || views[0].Terminals != 0 || views[0].Status != tc.status) {
				t.Fatalf("Linux clients must not count or prove Windows success: %+v", views[0])
			}
			view, err := f.svc.Get(ctx, term.HardwareProfileID)
			if err != nil || view.Terminals != 0 || len(view.TerminalList) != 0 || view.Status != tc.status {
				t.Fatalf("detail remains readable with Windows-only client count: %+v err=%v", view, err)
			}
			wantRecords := 0
			if tc.record {
				wantRecords = 1
			}
			if len(view.Adaptations) != wantRecords || len(view.ReductionList) != wantRecords {
				t.Fatalf("detail must exclude Linux and unconfirmed history: %+v", view)
			}
			if tc.record && (view.ReductionList[0].ID != a.ID || view.ReductionList[0].TaskID != a.TaskID || view.ReductionList[0].Unavailable != tc.unavailable) {
				t.Fatalf("confirmed history keeps its task and unavailable reason: %+v", view.ReductionList)
			}
		})
	}
}

func TestDetailResolvesAdaptationBusinessNamesAndUnavailablePoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	red, err := f.st.Reductions().Get(ctx, "red-1")
	if err != nil {
		t.Fatal(err)
	}
	red.DisplayName = "E1000 可开机"
	if err := f.st.Reductions().Update(ctx, red); err != nil {
		t.Fatal(err)
	}
	for _, a := range []domain.ModelAdaptation{
		{ID: "a-live", ProfileID: term.HardwareProfileID, ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationAdapted, TaskID: "adapt-task", CreatedAt: f.now},
		{ID: "a-missing", ProfileID: term.HardwareProfileID, ReductionID: "deleted-point", ConfigID: "cfg-1", Source: domain.AdaptationInherited, TaskID: "origin-task", CreatedAt: f.now.Add(time.Minute)},
	} {
		if err := f.st.ModelAdaptations().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	view, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Adaptations   []domain.ModelAdaptation `json:"adaptations"`
		ReductionList []map[string]any         `json:"reduction_list"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Adaptations) != 2 || len(response.ReductionList) != 2 {
		t.Fatalf("detail must preserve records and provide business names: %s", encoded)
	}
	byID := map[string]map[string]any{}
	for _, r := range response.ReductionList {
		byID[r["id"].(string)] = r
	}
	live := byID["a-live"]
	for key, want := range map[string]any{"image_id": "img-1", "image_name": "win", "config_id": "cfg-1", "config_name": "default", "reduction_id": "red-1", "reduction_name": "E1000 可开机", "source": "adapted", "task_id": "adapt-task", "current": true, "created_at": "2026-10-07T04:00:00Z"} {
		if live[key] != want {
			t.Fatalf("%s=%v, want %v", key, live[key], want)
		}
	}
	missing := byID["a-missing"]
	if missing["unavailable"] != "还原点已不存在" || missing["current"] != false || missing["reduction_name"] != "" {
		t.Fatalf("missing point must not invent a name or current marker: %+v", missing)
	}
	// A record follows the actual configuration point, rather than the group's cached point.
	cfg, err := f.st.Configs().Get(ctx, "cfg-1")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultReductionID = nil
	if err := f.st.Configs().Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	view, err = f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(view)
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	for _, r := range response.ReductionList {
		if r["id"] == "a-live" && r["current"] != false {
			t.Fatalf("no applied point should have no current marker: %+v", r)
		}
	}
}

func TestObserveMergesSameHardwareAndMovesTerminalsOnNICChange(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	t1, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil || t1.HardwareProfileID == "" {
		t.Fatalf("observe t1: %+v %v", t1, err)
	}
	t2, _ := f.svc.Observe(ctx, f.terminal(t, "t2"), vmxnet3)
	if t2.HardwareProfileID != t1.HardwareProfileID {
		t.Fatalf("同一硬件应归到同一机型：%q vs %q", t1.HardwareProfileID, t2.HardwareProfileID)
	}
	profiles, _ := f.st.HardwareProfiles().List(ctx)
	if len(profiles) != 1 || profiles[0].Name != "VMware 虚拟机 · VMware vmxnet3" {
		t.Fatalf("profiles = %+v", profiles)
	}
	// t1 换了网卡：归到新机型，旧机型只剩 t2。
	moved, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), e1000e)
	if moved.HardwareProfileID == t1.HardwareProfileID || moved.HardwareProfileID == "" {
		t.Fatalf("换网卡后应归到新机型：%+v", moved)
	}
	old, _ := f.st.Terminals().ByHardwareProfile(ctx, t1.HardwareProfileID)
	if len(old) != 1 || old[0].ID != "t2" {
		t.Fatalf("old profile terminals = %+v", old)
	}
	// 没有网卡信息的 /boot（老 iPXE）不改动终端。
	same, err := f.svc.Observe(ctx, f.terminal(t, "t1"), Hints{Platform: "efi"})
	if err != nil || same.HardwareProfileID != moved.HardwareProfileID {
		t.Fatalf("no hints should keep profile: %+v %v", same, err)
	}
}

func TestPreviousBootFailureDoesNotCondemnNewReductionOrNIC(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	img, _ := f.st.Images().Get(ctx, "img-1")
	term, _ = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	f.now = f.now.Add(time.Minute)
	term, _ = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	v, _ := f.svc.Get(ctx, term.HardwareProfileID)
	if v.Status != StatusWaiting || v.BootFailed != 0 {
		t.Fatalf("缺少回报只能等待确认: %+v", v)
	}
	newRed := domain.Reduction{ID: "new-red", ConfigID: "cfg-1", Name: "@new", Status: domain.ReductionStatusReady, CreatedAt: f.now}
	if err := f.st.Reductions().Create(ctx, newRed); err != nil {
		t.Fatal(err)
	}
	cfg, _ := f.st.Configs().Get(ctx, "cfg-1")
	cfg.DefaultReductionID = &newRed.ID
	if err := f.st.Configs().Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	v, _ = f.svc.Get(ctx, term.HardwareProfileID)
	if v.Status != StatusWaiting {
		t.Fatalf("尚未尝试新点，不能声称蓝屏: %+v", v)
	}
	term, _ = f.svc.Observe(ctx, term, e1000e)
	f.now = f.now.Add(time.Minute)
	term, err := f.svc.NoteBootStarted(ctx, term, img, newRed.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, _ = f.svc.Get(ctx, term.HardwareProfileID)
	if v.Status != StatusWaiting {
		t.Fatalf("旧网卡失败不能算新网卡失败: %+v", v)
	}
}

func TestBootTrackingRecordsObservedAdaptationAndFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	t1, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	img, _ := f.st.Images().Get(ctx, "img-1")

	// 第一次开机：只记时间与还原点。
	t1, err := f.svc.NoteBootStarted(ctx, t1, img, "red-1")
	if err != nil || t1.LastBootAt == nil || !t1.LastBootAt.Equal(f.now) || t1.LastBootReductionID != "red-1" {
		t.Fatalf("boot started: %+v %v", t1, err)
	}
	if failures, _ := f.st.BootFailures().List(ctx); len(failures) != 0 {
		t.Fatalf("首次开机不该有失败记录：%+v", failures)
	}
	// 系统起来了，开机脚本回调：记成功并写一条 observed。
	f.now = f.now.Add(90 * time.Second)
	if err := f.svc.NoteBooted(ctx, t1); err != nil {
		t.Fatal(err)
	}
	t1 = f.terminal(t, "t1")
	if t1.LastBootOKAt == nil || !t1.LastBootOKAt.Equal(f.now) {
		t.Fatalf("boot ok not recorded: %+v", t1)
	}
	ad, err := f.st.ModelAdaptations().ForProfileReduction(ctx, t1.HardwareProfileID, "red-1")
	if err != nil || ad.Source != domain.AdaptationObserved || ad.ConfigID != "cfg-1" || ad.TerminalID != "t1" {
		t.Fatalf("observed adaptation = %+v err = %v", ad, err)
	}
	// 再回调一次不重复、不报错。
	if err := f.svc.NoteBooted(ctx, t1); err != nil {
		t.Fatal(err)
	}

	// Another machine can reboot without reporting: this proves neither failure nor success.
	t2, _ := f.svc.Observe(ctx, f.terminal(t, "t2"), e1000e)
	f.now = f.now.Add(time.Minute)
	t2, _ = f.svc.NoteBootStarted(ctx, t2, img, "red-1")
	f.now = f.now.Add(3 * time.Minute)
	t2, _ = f.svc.NoteBootStarted(ctx, t2, img, "red-1")
	failures, _ := f.st.BootFailures().List(ctx)
	if len(failures) != 0 {
		t.Fatalf("missing callbacks are not failures: %+v", failures)
	}
	// 隔了很久再开机（中间正常关机）不算失败。
	f.now = f.now.Add(time.Hour)
	t2, _ = f.svc.NoteBootStarted(ctx, t2, img, "red-1")
	if failures, _ := f.st.BootFailures().List(ctx); len(failures) != 0 {
		t.Fatalf("过期的开机不该再记失败：%+v", failures)
	}
	// Linux 镜像没有开机脚本回调，不参与判定。
	tl := f.terminal(t, "tl")
	linux, _ := f.st.Images().Get(ctx, "img-l")
	tl, _ = f.svc.NoteBootStarted(ctx, tl, linux, "red-l")
	f.now = f.now.Add(time.Minute)
	_, _ = f.svc.NoteBootStarted(ctx, tl, linux, "red-l")
	if failures, _ := f.st.BootFailures().List(ctx); len(failures) != 0 {
		t.Fatalf("linux 不该记 windows 失败：%+v", failures)
	}
}

func TestStatusPerProfile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	img, _ := f.st.Images().Get(ctx, "img-1")
	t1, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3) // g1 → cfg-1 → red-1
	t3, _ := f.svc.Observe(ctx, f.terminal(t, "t3"), vmxnet3) // g2 → cfg-2 → red-2
	tl, _ := f.svc.Observe(ctx, f.terminal(t, "tl"), e1000e)  // Linux 分组
	if t1.HardwareProfileID != t3.HardwareProfileID {
		t.Fatal("fixture: t1/t3 should share a profile")
	}
	pid := t1.HardwareProfileID
	point := func(v View, red string) PointBrief {
		t.Helper()
		for _, p := range v.Points {
			if p.ReductionID == red {
				return p
			}
		}
		t.Fatalf("point %s missing from %+v", red, v.Points)
		return PointBrief{}
	}

	// 还没有任何开机结果：等待首次开机，两个当前点都未验证。
	views, err := f.svc.List(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("list = %+v err = %v", views, err)
	}
	v := views[0]
	if v.ID != pid || v.Status != StatusWaiting || v.Terminals != 2 || v.BootWaiting != 2 || len(v.Points) != 2 {
		t.Fatalf("vmxnet3 view = %+v", v)
	}
	if p := point(v, "red-1"); p.Verified || p.Terminals != 1 || p.Waiting != 1 || p.ConfigName != "default" || p.ReductionName != "0" {
		t.Fatalf("red-1 point = %+v", p)
	}
	for _, other := range views {
		if other.ID == tl.HardwareProfileID {
			t.Fatalf("linux-only profile must not appear: %+v", views)
		}
	}

	// t1 reported success; t3 has a separately recorded template failure, only g2 warns.
	t1, _ = f.svc.NoteBootStarted(ctx, t1, img, "red-1")
	_ = f.svc.NoteBooted(ctx, t1)
	t3, _ = f.svc.NoteBootStarted(ctx, t3, img, "red-2")
	f.now = f.now.Add(2 * time.Minute)
	t3, _ = f.svc.NoteBootStarted(ctx, t3, img, "red-2")
	// Only a separately recorded template failure can make the model failed.
	if err := f.st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: t3.ID, TerminalID: t3.ID, ProfileID: pid, TemplateFailedRef: "red-2"}); err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.Get(ctx, pid)
	if err != nil || v.Status != StatusFailed || v.BootOK != 1 || v.BootFailed != 1 {
		t.Fatalf("after failure view = %+v err = %v", v, err)
	}
	if p := point(v, "red-1"); !p.Verified || p.OK != 1 {
		t.Fatalf("red-1 point = %+v", p)
	}
	if p := point(v, "red-2"); p.Verified || p.Failed != 1 || p.ConfigName != "other" {
		t.Fatalf("red-2 point = %+v", p)
	}
	failing, err := f.svc.FailingByGroup(ctx)
	if err != nil || len(failing["g1"]) != 0 || len(failing["g2"]) != 1 || failing["g2"][0].ProfileID != pid || failing["g2"][0].Terminals != 1 || failing["g2"][0].ProfileName == "" {
		t.Fatalf("failing by group = %+v err = %v", failing, err)
	}

	// 有进行中的适配任务 → 适配中。
	if err := f.st.Tasks().Create(ctx, domain.Task{ID: "task-model_adapt-1", Type: domain.TaskTypeModelAdapt, TargetRef: pid, Status: domain.TaskStatusRunning, CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if v, _ = f.svc.Get(ctx, pid); v.Status != StatusAdapting {
		t.Fatalf("adapting view = %+v", v)
	}

	// 适配完成：新点设为 cfg-2 当前点并写了适配记录。旧点的失败不再算，但新点还没开过机。
	task, _ := f.st.Tasks().Get(ctx, "task-model_adapt-1")
	task.Status = domain.TaskStatusSuccess
	_ = f.st.Tasks().Update(ctx, task)
	red3 := "red-3"
	_ = f.st.Reductions().Create(ctx, domain.Reduction{ID: red3, ConfigID: "cfg-2", Name: "@3", DisplayName: "3", CreatedAt: f.now, Status: domain.ReductionStatusReady})
	_ = f.st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "ad-1", ProfileID: pid, ReductionID: red3, ConfigID: "cfg-2", Source: domain.AdaptationAdapted, CreatedAt: f.now})
	cfg, _ := f.st.Configs().Get(ctx, "cfg-2")
	cfg.DefaultReductionID = &red3
	_ = f.st.Configs().Update(ctx, cfg)
	if v, _ = f.svc.Get(ctx, pid); v.Status != StatusWaiting || v.BootFailed != 0 || point(v, red3).Verified {
		t.Fatalf("adapted but not yet booted = %+v", v)
	}
	if failing, _ = f.svc.FailingByGroup(ctx); len(failing["g2"]) != 0 {
		t.Fatalf("old point failure must not keep the group hint: %+v", failing)
	}

	// t3 在新点开机成功 → 正常。
	f.now = f.now.Add(time.Hour)
	t3, _ = f.svc.NoteBootStarted(ctx, f.terminal(t, "t3"), img, red3)
	f.now = f.now.Add(time.Minute)
	_ = f.svc.NoteBooted(ctx, t3)
	if v, _ = f.svc.Get(ctx, pid); v.Status != StatusOK || v.BootOK != 2 {
		t.Fatalf("both points verified = %+v", v)
	}
	ts, err := f.svc.ForTerminal(ctx, f.terminal(t, "t1"))
	if err != nil || ts.ProfileID != pid || ts.Status != StatusOK || ts.BootVerifiedAt == nil || ts.ProfileName == "" {
		t.Fatalf("terminal status = %+v err = %v", ts, err)
	}
}

func TestTerminalStatusTracksOnlyCurrentPoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Observe(ctx, f.terminal(t, "t3"), vmxnet3); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "current-adapt", ProfileID: term.HardwareProfileID, ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationAdapted, CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	status, err := f.svc.ForTerminal(ctx, term)
	if err != nil || status.Status != StatusWaiting || status.ReductionID != "red-1" {
		t.Fatalf("适配记录不等于本机开过机，另一配置缺记录也不能改变本机当前点状态: %+v err=%v", status, err)
	}
	other, err := f.svc.ForTerminal(ctx, f.terminal(t, "t3"))
	if err != nil || other.Status != StatusWaiting || other.ReductionID != "red-2" {
		t.Fatalf("当前点无记录也无失败，应等待首次开机: %+v err=%v", other, err)
	}
}

// 客户机详情只说本机开机结果：适配记录不出现在接口里，也不让本机算开过机。
func TestTerminalStatusReportsOnlyThisMachinesBoot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "saved-adapt", ProfileID: term.HardwareProfileID, ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationAdapted, TaskID: "save-task", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		status, err := f.svc.ForTerminal(ctx, f.terminal(t, "t1"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := read()
	if before["config_id"] != "cfg-1" || before["status"] != "waiting" || before["boot_verified_at"] != nil {
		t.Fatalf("适配保存不能声称本机已启动: %+v", before)
	}
	for _, key := range []string{"adapted", "source", "task_id"} {
		if _, ok := before[key]; ok {
			t.Fatalf("记录依据不再出现在客户机机型字段里: %+v", before)
		}
	}
	img, err := f.st.Images().Get(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	term, err = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after["status"] != "ok" || after["boot_verified_at"] != f.now.Format(time.RFC3339) {
		t.Fatalf("已有适配记录也必须能显示本机成功回报: %+v", after)
	}
	newRed := domain.Reduction{ID: "red-new", ConfigID: "cfg-1", Name: "@new", Status: domain.ReductionStatusReady, CreatedAt: f.now}
	if err := f.st.Reductions().Create(ctx, newRed); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.st.Configs().Get(ctx, "cfg-1")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultReductionID = &newRed.ID
	if err := f.st.Configs().Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	switched := read()
	if switched["reduction_id"] != "red-new" || switched["boot_verified_at"] != nil || switched["status"] != "waiting" {
		t.Fatalf("旧点成功不能套到新点: %+v", switched)
	}
}

func TestTerminalStatusFiltersFailureEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	img, err := f.st.Images().Get(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	term, err = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	if err != nil {
		t.Fatal(err)
	}
	failure := domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-1", ProfileID: term.HardwareProfileID, At: f.now}
	read := func() map[string]any {
		t.Helper()
		status, err := f.svc.ForTerminal(ctx, f.terminal(t, "t1"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, tt := range []struct {
		name   string
		f      domain.BootFailure
		want   bool
		status Status
	}{
		{"current point", failure, true, StatusWaiting},
		{"old point", domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-2", ProfileID: term.HardwareProfileID, At: f.now}, false, StatusWaiting},
		{"old profile", domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-1", ProfileID: "old-profile", At: f.now}, false, StatusWaiting},
		{"expired", domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-1", ProfileID: term.HardwareProfileID, At: f.now.Add(-8 * 24 * time.Hour)}, false, StatusWaiting},
		{"future", domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-1", ProfileID: term.HardwareProfileID, At: f.now.Add(time.Hour)}, false, StatusWaiting},
		{"iPXE stage", domain.BootFailure{MAC: term.MAC, Stage: domain.BootStageSanhook, Code: "0x1234", ReductionID: "red-1", ProfileID: term.HardwareProfileID, At: f.now}, true, StatusWaiting},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := f.st.BootFailures().Upsert(ctx, tt.f); err != nil {
				t.Fatal(err)
			}
			got := read()
			if (got["failure"] != nil) != tt.want || got["status"] != string(tt.status) {
				t.Fatalf("当前点失败依据=%+v want failure=%v status=%s", got, tt.want, tt.status)
			}
			if tt.want {
				proof := got["failure"].(map[string]any)
				if proof["stage"] != tt.f.Stage || proof["at"] != tt.f.At.Format(time.RFC3339) || (tt.f.Code != "" && proof["code"] != tt.f.Code) {
					t.Fatalf("失败阶段、时间与代码应来自当前记录: %+v", proof)
				}
			}
		})
	}
	// 同机型另一台客户机的失败不能冒充本机失败。
	other, err := f.svc.Observe(ctx, f.terminal(t, "t2"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	otherFailure := failure
	otherFailure.MAC = other.MAC
	if err := f.st.BootFailures().Upsert(ctx, otherFailure); err != nil {
		t.Fatal(err)
	}
	oldPoint := failure
	oldPoint.ReductionID = "red-2"
	if err := f.st.BootFailures().Upsert(ctx, oldPoint); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["failure"] != nil || got["status"] != "waiting" {
		t.Fatalf("另一台同机型客户机失败不能改变本机状态: %+v", got)
	}
	if err := f.st.BootFailures().Upsert(ctx, failure); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["failure"] != nil || got["boot_verified_at"] == nil {
		t.Fatalf("当前点成功回报应覆盖旧失败: %+v", got)
	}
	// 新网卡归并后，上次网卡的回报不能证明当前网卡成功启动。
	if _, err := f.svc.Observe(ctx, f.terminal(t, "t1"), e1000e); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["boot_verified_at"] != nil || got["failure"] != nil {
		t.Fatalf("换网卡后旧回报/失败不能沿用: %+v", got)
	}
}

func TestModelListAndDetailIgnoreFutureBootFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	if err != nil {
		t.Fatal(err)
	}
	// A failover or clock correction can make an otherwise matching failure lie in the future.
	if err := f.st.BootFailures().Upsert(ctx, domain.BootFailure{
		MAC: term.MAC, Stage: domain.BootStageWindows, ReductionID: "red-1",
		ProfileID: term.HardwareProfileID, At: f.now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	terminal, err := f.svc.ForTerminal(ctx, term)
	if err != nil || terminal.Status != StatusWaiting || terminal.Failure != nil {
		t.Fatalf("future failure cannot establish terminal incompatibility: %+v err=%v", terminal, err)
	}
	models, err := f.svc.List(ctx)
	if err != nil || len(models) != 1 || models[0].Status != StatusWaiting {
		t.Fatalf("model list must agree with terminal evidence: %+v err=%v", models, err)
	}
	detail, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || detail.Status != StatusWaiting {
		t.Fatalf("model detail must ignore future failure: %+v err=%v", detail, err)
	}
}

func TestInheritAndRename(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	t1, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	t2, _ := f.svc.Observe(ctx, f.terminal(t, "t2"), e1000e)
	for _, a := range []domain.ModelAdaptation{
		{ID: "ad-1", ProfileID: t1.HardwareProfileID, ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationAdapted, TemplateInstanceID: "PCI\\X\\1", ClassKey: "0010", CreatedAt: f.now},
		{ID: "ad-2", ProfileID: t2.HardwareProfileID, ReductionID: "red-1", ConfigID: "cfg-1", Source: domain.AdaptationObserved, CreatedAt: f.now},
	} {
		if err := f.st.ModelAdaptations().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	// 从 red-1 派生出 red-5（超管保存）：两条记录都继承，来源改为 inherited，模板位置保留。
	red5 := domain.Reduction{ID: "red-5", ConfigID: "cfg-1", Name: "@5", DisplayName: "5", CreatedAt: f.now.Add(time.Minute), Status: domain.ReductionStatusReady}
	if err := f.st.Reductions().Create(ctx, red5); err != nil {
		t.Fatal(err)
	}
	if err := InheritFrom(ctx, f.st, "red-1", red5, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.ModelAdaptations().ByReduction(ctx, "red-5")
	if len(got) != 2 {
		t.Fatalf("inherited = %+v", got)
	}
	for _, a := range got {
		if a.Source != domain.AdaptationInherited || a.ConfigID != "cfg-1" {
			t.Fatalf("inherited row = %+v", a)
		}
		if a.ProfileID == t1.HardwareProfileID && (a.TemplateInstanceID != "PCI\\X\\1" || a.ClassKey != "0010") {
			t.Fatalf("template location lost: %+v", a)
		}
	}
	// 再继承一次不重复。
	if err := InheritFrom(ctx, f.st, "red-1", red5, f.now); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.ModelAdaptations().ByReduction(ctx, "red-5"); len(got) != 2 {
		t.Fatalf("duplicate inherit: %+v", got)
	}
	// InheritLatest：red-7 从配置里（除自己外）最新的就绪点继承，即 red-5。
	red7 := domain.Reduction{ID: "red-7", ConfigID: "cfg-1", Name: "@7", DisplayName: "7", CreatedAt: f.now.Add(2 * time.Minute), Status: domain.ReductionStatusReady}
	_ = f.st.Reductions().Create(ctx, red7)
	if err := InheritLatest(ctx, f.st, red7, f.now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.ModelAdaptations().ByReduction(ctx, "red-7"); len(got) != 2 {
		t.Fatalf("inherit latest = %+v", got)
	}
	// 不从别的配置继承。
	if got, _ := f.st.ModelAdaptations().ByReduction(ctx, "red-2"); len(got) != 0 {
		t.Fatalf("cross-config leak: %+v", got)
	}

	if err := f.svc.Rename(ctx, t1.HardwareProfileID, "  电竞区机型 "); err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.HardwareProfiles().Get(ctx, t1.HardwareProfileID)
	if p.Name != "电竞区机型" {
		t.Fatalf("rename = %q", p.Name)
	}
	if err := f.svc.Rename(ctx, t1.HardwareProfileID, "   "); err == nil {
		t.Fatal("空名字应拒绝")
	}
}

func TestMissingWindowsReportIsNotFailureEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	tm, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	img, _ := f.st.Images().Get(ctx, "img-1")
	for i := 0; i < 3; i++ {
		var err error
		tm, err = f.svc.NoteBootStarted(ctx, tm, img, "red-1")
		if err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(2 * time.Minute)
	}
	failures, err := f.st.BootFailures().List(ctx)
	if err != nil || len(failures) != 0 {
		t.Fatalf("missing callback invented Windows failure: %+v %v", failures, err)
	}
	if tm.LastBootOKAt != nil {
		t.Fatal("missing callback invented success")
	}
}

func TestLegacyMissingCallbackStrikesAreNotFailureAndAcceptRealSuccess(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	id, _ := f.st.TerminalIdentities().Get(ctx, term.ID)
	id.TemplateFailedRef = "red-1"
	id.TemplateStrikes = 2
	if err := f.st.TerminalIdentities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.ForTerminal(ctx, term)
	if err != nil || view.Status == StatusFailed {
		t.Fatalf("legacy inferred failure remained: %+v %v", view, err)
	}
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	id, _ = f.st.TerminalIdentities().Get(ctx, term.ID)
	if id.TemplateOKRef != "red-1" || id.TemplateFailedRef != "" || id.TemplateStrikes != 0 {
		t.Fatalf("real success rejected by inferred state: %+v", id)
	}
}

func TestLegacyMissingReportIsWaitingForEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	tm, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), e1000e)
	if err := f.st.BootFailures().Upsert(ctx, domain.BootFailure{MAC: tm.MAC, Stage: domain.BootStageWindows, At: f.now, ReductionID: "red-1", ProfileID: tm.HardwareProfileID}); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.ForTerminal(ctx, tm)
	if err != nil || view.Status != StatusWaiting {
		t.Fatalf("missing callback still presented as proven Windows failure: %+v %v", view, err)
	}
}

func TestRuntimeHardwareAndBootWritersPreserveNewerSuccess(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	old := f.terminal(t, "t1")
	at := f.now
	fresh := old
	fresh.LastBootAt = &at
	fresh.LastBootOKAt = &at
	fresh.LastBootReductionID = "red-2"
	if err := f.st.Terminals().Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Observe(ctx, old, vmxnet3); err != nil {
		t.Fatal(err)
	}
	got := f.terminal(t, "t1")
	if got.LastBootOKAt == nil || got.LastBootReductionID != "red-2" {
		t.Fatalf("Observe erased ledger: %+v", got)
	}
	f.now = f.now.Add(time.Second) // This new boot occurs after the preserved successful callback.
	img, _ := f.st.Images().Get(ctx, "img-1")
	if _, err := f.svc.NoteBootStarted(ctx, old, img, "red-1"); err != nil {
		t.Fatal(err)
	}
	got = f.terminal(t, "t1")
	if got.LastBootOKAt == nil || got.LastBootReductionID != "red-1" {
		t.Fatalf("stale start erased ledger: %+v", got)
	}
}

func TestBootCallbackCannotCertifyANewerBootAfterItsRead(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	tm, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	img, _ := f.st.Images().Get(ctx, "img-1")
	tm, _ = f.svc.NoteBootStarted(ctx, tm, img, "red-1")
	f.now = f.now.Add(time.Minute)
	f.svc.Store = bootChangesAfterCallbackRead{Store: f.st, at: f.now}
	if err := f.svc.NoteBooted(ctx, tm); err != nil {
		t.Fatal(err)
	}
	got := f.terminal(t, "t1")
	if got.LastBootReductionID != "red-2" || got.LastBootOKAt != nil {
		t.Fatalf("old callback certified/rewrote new boot: %+v", got)
	}
	ads, _ := f.st.ModelAdaptations().List(ctx)
	if len(ads) != 0 {
		t.Fatalf("old callback invented observed adaptation: %+v", ads)
	}
}

type bootChangesAfterCallbackRead struct {
	store.Store
	at time.Time
}

func (s bootChangesAfterCallbackRead) Terminals() store.TerminalRepo {
	return bootChangesAfterCallbackRepo{TerminalRepo: s.Store.Terminals(), at: s.at}
}

type bootChangesAfterCallbackRepo struct {
	store.TerminalRepo
	at time.Time
}

func (r bootChangesAfterCallbackRepo) Get(ctx context.Context, id string) (domain.Terminal, error) {
	old, err := r.TerminalRepo.Get(ctx, id)
	if err != nil {
		return old, err
	}
	fresh := old
	fresh.LastBootAt = &r.at
	fresh.LastBootReductionID = "red-2"
	return old, r.TerminalRepo.Update(ctx, fresh)
}

func TestBootStartRetriesConcurrentSuccessButNeverOverwritesLaterStart(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprint(later), func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			old, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
			start := f.now
			old.LastBootAt = &start
			old.LastBootReductionID = "red-1"
			if err := f.st.Terminals().Update(ctx, old); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Minute)
			callback := f.now.Add(-time.Second)
			fresh := old
			fresh.LastBootOKAt = &callback
			if later {
				future := f.now.Add(time.Second)
				fresh.LastBootAt = &future
				fresh.LastBootReductionID = "later-point"
			}
			if err := f.st.Terminals().Update(ctx, fresh); err != nil {
				t.Fatal(err)
			}
			img, _ := f.st.Images().Get(ctx, "img-1")
			got, err := f.svc.NoteBootStarted(ctx, old, img, "red-2")
			stored := f.terminal(t, old.ID)
			if later {
				if stored.LastBootReductionID != "later-point" || !stored.LastBootAt.Equal(*fresh.LastBootAt) {
					t.Fatalf("newer start overwritten: %+v", stored)
				}
			} else if err != nil || got.LastBootReductionID != "red-2" || stored.LastBootReductionID != "red-2" || !stored.LastBootAt.Equal(f.now) || stored.LastBootOKAt == nil || !stored.LastBootOKAt.Equal(callback) {
				t.Fatalf("concurrent OK lost this boot: got=%+v stored=%+v err=%v", got, stored, err)
			}
		})
	}
}

func TestBootStartRetryDoesNotBorrowALaterCallbackFromThePreviousBoot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	tm, _ := f.svc.Observe(ctx, f.terminal(t, "t1"), vmxnet3)
	img, _ := f.st.Images().Get(ctx, "img-1")
	tm, _ = f.svc.NoteBootStarted(ctx, tm, img, "red-1")
	f.now = f.now.Add(time.Minute)
	swapped := false
	f.svc.Store = callbackDuringStartCAS{Store: f.st, afterRead: func(before domain.Terminal) {
		if swapped {
			return
		}
		swapped = true
		f.now = f.now.Add(time.Second)
		callback := f.now
		before.LastBootOKAt = &callback
		if err := f.st.Terminals().Update(ctx, before); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Second)
	}}
	if _, err := f.svc.NoteBootStarted(ctx, tm, img, "red-2"); err != nil {
		t.Fatal(err)
	}
	got := f.terminal(t, tm.ID)
	if got.LastBootReductionID != "red-2" || got.LastBootOKAt == nil || !got.LastBootAt.After(*got.LastBootOKAt) || bootedAfter(got) {
		t.Fatalf("new point borrowed previous callback: %+v", got)
	}
}

type callbackDuringStartCAS struct {
	store.Store
	afterRead func(domain.Terminal)
}

func (s callbackDuringStartCAS) Terminals() store.TerminalRepo {
	return callbackDuringStartRepo{TerminalRepo: s.Store.Terminals(), afterRead: s.afterRead}
}

type callbackDuringStartRepo struct {
	store.TerminalRepo
	afterRead func(domain.Terminal)
}

func (r callbackDuringStartRepo) UpdateBootIfUnchanged(ctx context.Context, before, after domain.Terminal) (bool, error) {
	r.afterRead(before)
	return r.TerminalRepo.UpdateBootIfUnchanged(ctx, before, after)
}
