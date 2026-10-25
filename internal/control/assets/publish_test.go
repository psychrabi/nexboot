package assets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

type fakePublishStorage struct {
	storage.StorageAgent
	req     storage.PublishDataDiskReq
	called  bool
	written []int64 // Return these byte counts on successive queries
	result  storage.PublishDataDiskResult
	err     error
	noDisk  map[int]bool // LUNs without Super Admin clones in storage
}

func (f *fakePublishStorage) PublishDataDisk(_ context.Context, req storage.PublishDataDiskReq) (storage.PublishDataDiskResult, error) {
	f.req, f.called = req, true
	if f.result.Reduction.ID == "" {
		f.result.Reduction = domain.Reduction{ID: storage.ReductionID(req.ConfigID, strings.TrimPrefix(req.ReductionName, "@")),
			ConfigID: req.ConfigID, Name: req.ReductionName, Status: domain.ReductionStatusReady}
	}
	return f.result, f.err
}

func (f *fakePublishStorage) SuperDiskWritten(_ context.Context, _ string, lun int) (int64, error) {
	if f.noDisk[lun] {
		// Mimic real ZFS behavior when this client's Super Admin clone is absent.
		return 0, errors.New("dataset does not exist")
	}
	if len(f.written) == 0 {
		return 0, nil
	}
	v := f.written[0]
	if len(f.written) > 1 {
		f.written = f.written[1:]
	}
	return v, nil
}

// One running Super Admin client with a data disk mounted at D:.
func seedPublishStore(t *testing.T) (store.Store, domain.Group, domain.Terminal) {
	t.Helper()
	ctx := context.Background()
	st := newImageTestStore(t)
	group := seedTerminalGroup(t, ctx, st, 5)
	now := time.Now().UTC()
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-games", ImageID: "img-1", Name: "Games Disk", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-games-0", ConfigID: "cfg-games", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := st.GroupDisks().Create(ctx, domain.GroupDisk{ID: "gd-1", GroupID: group.ID, ConfigID: "cfg-games", ImageID: "img-1", MountTarget: "D:"}); err != nil {
		t.Fatal(err)
	}
	terminal := domain.Terminal{ID: "t-1", Name: "Teacher PC", MAC: "001122334455", IP: "192.168.1.10", GroupID: group.ID,
		IsSuper: true, State: domain.TerminalStateOnline}
	if err := st.Terminals().Create(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	return st, group, terminal
}

// Publishing without shutdown creates an applied restore point for clients' next boot.
func TestPublishDataDiskSavesAndMakesItCurrent(t *testing.T) {
	ctx := context.Background()
	st, _, terminal := seedPublishStore(t)
	pool := &fakePublishStorage{}
	svc := TerminalService{Store: st, Storage: pool, QuietWindow: time.Millisecond}

	res, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-1", Name: "Game installed"})
	if err != nil {
		t.Fatal(err)
	}
	if !pool.called || pool.req.MAC != terminal.MAC || pool.req.LUN != 1 || pool.req.ConfigID != "cfg-games" {
		t.Fatalf("Storage request = %#v", pool.req)
	}
	if pool.req.ReductionName != "@Game installed" && !strings.HasPrefix(pool.req.ReductionName, "@") {
		t.Fatalf("Restore point name = %q", pool.req.ReductionName)
	}
	if res.Reduction == nil {
		t.Fatalf("Result is missing restore point：%#v", res)
	}
	// Persist the restore point and make it current.
	if _, err := st.Reductions().Get(ctx, res.Reduction.ID); err != nil {
		t.Fatalf("Restore point was not persisted：%v", err)
	}
	cfg, err := st.Configs().Get(ctx, "cfg-games")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultReductionID == nil || *cfg.DefaultReductionID != res.Reduction.ID {
		t.Fatalf("Published point was not made current：%#v", cfg.DefaultReductionID)
	}
	// Publishing must not disable Super Admin mode.
	after, _ := st.Terminals().Get(ctx, terminal.ID)
	if !after.IsSuper {
		t.Fatal("Client must remain Super Admin after publishing")
	}
}

// Reject publishing disks still receiving writes to prevent distributing partial installs.
func TestPublishDataDiskRefusesWhileTheDiskIsStillBeingWritten(t *testing.T) {
	ctx := context.Background()
	st, _, terminal := seedPublishStore(t)
	pool := &fakePublishStorage{written: []int64{100, 4096}} // Byte count increases between samples
	svc := TerminalService{Store: st, Storage: pool, QuietWindow: time.Millisecond}

	_, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-1", Name: "Game installed"})
	if !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "still being written") {
		t.Fatalf("Should reject and identify disk writes：%v", err)
	}
	if pool.called {
		t.Fatal("Disk must not change while writes are in progress")
	}
}

// If the disk cannot remount after a successful publish, report success with reboot guidance in Detail.
func TestPublishDataDiskReportsAPublishedDiskThatDidNotComeBack(t *testing.T) {
	ctx := context.Background()
	st, _, terminal := seedPublishStore(t)
	pool := &fakePublishStorage{err: storage.DiskNotReattached{LUN: 1, Err: errors.New("export failed")}}
	svc := TerminalService{Store: st, Storage: pool, QuietWindow: time.Millisecond}

	res, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-1", Name: "Game installed"})
	if err != nil {
		t.Fatalf("Publish succeeded and must not report an error：%v", err)
	}
	if res.Reduction == nil {
		t.Fatalf("Result is missing restore point：%#v", res)
	}
	if !strings.Contains(res.Detail, "reboot") {
		t.Fatalf("Give operator recovery instructions：%q", res.Detail)
	}
	if _, err := st.Reductions().Get(ctx, res.Reduction.ID); err != nil {
		t.Fatalf("Successful publication must persist：%v", err)
	}
}

func TestPublishDataDiskChecksTheMachineAndTheDisk(t *testing.T) {
	ctx := context.Background()
	st, _, terminal := seedPublishStore(t)
	pool := &fakePublishStorage{}
	svc := TerminalService{Store: st, Storage: pool, QuietWindow: time.Millisecond}

	// Not a Super Admin client
	normal := terminal
	normal.IsSuper = false
	if err := st.Terminals().Update(ctx, normal); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-1", Name: "x"}); !errors.Is(err, ErrTerminalSuperRequired) {
		t.Fatalf("Only Super Admin clients can publish：%v", err)
	}
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}

	// This disk is not in the client's group
	if _, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-nope", Name: "x"}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Reject disks outside this group：%v", err)
	}

	// Restore point name conflicts with an existing point
	if _, err := svc.PublishDataDisk(ctx, terminal.ID, PublishDataDiskRequest{DiskID: "gd-1", Name: "0"}); err == nil {
		t.Fatal("Reject duplicate restore point names")
	}
	if pool.called {
		t.Fatal("Validation must not mutate storage")
	}
}

// Data disk listing reports current point, unpublished written bytes and publish readiness.
// A disk not yet booted should be not ready to publish, not a failure with zero changes.
func TestSuperDisksTellsWhatEachDiskIsPublishingAndWhatIsPending(t *testing.T) {
	ctx := context.Background()
	st, group, terminal := seedPublishStore(t)
	now := time.Now().UTC()
	// Second disk has not been booted and has no Super Admin clone.
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-doc", ImageID: "img-1", Name: "Documents Disk", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.GroupDisks().Create(ctx, domain.GroupDisk{ID: "gd-2", GroupID: group.ID, ConfigID: "cfg-doc", ImageID: "img-1", MountTarget: "E:"}); err != nil {
		t.Fatal(err)
	}
	// The game disk's current point comes from its applied configuration and shows operator-supplied display name.
	cfg, err := st.Configs().Get(ctx, "cfg-games")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-games-1", ConfigID: "cfg-games", Name: "@9yue14", DisplayName: "September 14 Update", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	current := "red-games-1"
	cfg.DefaultReductionID = &current
	if err := st.Configs().Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	pool := &fakePublishStorage{written: []int64{13 << 30}, noDisk: map[int]bool{2: true}}
	res, err := (TerminalService{Store: st, Storage: pool}).SuperDisks(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("List both data disks：%#v", res)
	}
	games := res.Items[0]
	if games.MountTarget != "D:" || games.LUN != 1 || games.ConfigName != "Games Disk" {
		t.Fatalf("Games Disk row = %#v", games)
	}
	if games.CurrentName != "September 14 Update" || games.Written != 13<<30 || !games.Ready {
		t.Fatalf("Show current point and unpublished changes：%#v", games)
	}
	doc := res.Items[1]
	if doc.MountTarget != "E:" || doc.Ready || doc.Written != 0 {
		t.Fatalf("Never-booted disk should not be ready to publish：%#v", doc)
	}

	// System disks are saved at shutdown and do not belong in this table.
	for _, item := range res.Items {
		if item.LUN == 0 {
			t.Fatalf("System disk should not appear in data disk list：%#v", item)
		}
	}

	// Non-Super Admin clients have no data disk publication table.
	normal := terminal
	normal.IsSuper = false
	if err := st.Terminals().Update(ctx, normal); err != nil {
		t.Fatal(err)
	}
	if _, err := (TerminalService{Store: st, Storage: pool}).SuperDisks(ctx, terminal.ID); !errors.Is(err, ErrTerminalSuperRequired) {
		t.Fatalf("Regular client has no Super Admin disks to list：%v", err)
	}
}
