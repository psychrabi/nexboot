package ops

import (
	"context"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

func TestPoolCannotBeDestroyedDuringInstallation(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	if err := st.Servers().Create(ctx, domain.Server{ID: "server-a", Name: "server-a", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "pool", ServerID: "server-a", Name: "tank"}
	if err := st.Pools().Create(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, domain.InstallSession{ID: "unfinished", MAC: "001122334455", Token: "token", PoolID: "pool", Phase: "failed"}); err != nil {
		t.Fatal(err)
	}
	agent := &fakePoolStorage{poolName: "tank"}
	_, err := (PoolService{Store: st, Storage: agent}).Destroy(ctx, pool.ID)
	if err == nil || agent.destroyedName != "" {
		t.Fatalf("unfinished installation lost its pool: destroy=%q err=%v", agent.destroyedName, err)
	}
}

func TestCatalogueRecoveryNeverPublishesUnverifiedInstallation(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	if err := st.InstallSessions().Create(ctx, domain.InstallSession{ID: "unfinished", MAC: "001122334455", Token: "token", ImageID: "iso_candidate", Phase: "saving"}); err != nil {
		t.Fatal(err)
	}
	agent := &fakeInventoryStorage{inv: storage.PoolInventory{Datasets: []string{"iso_candidate", "iso_candidate_default"}, Snapshots: []string{"iso_candidate@0", "iso_candidate_default@0"}, Origins: map[string]string{"iso_candidate_default": "iso_candidate@0"}}}
	report, err := (ReconcileService{Store: st, Storage: agent}).RecoverCatalogue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Images != 0 || report.Configs != 0 {
		t.Fatalf("unverified ISO must not become a ready image after service restart: %+v", report)
	}
	consistency, err := (ReconcileService{Store: st, Storage: agent}).Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !consistency.OK {
		t.Fatalf("installation candidate must not cause orphan alarms: %+v", consistency)
	}
}
