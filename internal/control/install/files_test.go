package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestInstallationLogsNeverPersistOrReturnCredentials(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	sess := domain.InstallSession{ID: "secret", MAC: "001122334455", Token: "private-token", Password: "PrivatePassword!", Phase: "installing", TaskID: "secret-task", MediaDir: dir}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "password.hash"), []byte("$6$private-password-hash"), 0600); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: st}
	raw := "curl /install/private-token/report PrivatePassword! $6$private-password-hash\nnode.session.auth.password = unknown-chap-secret"
	for _, name := range []string{"storage.log", "program.log", "iscsi.log"} {
		if err := svc.SaveLog(ctx, sess.Token, name, strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		f, err := svc.TaskLog(ctx, sess.TaskID, name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(f.Content), "unknown-chap-secret") {
			t.Fatal("unknown CHAP credential exposed")
		}
	}
	if err := svc.SaveLog(ctx, sess.Token, "console.log", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "logs", "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-token") || strings.Contains(string(b), "PrivatePassword!") || strings.Contains(string(b), "$6$") {
		t.Fatalf("raw credentials persisted: %q", b)
	}
	// Older logs must also be sanitized on read.
	if err := os.WriteFile(filepath.Join(dir, "logs", "console.log"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := svc.TaskLog(ctx, sess.TaskID, "console.log")
	if err != nil {
		t.Fatal(err)
	}
	if file.Path != "" || strings.Contains(string(file.Content), "private-token") || strings.Contains(string(file.Content), "PrivatePassword!") {
		t.Fatalf("task log exposed credentials: %+v", file)
	}
}

func TestOrdinaryBootDoesNotWaitForInstallationPreparation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := Service{Store: st}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan bool, 1)
	go func() { _, handled, _ := svc.BootScript(ctx, "001122334455"); done <- handled }()
	select {
	case handled := <-done:
		if handled {
			t.Fatal("unregistered installation MAC was handled")
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary boot blocked behind an unrelated installation")
	}
}
