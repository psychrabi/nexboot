package pe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

func finalizingModelFixture(t *testing.T) (*ModelAdaptService, domain.PESession) {
	t.Helper()
	pe, st, _ := newSessionFixture(t)
	ctx := context.Background()
	hp := domain.HardwareProfile{ID: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100F", CreatedAt: pe.now(), UpdatedAt: pe.now()}
	if err := st.HardwareProfiles().Create(ctx, hp); err != nil {
		t.Fatal(err)
	}
	terminal, _ := st.Terminals().Get(ctx, "t1")
	terminal.HardwareProfileID = hp.ID
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	model := &ModelAdaptService{Store: st, Storage: &modelDiskFake{}, PE: &pe}
	res, err := model.Start(ctx, terminal.ID, ModelAdaptRequest{ConfigID: "win11_default", Name: "e1000"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.PESessions().Get(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	nic := NICIdentity{InstanceID: `PCI\VEN_8086&DEV_100F\4&123&0&0088`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", HWID: `PCI\VEN_8086&DEV_100F`}
	data, _ := json.Marshal(nic)
	result, _ := json.Marshal(PEResult{OK: true, Data: data})
	sess.State, sess.Result = domain.PEStateFinalizing, string(result)
	if err := st.PESessions().Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return model, sess
}

type recoveryModelDisk struct {
	modelDiskFake
	active func(context.Context) ([]string, error)
	save   func(context.Context)
}

func (f *recoveryModelDisk) ActiveClientMACs(ctx context.Context) ([]string, error) {
	if f.active != nil {
		return f.active(ctx)
	}
	return nil, nil
}

func (f *recoveryModelDisk) SaveModelAdaptation(ctx context.Context, req storage.SuperStopReq) (domain.Reduction, error) {
	if f.save != nil {
		f.save(ctx)
	}
	return f.modelDiskFake.SaveModelAdaptation(ctx, req)
}

func TestModelAdaptResumeBoundsClientDisconnectWait(t *testing.T) {
	for _, parentTimeout := range []time.Duration{0, time.Second} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			model, sess := finalizingModelFixture(t)
			ctx := context.Background()
			if parentTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, parentTimeout)
				defer cancel()
			}
			called := false
			model.Storage = &recoveryModelDisk{active: func(waitCtx context.Context) ([]string, error) {
				called = true
				deadline, ok := waitCtx.Deadline()
				if !ok || time.Until(deadline) > DefaultTTL {
					t.Error("restart recovery must bound a client that never releases its disk")
				}
				if parentDeadline, ok := ctx.Deadline(); ok && deadline.After(parentDeadline) {
					t.Error("recovery must respect the caller's earlier deadline")
				}
				return nil, errors.New("disconnect check failed")
			}}
			model.ResumeFinalizing(ctx)
			task, _ := model.Store.Tasks().Get(ctx, sess.TaskID)
			if !called || task.Status != domain.TaskStatusFailed {
				t.Fatalf("recovery failure must be visible, called=%v task=%+v", called, task)
			}
		})
	}
}

type recoveryCatalogueStore struct {
	store.Store
	probe func(string)
}

func (s recoveryCatalogueStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	s.probe("database commit")
	return s.Store.Tx(ctx, fn)
}

func TestModelAdaptPublishExcludesReplicationUntilDatabaseCommit(t *testing.T) {
	model, sess := finalizingModelFixture(t)
	probes := 0
	probe := func(where string) {
		probes++
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		release, err := storage.HoldCatalogueContext(ctx)
		if err == nil {
			release()
			t.Errorf("replication can copy a partial adaptation during %s", where)
		}
	}
	model.Store = recoveryCatalogueStore{Store: model.Store, probe: probe}
	model.Storage = &recoveryModelDisk{save: func(context.Context) { probe("storage save") }}
	model.ResumeFinalizing(context.Background())
	task, _ := model.Store.Tasks().Get(context.Background(), sess.TaskID)
	if task.Status != domain.TaskStatusSuccess || probes < 2 {
		t.Fatalf("must publish storage and database successfully, task=%+v probes=%d", task, probes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := storage.HoldCatalogueContext(ctx)
	if err != nil {
		t.Fatalf("completed publication must release replication: %v", err)
	}
	release()
}
