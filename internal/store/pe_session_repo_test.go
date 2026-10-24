package store

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func TestPESessionRepoActiveAndTokenLookups(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	mk := func(id, terminal, token string, state domain.PESessionState) domain.PESession {
		return domain.PESession{ID: id, TerminalID: terminal, MAC: "AABBCCDDEEFF", Mode: domain.PEModeProbe, TaskID: "task-" + id, Token: token,
			EnvID: "winpe-win11", BCDID: "winpe-bcd", State: state, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(20 * time.Minute)}
	}
	for _, s := range []domain.PESession{mk("s1", "t1", "tok1", domain.PEStateDone), mk("s2", "t1", "tok2", domain.PEStatePending), mk("s3", "t2", "tok3", domain.PEStateBooted)} {
		if err := st.PESessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	active, err := st.PESessions().ActiveByTerminal(ctx, "t1")
	if err != nil || active.ID != "s2" {
		t.Fatalf("active = %+v err = %v", active, err)
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, "t9"); err == nil {
		t.Fatal("没有进行中会话的终端应返回未找到")
	}
	byTok, err := st.PESessions().ByToken(ctx, "tok3")
	if err != nil || byTok.TerminalID != "t2" || !byTok.ExpiresAt.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("byToken = %+v err = %v", byTok, err)
	}
	all, err := st.PESessions().ListActive(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("active = %+v err = %v", all, err)
	}
	byTok.State = domain.PEStateFailed
	if err := st.PESessions().Update(ctx, byTok); err != nil {
		t.Fatal(err)
	}
	if all, _ = st.PESessions().ListActive(ctx); len(all) != 1 {
		t.Fatalf("after fail active = %+v", all)
	}
}
