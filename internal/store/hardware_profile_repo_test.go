package store

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func TestHardwareProfileRepoFingerprintLookup(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	p := domain.HardwareProfile{
		ID: "hwp-1", Name: "VMware Virtual Platform · 网卡 15AD:07B0", Fingerprint: "15ad:07b0/1024/efi",
		NICVendor: "15ad", NICDevice: "07b0", BusLoc: "1024", Platform: "efi",
		Manufacturer: "VMware, Inc.", Product: "VMware Virtual Platform", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.HardwareProfiles().Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.HardwareProfiles().ByFingerprint(ctx, p.Fingerprint)
	if err != nil || got.ID != "hwp-1" || got.NICVendor != "15ad" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("byFingerprint = %+v err = %v", got, err)
	}
	if _, err := st.HardwareProfiles().ByFingerprint(ctx, "none"); err == nil {
		t.Fatal("未知指纹应返回未找到")
	}
	dup := p
	dup.ID = "hwp-2"
	if err := st.HardwareProfiles().Create(ctx, dup); err == nil {
		t.Fatal("同指纹不能有两个机型")
	}
	got.NICSubsys = "15ad:07b0"
	got.Name = "电竞区 A"
	if err := st.HardwareProfiles().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.HardwareProfiles().Get(ctx, "hwp-1")
	if again.Name != "电竞区 A" || again.NICSubsys != "15ad:07b0" {
		t.Fatalf("update lost: %+v", again)
	}
}

func TestModelAdaptationRepoLookups(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	mk := func(id, profile, reduction, config string, src domain.ModelAdaptationSource) domain.ModelAdaptation {
		return domain.ModelAdaptation{ID: id, ProfileID: profile, ReductionID: reduction, ConfigID: config, Source: src,
			TemplateInstanceID: "PCI\\VEN_15AD&DEV_07B0\\005056FFFF2EF64E00", ClassKey: "0010",
			NetCfgInstanceID: "{GUID}", TaskID: "task-1", TerminalID: "t1", DriverPackIDs: []string{"dp1", "dp2"}, CreatedAt: now}
	}
	for _, a := range []domain.ModelAdaptation{
		mk("a1", "hwp-1", "red-1", "cfg-1", domain.AdaptationAdapted),
		mk("a2", "hwp-1", "red-2", "cfg-1", domain.AdaptationInherited),
		mk("a3", "hwp-2", "red-1", "cfg-1", domain.AdaptationObserved),
		mk("a4", "hwp-2", "red-9", "cfg-2", domain.AdaptationObserved),
	} {
		if err := st.ModelAdaptations().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ModelAdaptations().Create(ctx, mk("a5", "hwp-1", "red-1", "cfg-1", domain.AdaptationObserved)); err == nil {
		t.Fatal("同一机型同一还原点只能有一条记录")
	}
	one, err := st.ModelAdaptations().ForProfileReduction(ctx, "hwp-1", "red-1")
	if err != nil || one.ID != "a1" || len(one.DriverPackIDs) != 2 || one.DriverPackIDs[1] != "dp2" {
		t.Fatalf("forProfileReduction = %+v err = %v", one, err)
	}
	if _, err := st.ModelAdaptations().ForProfileReduction(ctx, "hwp-1", "red-9"); err == nil {
		t.Fatal("没有记录应返回未找到")
	}
	byRed, err := st.ModelAdaptations().ByReduction(ctx, "red-1")
	if err != nil || len(byRed) != 2 {
		t.Fatalf("byReduction = %+v err = %v", byRed, err)
	}
	byProfile, err := st.ModelAdaptations().ByProfile(ctx, "hwp-2")
	if err != nil || len(byProfile) != 2 {
		t.Fatalf("byProfile = %+v err = %v", byProfile, err)
	}
	if err := st.ModelAdaptations().DeleteByReduction(ctx, "red-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ModelAdaptations().DeleteByConfig(ctx, "cfg-2"); err != nil {
		t.Fatal(err)
	}
	left, _ := st.ModelAdaptations().List(ctx)
	if len(left) != 1 || left[0].ID != "a2" {
		t.Fatalf("left = %+v", left)
	}
}

func TestTerminalRepoKeepsHardwareAndBootColumns(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	ok := now.Add(time.Minute)
	seedGroupChain(t, st, now)
	term := domain.Terminal{ID: "t1", MAC: "AABBCCDDEEFF", IP: "10.0.0.2", GroupID: "g1", State: domain.TerminalStateUnknown,
		HardwareProfileID: "hwp-1", LastBootAt: &now, LastBootReductionID: "red-1", LastBootOKAt: &ok}
	if err := st.Terminals().Create(ctx, term); err != nil {
		t.Fatal(err)
	}
	got, err := st.Terminals().GetByMAC(ctx, "AABBCCDDEEFF")
	if err != nil || got.HardwareProfileID != "hwp-1" || got.LastBootReductionID != "red-1" ||
		got.LastBootAt == nil || !got.LastBootAt.Equal(now) || got.LastBootOKAt == nil || !got.LastBootOKAt.Equal(ok) {
		t.Fatalf("terminal = %+v err = %v", got, err)
	}
	got.HardwareProfileID = ""
	got.LastBootOKAt = nil
	if err := st.Terminals().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.Terminals().Get(ctx, "t1")
	if again.HardwareProfileID != "" || again.LastBootOKAt != nil || again.LastBootAt == nil {
		t.Fatalf("update lost: %+v", again)
	}
	byProfile, err := st.Terminals().ByHardwareProfile(ctx, "hwp-1")
	if err != nil || len(byProfile) != 0 {
		t.Fatalf("byProfile = %+v err = %v", byProfile, err)
	}
	again.HardwareProfileID = "hwp-1"
	_ = st.Terminals().Update(ctx, again)
	byProfile, err = st.Terminals().ByHardwareProfile(ctx, "hwp-1")
	if err != nil || len(byProfile) != 1 || byProfile[0].ID != "t1" {
		t.Fatalf("byProfile = %+v err = %v", byProfile, err)
	}
}

// seedGroupChain 建终端外键所需的镜像 → 配置 → 还原点 → 分组 g1。
func seedGroupChain(t *testing.T, st *SQLStore, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, step := range []struct {
		name string
		err  error
	}{
		{"image", st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now})},
		{"config", st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: now})},
		{"reduction", st.Reductions().Create(ctx, domain.Reduction{ID: "red-1", ConfigID: "cfg-1", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady})},
		{"group", st.Groups().Create(ctx, domain.Group{ID: "g1", Name: "g", StartIP: "10.0.0.2", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: "red-1"})},
	} {
		if step.err != nil {
			t.Fatalf("%s: %v", step.name, step.err)
		}
	}
}
