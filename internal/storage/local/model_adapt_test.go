package local

import (
	"context"
	"errors"
	"github.com/tianwei/diskless/internal/storage"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAdaptationExportSurvivesOrdinaryClientLifecycle(t *testing.T) {
	for _, operation := range []string{"reclaim", "cleanup", "identity-rebuild", "identity-delete", "normal-boot", "super-stop", "super-cleanup"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			const mac = "AABBCCDDEEFF"
			z := seedSuperPool()
			z.datasets[2] = "tank/run/CLIENT-" + mac
			delete(z.origin, "tank/run/SCLIENT-"+mac)
			z.origin["tank/run/CLIENT-"+mac] = "tank/nd/cfg@0"
			exporter := &fakeExporter{}
			a := New("node-a", z, exporter)
			a.identityWriteFn = func(context.Context, string, storage.IdentityLayerReq) error { return nil }
			if _, err := a.CreateModelAdaptation(ctx, storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: mac, System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}}}); err != nil {
				t.Fatal(err)
			}
			// Simulate a legacy leftover after the adaptation has obtained its target.
			if strings.HasPrefix(operation, "super-") {
				z.datasets = append(z.datasets, "tank/run/SCLIENT-"+mac)
				z.origin["tank/run/SCLIENT-"+mac] = "tank/nd/cfg@0"
			}
			// The old CLIENT still exists, but LUN 0 now belongs to the exported
			// ADAPT disk. There is no live initiator while PE waits for power-on.
			if len(exporter.reqs) != 1 || exporter.reqs[0].VolPath != "/dev/zvol/tank/run/ADAPT-"+mac {
				t.Fatalf("adaptation was not exported: %+v", exporter.reqs)
			}
			exporter.tornTarget, exporter.tornLUNs = "", nil
			exporter.deletedTarget, exporter.deletedLUNs = "", nil
			var err error
			switch operation {
			case "reclaim":
				var reclaimed bool
				reclaimed, err = a.ReclaimIdleClientClones(ctx, "aa:bb:cc:dd:ee:ff", time.Now())
				if reclaimed || err != nil {
					t.Errorf("PE-owned export was reclaimed: done=%v err=%v", reclaimed, err)
				}
			case "cleanup":
				err = a.CleanupClientClones(ctx, "aa:bb:cc:dd:ee:ff", nil)
			case "identity-rebuild":
				err = a.CreateIdentityLayer(ctx, storage.IdentityLayerReq{MAC: "aa:bb:cc:dd:ee:ff", Background: true, ConfigID: "cfg", SnapshotName: "0", TemplateInstanceID: "TEMPLATE", InstanceID: "CURRENT", ClassKey: "0010"})
			case "identity-delete":
				err = a.DeleteIdentityLayer(ctx, "aa:bb:cc:dd:ee:ff")
			case "normal-boot":
				_, err = a.CreateClientLUN(ctx, storage.ClientReq{MAC: "aa:bb:cc:dd:ee:ff", System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}})
				if _, marked := a.provisionedAt.Load(mac); marked {
					t.Error("refused normal boot marked a fresh provision")
				}
			case "super-stop":
				_, err = a.SuperStop(ctx, storage.SuperStopReq{MAC: "aa:bb:cc:dd:ee:ff", ConfigID: "cfg"})
			case "super-cleanup":
				err = a.CleanupSuperClones(ctx, "aa:bb:cc:dd:ee:ff")
			}
			if operation != "reclaim" && (err == nil || !strings.Contains(err.Error(), mac) || !strings.Contains(err.Error(), "适配")) {
				t.Errorf("ordinary lifecycle must refuse the adaptation owner with its MAC: %v", err)
			}
			if exporter.deletedTarget != "" || len(exporter.deletedLUNs) != 0 || exporter.tornTarget != "" {
				t.Fatalf("adaptation target/LUN was detached: %+v", exporter)
			}
			if !slices.Contains(z.datasets, "tank/run/ADAPT-"+mac) || !slices.Contains(z.datasets, "tank/run/CLIENT-"+mac) {
				t.Fatalf("another owner changed the pending disks: %v", z.datasets)
			}
		})
	}
}

func TestOrdinaryLifecycleRefusesUnreadableAdaptationOwnership(t *testing.T) {
	for _, operation := range []string{"super-stop", "super-cleanup", "cleanup", "normal-boot"} {
		t.Run(operation, func(t *testing.T) {
			want := errors.New("pool inventory unavailable")
			exporter := &fakeExporter{}
			a := New("node-a", &failingListPool{fakePool: seedSuperPool(), err: want}, exporter)
			ctx := context.Background()
			var err error
			switch operation {
			case "super-stop":
				_, err = a.SuperStop(ctx, storage.SuperStopReq{MAC: "AABBCCDDEEFF", ConfigID: "cfg"})
			case "super-cleanup":
				err = a.CleanupSuperClones(ctx, "AABBCCDDEEFF")
			case "cleanup":
				err = a.CleanupClientClones(ctx, "AABBCCDDEEFF", nil)
			case "normal-boot":
				_, err = a.CreateClientLUN(ctx, storage.ClientReq{MAC: "AABBCCDDEEFF"})
				if _, marked := a.provisionedAt.Load("AABBCCDDEEFF"); marked {
					t.Error("failed inventory read marked a fresh provision")
				}
			}
			if !errors.Is(err, want) {
				t.Fatalf("want inventory error, got %v", err)
			}
			if exporter.deletedTarget != "" || len(exporter.deletedLUNs) != 0 || exporter.tornTarget != "" {
				t.Fatalf("unreadable owner caused export deletion: %+v", exporter)
			}
		})
	}
}

func TestAdaptationOwnershipSurvivesAgentRestart(t *testing.T) {
	ctx := context.Background()
	z := seedSuperPool()
	z.datasets[2] = "tank/run/CLIENT-AABBCCDDEEFF"
	delete(z.origin, "tank/run/SCLIENT-AABBCCDDEEFF")
	z.origin[z.datasets[2]] = "tank/nd/cfg@0"
	exporter := &fakeExporter{}
	a := New("node-a", z, exporter)
	if _, err := a.CreateModelAdaptation(ctx, storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: "AABBCCDDEEFF", System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}}}); err != nil {
		t.Fatal(err)
	}
	exporter.tornTarget, exporter.tornLUNs = "", nil
	a = New("node-a", z, exporter) // No in-memory timestamps survive a restart.
	if done, err := a.ReclaimIdleClientClones(ctx, "aa:bb:cc:dd:ee:ff", time.Now()); done || err != nil {
		t.Fatalf("restarted agent reclaimed the adaptation owner: %v %v", done, err)
	}
	if exporter.tornTarget != "" || exporter.deletedTarget != "" || len(exporter.deletedLUNs) != 0 {
		t.Fatalf("restart lost target ownership: %+v", exporter)
	}
}

func TestAdaptationOwnerCanReleaseTargetForOrdinaryReclaim(t *testing.T) {
	for _, operation := range []string{"cleanup", "save"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			z := seedSuperPool()
			z.datasets[2] = "tank/run/CLIENT-AABBCCDDEEFF"
			delete(z.origin, "tank/run/SCLIENT-AABBCCDDEEFF")
			z.origin[z.datasets[2]] = "tank/nd/cfg@0"
			a := New("node-a", z, &fakeExporter{})
			req := storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: "AABBCCDDEEFF", System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}}}
			if _, err := a.CreateModelAdaptation(ctx, req); err != nil {
				t.Fatal(err)
			}
			// The owner's explicit retry is still allowed to replace its export.
			if _, err := a.CreateModelAdaptation(ctx, req); err != nil {
				t.Fatal(err)
			}
			if operation == "cleanup" {
				if err := a.CleanupModelAdaptation(ctx, req.MAC); err != nil {
					t.Fatal(err)
				}
			} else {
				red, err := a.SaveModelAdaptation(ctx, storage.SuperStopReq{MAC: req.MAC, ConfigID: "cfg", ReductionName: "@adapted"})
				if err != nil || red.ID != "cfg_adapted" || !z.snaps["tank/nd/cfg@adapted"] {
					t.Fatalf("owner could not save: %+v %v", red, err)
				}
			}
			if done, err := a.ReclaimIdleClientClones(ctx, req.MAC, time.Now()); !done || err != nil {
				t.Fatalf("released owner still blocked ordinary reclaim: %v %v", done, err)
			}
			if slices.Contains(z.datasets, "tank/run/ADAPT-AABBCCDDEEFF") || slices.Contains(z.datasets, "tank/run/CLIENT-AABBCCDDEEFF") {
				t.Fatalf("completed lifecycle left temporary disks: %v", z.datasets)
			}
		})
	}
}

func TestAdaptationOwnershipDoesNotBlockAnotherMAC(t *testing.T) {
	ctx := context.Background()
	z := seedSuperPool()
	z.datasets = z.datasets[:2]
	delete(z.origin, "tank/run/SCLIENT-AABBCCDDEEFF")
	exporter := &fakeExporter{}
	a := New("node-a", z, exporter)
	if _, err := a.CreateModelAdaptation(ctx, storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: "AABBCCDDEEFF", System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}}}); err != nil {
		t.Fatal(err)
	}
	z.datasets = append(z.datasets, "tank/run/CLIENT-AABBCCDDEE00")
	z.origin["tank/run/CLIENT-AABBCCDDEE00"] = "tank/nd/cfg@0"
	if done, err := a.ReclaimIdleClientClones(ctx, "aa:bb:cc:dd:ee:00", time.Now()); !done || err != nil {
		t.Fatalf("another MAC was blocked: %v %v", done, err)
	}
	if !strings.HasSuffix(exporter.deletedTarget, "aabbccddee00") || !slices.Contains(z.datasets, "tank/run/ADAPT-AABBCCDDEEFF") {
		t.Fatalf("another MAC's reclaim changed the adaptation owner: target=%s datasets=%v", exporter.deletedTarget, z.datasets)
	}
}

func TestModelAdaptSaveResumesEveryStorageStep(t *testing.T) {
	for _, stopAt := range []string{"promote tank/run/ADAPT-AABBCCDDEEFF", "rename tank/nd/cfg", "rename tank/run/ADAPT-AABBCCDDEEFF", "snapshot tank/nd/cfg@adapted"} {
		t.Run(stopAt, func(t *testing.T) {
			z := seedSuperPool()
			z.datasets[2] = "tank/run/ADAPT-AABBCCDDEEFF"
			delete(z.origin, "tank/run/SCLIENT-AABBCCDDEEFF")
			z.origin["tank/run/ADAPT-AABBCCDDEEFF"] = "tank/nd/cfg@0"
			z.failOn = stopAt
			a := New("node-a", z)
			req := storage.SuperStopReq{MAC: "aabbccddeeff", ConfigID: "cfg", ReductionName: "@adapted"}
			if _, err := a.SaveModelAdaptation(context.Background(), req); err == nil {
				t.Fatal("expected interrupted save")
			}
			z.failOn = ""
			red, err := a.SaveModelAdaptation(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if red.ID != "cfg_adapted" || !z.snaps["tank/nd/cfg@adapted"] || slices.Contains(z.datasets, "tank/run/ADAPT-AABBCCDDEEFF") {
				t.Fatalf("incorrect final state: %+v", red)
			}
		})
	}
}

func TestModelAdaptationRefusesOtherDiskOwnersBeforeChangingTarget(t *testing.T) {
	for _, owner := range []string{"installation", "super", "active-client"} {
		t.Run(owner, func(t *testing.T) {
			ctx := context.Background()
			const mac = "AABBCCDDEEFF"
			z := newFakePool()
			z.datasets = []string{"tank/nd/cfg"}
			z.snaps["tank/nd/cfg@0"] = true
			exporter := &fakeExporter{}
			switch owner {
			case "installation":
				z.datasets = append(z.datasets, z.Dataset(storage.InstallationDiskName(mac, "install-old")))
			case "super":
				z.datasets = append(z.datasets, z.Dataset(storage.SuperClientCloneName(mac)))
			case "active-client":
				exporter.sessions = []string{"aa:bb:cc:dd:ee:ff"}
			}
			a := New("node-a", z, exporter)
			_, err := a.CreateModelAdaptation(ctx, storage.ModelAdaptReq{ClientReq: storage.ClientReq{MAC: mac, System: storage.ClientSource{ConfigID: "cfg", SnapshotName: "0"}}})
			if err == nil || !strings.Contains(err.Error(), mac) {
				t.Errorf("adaptation must refuse another disk owner and name the client: %v", err)
			}
			if exporter.tornTarget != "" || len(exporter.reqs) != 0 || len(z.ops) != 0 {
				t.Fatalf("adaptation changed another owner's target or disk: target=%q exports=%v operations=%v", exporter.tornTarget, exporter.reqs, z.ops)
			}
		})
	}
}

func TestModelAdaptationCleanupDoesNotDetachAnotherOwnersTarget(t *testing.T) {
	for _, owner := range []string{"installation", "super", "ordinary-client"} {
		t.Run(owner, func(t *testing.T) {
			ctx := context.Background()
			const mac = "AABBCCDDEEFF"
			z := newFakePool()
			switch owner {
			case "installation":
				z.datasets = []string{z.Dataset(storage.InstallationDiskName(mac, "install-current"))}
			case "super":
				z.datasets = []string{z.Dataset(storage.SuperClientCloneName(mac))}
			case "ordinary-client":
				z.datasets = []string{z.Dataset(storage.ClientCloneName(mac))}
			}
			exporter := &fakeExporter{}
			a := New("node-a", z, exporter)
			if err := a.CleanupModelAdaptation(ctx, mac); err != nil {
				t.Fatal(err)
			}
			if exporter.tornTarget != "" || len(z.ops) != 0 {
				t.Fatalf("failed adaptation without its own disk detached another owner: target=%q operations=%v", exporter.tornTarget, z.ops)
			}
		})
	}
}
