package assets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

type adaptationActivityStore struct {
	store.Store
	afterScan func()
	scanErr   error
}

func (s adaptationActivityStore) PESessions() store.PESessionRepo {
	return adaptationActivitySessions{PESessionRepo: s.Store.PESessions(), afterScan: s.afterScan, scanErr: s.scanErr}
}

type adaptationActivitySessions struct {
	store.PESessionRepo
	afterScan func()
	scanErr   error
}

func (r adaptationActivitySessions) ListActive(ctx context.Context) ([]domain.PESession, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	active, err := r.PESessionRepo.ListActive(ctx)
	if r.afterScan != nil {
		r.afterScan()
	}
	return active, err
}

func TestDeleteCannotSlipBetweenAdaptationClaimAndPersistedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st := seedReductionStore(t)
	releaseAdapt, err := BeginModelAdaptation(ctx, st, "img-1", "cfg-1")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAdapt()
	scanned, resume := make(chan struct{}), make(chan struct{})
	defer close(resume)
	racing := adaptationActivityStore{Store: st, afterScan: func() {
		close(scanned)
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}}
	disk := &fakeReductionStorage{}
	done := make(chan error, 1)
	go func() { _, err := (ReductionService{Store: racing, Storage: disk}).Delete(ctx, "red-2"); done <- err }()

	var deleteErr error
	paused := false
	select {
	case <-scanned:
		paused = true // The old implementation has already read an empty PE snapshot.
	case deleteErr = <-done:
		// A held preparation claim may refuse deletion before querying active sessions.
	case <-ctx.Done():
		t.Fatal("delete did not reach the catalogue gate")
	}
	now := time.Now().UTC()
	sess := domain.PESession{ID: "adapt-handoff", TerminalID: "t1", MAC: "001122334455", Token: "adapt-handoff-token", Mode: domain.PEModeAdapt, State: domain.PEStatePending, ConfigID: "cfg-1", ReductionID: "red-1", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.PESessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	releaseAdapt() // The persisted session now owns the source image instead of the temporary claim.
	if paused {
		resume <- struct{}{}
		select {
		case deleteErr = <-done:
		case <-ctx.Done():
			t.Fatal("delete did not finish after the handoff")
		}
	}
	if !errors.Is(deleteErr, errs.ErrConflict) {
		t.Errorf("delete must refuse an adaptation source throughout the handoff: %v", deleteErr)
	}
	if _, err := st.Reductions().Get(ctx, "red-2"); err != nil || disk.deletedName != "" {
		t.Fatalf("adaptation source was deleted: storage=%q, err=%v", disk.deletedName, err)
	}
	if _, err := (ReductionService{Store: st, Storage: disk}).Delete(ctx, "red-1"); !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "机型适配") {
		t.Fatalf("persisted adaptation must continue to protect the source: %v", err)
	}
	if err := st.Images().Create(ctx, domain.Image{ID: "img-other", Name: "other", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-other", ImageID: "img-other", Name: "default", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	releaseOther, err := BeginModelAdaptation(ctx, st, "img-other", "cfg-other")
	if err != nil {
		t.Fatalf("adaptation must not reserve unrelated images: %v", err)
	}
	releaseOther()
}

func TestFailedAdaptationActivityScanReleasesCatalogueGate(t *testing.T) {
	ctx := context.Background()
	st := seedReductionStore(t)
	cause := errors.New("PE session scan interrupted")
	broken := adaptationActivityStore{Store: st, scanErr: cause}
	if _, err := (ReductionService{Store: broken, Storage: &fakeReductionStorage{}}).Delete(ctx, "red-2"); !errors.Is(err, cause) {
		t.Fatalf("activity scan error must be preserved: %v", err)
	}
	if _, err := (ReductionService{Store: st, Storage: &fakeReductionStorage{}}).Delete(ctx, "red-2"); err != nil {
		t.Fatalf("failed activity scan retained the catalogue gate: %v", err)
	}
}
