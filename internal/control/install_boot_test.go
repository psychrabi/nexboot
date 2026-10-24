package control

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

type installBootFake struct{}

func (installBootFake) BootScript(context.Context, string) (string, bool, error) {
	return "#!ipxe\necho ISO installation\n", true, nil
}
func TestInstallationBootAndNetworkDoNotRequireTerminalOrGroup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sess := domain.InstallSession{ID: "install-empty", MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", DNS1: "223.5.5.5", Token: "token", Phase: "waiting"}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc := BootService{Store: st, Install: installBootFake{}}
	if script, err := svc.BuildBootScript(ctx, "00:11:22:33:44:55"); err != nil || script != "#!ipxe\necho ISO installation\n" {
		t.Fatalf("empty environment boot = %q %v", script, err)
	}
	if ip, err := svc.RegisteredIP(ctx, sess.MAC); err != nil || ip != sess.IP {
		t.Fatalf("installation assigned ip = %q %v", ip, err)
	}
	if cfg, err := svc.NetConfig(ctx, sess.MAC); err != nil || len(cfg.DNS) != 1 || cfg.DNS[0] != "223.5.5.5" {
		t.Fatalf("installation network = %+v %v", cfg, err)
	}
	if cfg, err := svc.NetConfig(ctx, sess.MAC); err != nil || !cfg.WindowsPolicy {
		t.Fatalf("new installations enforce Windows policy by default: %+v %v", cfg, err)
	}
	if err := st.SystemSettings().Upsert(ctx, domain.SystemSettings{ID: domain.SystemSettingsDefaultID, KeepWindowsDefaults: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := svc.NetConfig(ctx, sess.MAC); err != nil || cfg.WindowsPolicy {
		t.Fatalf("installation verification must respect disabled Windows policy: %+v %v", cfg, err)
	}
	if disks, err := svc.DataDiskLetters(ctx, sess.MAC); err != nil || len(disks) != 0 {
		t.Fatalf("installation must not attach business data disks: %+v %v", disks, err)
	}
}
