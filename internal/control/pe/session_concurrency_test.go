package pe

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

type pausedExpiryStore struct {
	store.Store
	listed, release chan struct{}
}

func (s pausedExpiryStore) PESessions() store.PESessionRepo {
	return pausedExpirySessions{PESessionRepo: s.Store.PESessions(), listed: s.listed, release: s.release}
}

type pausedExpirySessions struct {
	store.PESessionRepo
	listed, release chan struct{}
}

func (r pausedExpirySessions) ListActive(ctx context.Context) ([]domain.PESession, error) {
	active, err := r.PESessionRepo.ListActive(ctx)
	close(r.listed)
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return active, err
}

func TestPEExpiryCannotFailASessionCompletedAfterItsScan(t *testing.T) {
	svc, st, _ := newSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := svc.now()
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	svc.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	listed, release := make(chan struct{}), make(chan struct{})
	svc.Store = pausedExpiryStore{Store: st, listed: listed, release: release}
	var cleaned atomic.Bool
	svc.OnFailure = func(context.Context, domain.PESession) error { cleaned.Store(true); return nil }
	done := make(chan int, 1)
	go func() { n, _ := svc.ExpireStale(ctx); done <- n }()
	select {
	case <-listed:
	case <-ctx.Done():
		t.Fatal("expiry scan never started")
	}
	err = svc.Complete(ctx, sess.Token, PEResult{OK: true})
	clock.Store(start.Add(DefaultTTL + time.Second).UnixNano())
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("completed session was expired: %d", n)
		}
	case <-ctx.Done():
		t.Fatal("expiry scan did not finish")
	}
	sess, _ = st.PESessions().Get(ctx, sess.ID)
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if cleaned.Load() || sess.State != domain.PEStateDone || task.Status != domain.TaskStatusSuccess {
		t.Fatalf("accepted success must never be cleaned up or changed to failure: cleaned=%v session=%s task=%s", cleaned.Load(), sess.State, task.Status)
	}
}
