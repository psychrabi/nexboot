package assets

import (
	"context"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

func TestDHCPIncludesInstallationWithoutBusinessGroup(t *testing.T) {
	st := newImageTestStore(t)
	ctx := context.Background()
	sess := domain.InstallSession{ID: "install-temp", MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Token: "secret", Phase: "waiting"}
	if err := st.InstallSessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	groups, err := buildDHCPConfigs(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range groups {
		for _, term := range g.Terminals {
			if term.MAC == sess.MAC && term.IP == sess.IP {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("unregistered installation client must have a DHCP reservation")
	}
	sess.Phase = "cancelled"
	if err := st.InstallSessions().Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	groups, err = buildDHCPConfigs(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		for _, term := range g.Terminals {
			if term.MAC == sess.MAC {
				t.Fatal("cancelled installation must release temporary reservation")
			}
		}
	}
}
