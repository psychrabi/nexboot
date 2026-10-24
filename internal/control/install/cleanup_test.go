package install

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/remote"
	"github.com/tianwei/diskless/internal/store"
)

func TestCancelOnNewWriterRetainsRetryableOwnership(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "001122334455", ServerID: "owner", Phase: "failed", TaskID: "task-cleanup", Password: "private"}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusFailed}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	svc := Service{Store: st, Storage: disk, Host: disk, NodeID: "writer"}
	_, _ = svc.Cancel(ctx, sess.ID)
	stored, err := st.InstallSessions().Get(ctx, sess.ID)
	if err != nil || stored.Phase != "cancelling" || stored.Password != sess.Password {
		t.Fatalf("cross-node cancellation must remain retryable: phase=%q err=%v", stored.Phase, err)
	}
	if disk.workRemoved {
		t.Fatal("remote owner was not cleaned, local resources must remain")
	}
}

func cleanupFixture(t *testing.T) (*Service, domain.InstallSession, *diskFake) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sess := domain.InstallSession{ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "001122334455", ServerID: "owner", Phase: "failed", TaskID: "task-cleanup", Password: "private-password", Token: "private-token"}
	if err := st.Tasks().Create(ctx, domain.Task{ID: sess.TaskID, Type: domain.TaskTypeInstallImage, Status: domain.TaskStatusFailed}); err != nil {
		t.Fatal(err)
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disk := &diskFake{}
	return &Service{Store: st, Storage: disk, Host: disk, NodeID: "writer"}, sess, disk
}

func writeCleanupMedia(t *testing.T, root string, sess domain.InstallSession) string {
	t.Helper()
	dir := filepath.Join(root, "installations", sess.ID)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"media.iso": "partial ISO", "password.hash": "private-hash", "logs/console.log": sess.Password + " " + sess.Token + " private-hash"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCancelRemoteOwnerWithoutSessionCopyAndRetriesLostResponse(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_response_%t", lostResponse), func(t *testing.T) {
			ctx := context.Background()
			svc, sess, writerDisk := cleanupFixture(t)
			ownerDisk := &diskFake{}
			ownerRoot, writerRoot := t.TempDir(), t.TempDir()
			ownerDir := writeCleanupMedia(t, ownerRoot, sess)
			writerDir := writeCleanupMedia(t, writerRoot, sess)
			// The standby has no Store/session copy; only the writer can change catalogue state.
			owner := Service{NodeID: "owner", Storage: ownerDisk, Host: ownerDisk, FilesDir: func(context.Context) (string, error) { return ownerRoot, nil }}
			srv := httptest.NewServer(remote.Handler{Token: "cluster", NodeID: "owner", CleanupInstallation: owner.CleanupLocalInstallation})
			defer srv.Close()
			client := remote.Client{BaseURL: srv.URL, Token: "cluster"}
			calls := 0
			svc.FilesDir = func(context.Context) (string, error) { return writerRoot, nil }
			svc.CleanupRemote = func(ctx context.Context, req storage.InstallationCleanupReq) error {
				calls++
				if err := client.CleanupInstallationResources(ctx, req); err != nil {
					return err
				}
				if lostResponse && calls == 1 {
					return fmt.Errorf("owner response lost")
				}
				return nil
			}
			v, err := svc.Cancel(ctx, sess.ID)
			if lostResponse {
				if err == nil || v.Phase != "cancelling" || writerDisk.workRemoved {
					t.Fatalf("lost response must retain local state: phase=%s err=%v", v.Phase, err)
				}
				svc.Resume(ctx)
				v, err = svc.Get(ctx, sess.ID)
			}
			if err != nil || v.Phase != "cancelled" || !ownerDisk.workRemoved || !writerDisk.workRemoved {
				t.Fatalf("cleanup phase=%s owner=%t writer=%t err=%v", v.Phase, ownerDisk.workRemoved, writerDisk.workRemoved, err)
			}
			stored, _ := svc.Store.InstallSessions().Get(ctx, sess.ID)
			if stored.Password != "" || ownerDisk.deletedImage != sess.ImageID || writerDisk.deletedImage != sess.ImageID {
				t.Fatal("candidate or credentials retained")
			}
			for _, dir := range []string{ownerDir, writerDir} {
				if _, err := os.Stat(filepath.Join(dir, "media.iso")); !os.IsNotExist(err) {
					t.Fatalf("ISO retained: %v", err)
				}
				b, err := os.ReadFile(filepath.Join(dir, "logs/console.log"))
				if err != nil || strings.Contains(string(b), "private-") {
					t.Fatalf("unsanitized log %q: %v", b, err)
				}
			}
		})
	}
}

func TestCancelRemoteOfflineOrActivePreservesRetry(t *testing.T) {
	for _, scenario := range []string{"offline", "owner-active", "writer-active", "gate"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			svc, sess, disk := cleanupFixture(t)
			ownerDisk := &diskFake{cloned: scenario == "owner-active"}
			ownerRoot := t.TempDir()
			owner := Service{NodeID: "owner", Storage: ownerDisk, Host: ownerDisk, FilesDir: func(context.Context) (string, error) { return ownerRoot, nil }}
			calls := 0
			svc.CleanupRemote = func(ctx context.Context, req storage.InstallationCleanupReq) error {
				calls++
				if scenario == "offline" {
					return fmt.Errorf("owner offline")
				}
				return owner.CleanupLocalInstallation(ctx, req)
			}
			if scenario == "gate" {
				svc.CanWrite = func() error { return fmt.Errorf("standby read only") }
			}
			disk.cloned = scenario == "writer-active"
			_, _ = svc.Cancel(ctx, sess.ID)
			stored, _ := svc.Store.InstallSessions().Get(ctx, sess.ID)
			wantPhase := "cancelling"
			if scenario == "gate" {
				wantPhase = "failed"
			}
			if stored.Phase != wantPhase || stored.Password != sess.Password || disk.workRemoved || ownerDisk.workRemoved {
				t.Fatalf("destructive cleanup in %s: phase=%s", scenario, stored.Phase)
			}
			if (scenario == "gate" || scenario == "writer-active") && calls != 0 {
				t.Fatal("unsafe remote cleanup")
			}
			ownerDisk.cloned = false
			disk.cloned = false
			svc.CanWrite = nil
			svc.CleanupRemote = owner.CleanupLocalInstallation
			v, err := svc.Cancel(ctx, sess.ID)
			if err != nil || v.Phase != "cancelled" {
				t.Fatalf("retry phase=%s err=%v", v.Phase, err)
			}
		})
	}
}

func TestCancelMediaFailureKeepsCredentialsAndRetries(t *testing.T) {
	ctx := context.Background()
	svc, sess, _ := cleanupFixture(t)
	svc.NodeID = "owner"
	root := t.TempDir()
	svc.FilesDir = func(context.Context) (string, error) { return root, nil }
	dir := writeCleanupMedia(t, root, sess)
	logfile := filepath.Join(dir, "logs/console.log")
	if err := os.Remove(logfile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(logfile, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, sess.ID); err == nil {
		t.Fatal("media failure ignored")
	}
	stored, _ := svc.Store.InstallSessions().Get(ctx, sess.ID)
	if stored.Phase != "cancelling" || stored.Password != sess.Password {
		t.Fatal("media failure finalized session")
	}
	if err := os.Remove(logfile); err != nil {
		t.Fatal(err)
	}
	v, err := svc.Cancel(ctx, sess.ID)
	if err != nil || v.Phase != "cancelled" {
		t.Fatalf("retry phase=%s err=%v", v.Phase, err)
	}
}

func TestCancelCannotDeletePublishedImageEvenWithStaleFailedSession(t *testing.T) {
	svc, sess, disk := cleanupFixture(t)
	ctx := context.Background()
	if err := svc.Store.Images().Create(ctx, domain.Image{ID: sess.ImageID, Name: "Published", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal}); err != nil {
		t.Fatal(err)
	}
	svc.CleanupRemote = func(context.Context, storage.InstallationCleanupReq) error {
		t.Fatal("published image remote cleanup")
		return nil
	}
	if _, err := svc.Cancel(ctx, sess.ID); err == nil {
		t.Fatal("published image deletion accepted")
	}
	if disk.workRemoved {
		t.Fatal("published image resources removed")
	}
}

func TestResumeDoesNotRunOtherOwnersInstallationStages(t *testing.T) {
	svc, sess, _ := cleanupFixture(t)
	ctx := context.Background()
	sess.Phase = "preparing"
	if err := svc.Store.InstallSessions().Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc.Prepare = func(context.Context, domain.InstallSession) (MediaFiles, error) {
		t.Fatal("new writer must not prepare another owner's installation")
		return MediaFiles{}, nil
	}
	svc.Resume(ctx)
	stored, _ := svc.Store.InstallSessions().Get(ctx, sess.ID)
	if stored.Phase != "preparing" {
		t.Fatalf("owner restriction lost: %s", stored.Phase)
	}
}
