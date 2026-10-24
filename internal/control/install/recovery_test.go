package install

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestInstallationReadRetainsRecreationSettingsWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{
		ID: "failed-install", Name: "Windows 11", MAC: "001122334455", IP: "192.168.50.10", Phase: "failed",
		SourcePath: "/tank/imports/windows.iso", SourceSHA256: "iso-hash", EditionIndex: 3, PoolID: "pool-1", SizeBytes: 96 << 30,
		Netmask: "255.255.255.0", Gateway: "192.168.50.1", DNS1: "223.5.5.5", Username: "tester",
		Password: "PrivatePassword!", Token: "private-token",
	}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: st}
	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.List(ctx)
	if err != nil || listed.Total != 1 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	for _, response := range []View{got, listed.Items[0]} {
		b, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string]any{
			"name": "Windows 11", "mac": "001122334455", "ip": "192.168.50.10",
			"source_path": "/tank/imports/windows.iso", "source_sha256": "iso-hash", "edition_index": float64(3),
			"pool_id": "pool-1", "size_gib": float64(96), "netmask": "255.255.255.0", "gateway": "192.168.50.1", "dns1": "223.5.5.5", "username": "tester",
		} {
			if fields[field] != want {
				t.Errorf("recreation setting %s = %v, want %v", field, fields[field], want)
			}
		}
		if _, ok := fields["password"]; ok {
			t.Error("installation password exposed")
		}
		if _, ok := fields["token"]; ok {
			t.Error("installation callback token exposed")
		}
		if strings.Contains(string(b), "PrivatePassword!") || strings.Contains(string(b), "private-token") {
			t.Error("installation secrets exposed")
		}
	}
}

func TestDuplicateInstallationPointsToExistingInstallationAndPreservesFailedWork(t *testing.T) {
	for _, tc := range []struct {
		name, phase, mac, imageName, state string
	}{
		{"failed MAC", "failed", "001122334455", "New Windows", "失败"},
		{"failed name", "failed", "AABBCCDDEEFF", "Windows 11", "失败"},
		{"running MAC", "installing", "001122334455", "New Windows", "进行中"},
		{"running name", "installing", "AABBCCDDEEFF", "Windows 11", "进行中"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			sess := domain.InstallSession{ID: "existing-install", Name: "Windows 11", MAC: "001122334455", Phase: tc.phase, Token: "existing-token", Password: "ExistingPassword!"}
			if err := st.InstallSessions().Create(ctx, sess); err != nil {
				t.Fatal(err)
			}
			disk := &diskFake{}
			svc := Service{Store: st, Host: disk, Storage: disk, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
				t.Fatal("duplicate creation must not prepare another installation")
				return MediaFiles{}, nil
			}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "", nil }}
			_, err = svc.Start(ctx, StartRequest{Name: tc.imageName, MAC: tc.mac})
			if !errors.Is(err, errs.ErrConflict) {
				t.Fatalf("duplicate installation error = %v", err)
			}
			for _, part := range []string{tc.state, "Windows 11", "001122334455", "「镜像」页", "安装卡片", "已有安装"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("duplicate error %q does not identify %q", err, part)
				}
			}
			if tc.phase == "failed" && (!strings.Contains(err.Error(), "资源仍保留") || strings.Contains(err.Error(), "进行中")) {
				t.Errorf("failed installation must explain its retained resources: %v", err)
			}
			stored, err := st.InstallSessions().Get(ctx, sess.ID)
			if err != nil || stored.Phase != tc.phase || stored.Token != sess.Token || stored.Password != sess.Password {
				t.Fatalf("duplicate creation modified the existing installation: %+v, %v", stored, err)
			}
			if disk.workRemoved || disk.deletedImage != "" {
				t.Fatal("duplicate creation must retain the existing work disk")
			}
		})
	}
}
