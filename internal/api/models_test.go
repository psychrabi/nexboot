package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tianwei/diskless/internal/control/hwprofile"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestReductionModelsUseFriendlyNamesAndPreserveCustomNames(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := domain.HardwareProfile{ID: "vmware", Fingerprint: "15ad:07b0/1024/efi", Manufacturer: "VMware, Inc.", Product: "VMware20,1", NICVendor: "15ad", NICDevice: "07b0", Name: "VMware20,1 · 网卡 15AD:07B0"}
	for _, err := range []error{
		st.Images().Create(ctx, domain.Image{ID: "win", Name: "Windows", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal}),
		st.Configs().Create(ctx, domain.Config{ID: "cfg", ImageID: "win", Name: "default"}),
		st.Reductions().Create(ctx, domain.Reduction{ID: "red", ConfigID: "cfg", Name: "@0", Status: domain.ReductionStatusReady}),
		st.HardwareProfiles().Create(ctx, p),
		st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "adapt", ProfileID: p.ID, ConfigID: "cfg", ReductionID: "red", Source: domain.AdaptationObserved}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := ModelAPI{Service: hwprofile.Service{Store: st}}
	for _, want := range []string{"VMware 虚拟机 · VMware vmxnet3", "教室专用机"} {
		if want == "教室专用机" {
			if err := svc.Rename(ctx, p.ID, RenameModelRequest{Name: want}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := svc.ForReduction(ctx, "red")
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		if rows[0].Name != want || rows[0].NICName != "VMware vmxnet3" || rows[0].NICLabel != "15AD:07B0" {
			t.Fatalf("model=%+v", rows[0])
		}
	}
}

// 还原点详情里的机型记录要说明来源：完整适配写过、沿用上个点都不等于开过机。
func TestReductionModelsCarryRecordSource(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, err := range []error{
		st.Images().Create(ctx, domain.Image{ID: "win", Name: "Windows", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal}),
		st.Configs().Create(ctx, domain.Config{ID: "cfg", ImageID: "win", Name: "default"}),
		st.Reductions().Create(ctx, domain.Reduction{ID: "red", ConfigID: "cfg", Name: "@0", Status: domain.ReductionStatusReady}),
		st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "a", Fingerprint: "15ad:07b0/1024/efi", NICVendor: "15ad", NICDevice: "07b0"}),
		st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "b", Fingerprint: "8086:100f/1024/efi", NICVendor: "8086", NICDevice: "100f"}),
		st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "ra", ProfileID: "a", ConfigID: "cfg", ReductionID: "red", Source: domain.AdaptationObserved}),
		st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "rb", ProfileID: "b", ConfigID: "cfg", ReductionID: "red", Source: domain.AdaptationAdapted}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ModelAPI{Service: hwprofile.Service{Store: st}}.ForReduction(ctx, "red")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	got := map[string]domain.ModelAdaptationSource{}
	for _, r := range rows {
		got[r.ID] = r.Source
		if r.Status != "" {
			t.Fatalf("a record alone has no boot status: %+v", r)
		}
	}
	if got["a"] != domain.AdaptationObserved || got["b"] != domain.AdaptationAdapted {
		t.Fatalf("sources=%+v", got)
	}
}
