package store

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func TestInstallSessionPersistsWithoutTerminalOrGroup(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	s := domain.InstallSession{ID: "iso-1", MAC: "AABBCCDDEEFF", Token: "secret", Phase: "waiting", Name: "new-system", Attempt: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.InstallSessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := st.InstallSessions().ActiveByMAC(ctx, s.MAC)
	if err != nil || got.ID != s.ID {
		t.Fatalf("session = %+v, %v", got, err)
	}
	if err := st.InstallSessions().Create(ctx, domain.InstallSession{ID: "iso-2", MAC: s.MAC, Token: "other", Phase: "waiting"}); err == nil {
		t.Fatal("one MAC must not have two active installations")
	}
	got.Phase = "ready"
	if err := st.InstallSessions().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InstallSessions().ActiveByMAC(ctx, s.MAC); err == nil {
		t.Fatal("finished session must release boot override")
	}
	if token, err := st.InstallSessions().ByToken(ctx, "secret"); err != nil || token.Name != "new-system" {
		t.Fatalf("token = %+v, %v", token, err)
	}
}

func TestFailedInstallSessionBlocksFallbackUntilCancelled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	s := domain.InstallSession{ID: "failed-install", MAC: "112233445566", Token: "failed-token", Phase: "failed"}
	if err := st.InstallSessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InstallSessions().ActiveByMAC(ctx, s.MAC); err != nil {
		t.Fatalf("failed installation must retain boot override: %v", err)
	}
	s.Phase = "cancelled"
	if err := st.InstallSessions().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InstallSessions().ActiveByMAC(ctx, s.MAC); err == nil {
		t.Fatal("explicit cancellation must release boot override")
	}
}
