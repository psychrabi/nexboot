package local

import (
	"context"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/storage"
)

func TestIdentityLayerIsReusedOnlyForSameRestorePoint(t *testing.T) {
	z := seedSuperPool()
	a := New("node-a", z)
	calls := 0
	a.identityWriteFn = func(context.Context, string, storage.IdentityLayerReq) error { calls++; return nil }
	req := storage.IdentityLayerReq{MAC: "001122334455", ConfigID: "cfg", SnapshotName: "0", TemplateInstanceID: `PCI\VEN_15AD&DEV_07B0\001122FFFF33445400`, InstanceID: `PCI\VEN_15AD&DEV_07B0\001122FFFF33445500`, ClassKey: "0010"}
	if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !z.snaps["tank/run/IDENT-001122334455@ready"] {
		t.Fatalf("calls=%d ready=%v", calls, z.snaps)
	}
	z.snaps["tank/nd/cfg@r1"] = true
	req.SnapshotName = "r1"
	if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("stale layer not rebuilt: %d", calls)
	}
}

func TestIdentityInstanceChangeRebuildsEvenAtSamePoint(t *testing.T) {
	z := seedSuperPool()
	a := New("node-a", z)
	writes := 0
	a.identityWriteFn = func(context.Context, string, storage.IdentityLayerReq) error { writes++; return nil }
	req := storage.IdentityLayerReq{MAC: "001122334455", ConfigID: "cfg", SnapshotName: "0", TemplateInstanceID: `PCI\VEN_15AD&DEV_07B0\TEMPLATE`, InstanceID: `PCI\VEN_15AD&DEV_07B0\FIRST`, ClassKey: "0010"}
	if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.InstanceID = `PCI\VEN_15AD&DEV_07B0\MEASURED`
	if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("wrong identity was reused; writes=%d", writes)
	}
}
func TestIdentityRebuildAndDeleteRefuseLiveClient(t *testing.T) {
	z := seedSuperPool()
	exporter := &fakeExporter{sessions: []string{"001122334455"}}
	a := New("node-a", z, exporter)
	req := storage.IdentityLayerReq{MAC: "001122334455", ConfigID: "cfg", SnapshotName: "0", InstanceID: `PCI\VEN_15AD&DEV_07B0\FIRST`, ClassKey: "0010"}
	if err := a.CreateIdentityLayer(context.Background(), req); err == nil {
		t.Fatal("live identity layer replacement accepted")
	}
	if err := a.DeleteIdentityLayer(context.Background(), req.MAC); err == nil {
		t.Fatal("live identity layer deletion accepted")
	}
	if exporter.tornTarget != "" {
		t.Fatalf("live target was detached: %s", exporter.tornTarget)
	}
}

func TestIdentityLayerRebuildsWhenTemplateContentChanges(t *testing.T) {
	z := seedSuperPool()
	a := New("node-a", z)
	var seen []*storage.NICTemplate
	a.identityWriteFn = func(_ context.Context, _ string, r storage.IdentityLayerReq) error {
		seen = append(seen, r.Template)
		return nil
	}
	req := storage.IdentityLayerReq{MAC: "001122334455", ConfigID: "cfg", SnapshotName: "0", InstanceID: `PCI\VEN_8086&DEV_10D3\001122FFFF33445500`,
		Template: &storage.NICTemplate{Reg: "v1", GUID: "{11111111-2222-3333-4444-555555555555}"}}
	for _, reg := range []string{"v1", "v1", "v2"} {
		req.Template = &storage.NICTemplate{Reg: reg, GUID: req.Template.GUID}
		if err := a.CreateIdentityLayer(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0].Reg != "v1" || seen[1].Reg != "v2" {
		t.Fatalf("template layer reuse wrong: %+v", seen)
	}
}

// 复制删数据或挪开旧副本前放掉钉在上面的身份层：只放没人用的，放掉的下次开机按当前还原点重建。
func TestReleaseIdentityLayersReportsBusyAndPreservesUnrelatedData(t *testing.T) {
	z := newFakePool()
	aside := "tank/nd-rebuilding-1"
	layer := func(n string) string { return "tank/run/IDENT-00000000000" + n }
	z.datasets = []string{aside + "/cfg", "tank/nd/cfg", layer("1"), layer("2"), "tank/run/CLIENT-000000000002", layer("3"), layer("4"), layer("5")}
	z.snaps = map[string]bool{aside + "/cfg@0": true, "tank/nd/cfg@0": true, "tank/nd/cfg@old": true, layer("2") + "@ready": true}
	z.origin = map[string]string{
		layer("1"):                     aside + "/cfg@0", // 钉在挪开的副本上、没人用 → 放
		layer("2"):                     aside + "/cfg@0", // 这台还有客户机克隆，可能在开机或刚关机 → 留
		"tank/run/CLIENT-000000000002": layer("2") + "@ready",
		layer("3"):                     aside + "/cfg@0",  // 有活跃会话 → 留
		layer("4"):                     "tank/nd/cfg@0",   // 来源还在当前目录 → 不相干
		layer("5"):                     "tank/nd/cfg@old", // 匹配另一待删点，但排在占用层后，本轮不处理
	}
	a := New("node-a", z, &fakeExporter{sessions: []string{"000000000003"}})

	released, err := a.ReleaseIdentityLayers(context.Background(), []string{aside, "tank/nd/cfg@old"})
	if err == nil || !strings.Contains(err.Error(), "000000000002") {
		t.Fatalf("busy matching layer must block the caller: %v", err)
	}
	if strings.Join(released, ",") != "000000000001" {
		t.Fatalf("released = %v", released)
	}
	for _, kept := range []string{layer("2"), "tank/run/CLIENT-000000000002", layer("3"), layer("4"), layer("5")} {
		if !z.present(kept) {
			t.Fatalf("%s 不该被删：%v", kept, z.destroyed)
		}
	}
	if z.present(layer("1")) {
		t.Fatalf("earlier idle layer not released: %v", z.datasets)
	}
}
