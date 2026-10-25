package config

import (
	"os"
	"strings"
	"testing"
)

// Node identity and portal address are distinct: stable ID keys database records while portal is the client-facing disk address (VIP for HA).
// Without overrides, portal defaults to boot URL hostname and ID to machine-id.
func TestLoadSplitsNodeIdentityFromPortal(t *testing.T) {
	for _, key := range []string{"NDISKLESS_NODE_ID", "NDISKLESS_PORTAL_ADDR", "NDISKLESS_ROLE", "NDISKLESS_BOOT_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("NDISKLESS_BOOT_URL", "http://192.168.50.1:8080/boot?mac=${net0/mac}")

	cfg := Load(nil)
	if cfg.PortalAddr != "192.168.50.1" {
		t.Fatalf("portal = %q, want the boot URL host", cfg.PortalAddr)
	}
	if cfg.NodeID == "" || cfg.NodeID == "192.168.50.1" {
		t.Fatalf("node id = %q, want a machine-derived identity", cfg.NodeID)
	}
	if cfg.Role != "all" {
		t.Fatalf("role = %q", cfg.Role)
	}

	t.Setenv("NDISKLESS_NODE_ID", "node-a")
	t.Setenv("NDISKLESS_PORTAL_ADDR", "192.168.50.250")
	t.Setenv("NDISKLESS_ROLE", "all")
	cfg = Load(nil)
	if cfg.NodeID != "node-a" || cfg.PortalAddr != "192.168.50.250" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// machineNodeID must remain stable and fall back to hostname if /etc/machine-id is missing.
func TestMachineNodeIDStableAndNonEmpty(t *testing.T) {
	a, b := machineNodeID(), machineNodeID()
	if a == "" || a != b {
		t.Fatalf("ids = %q, %q", a, b)
	}
	if strings.ContainsAny(a, " \t\n") {
		t.Fatalf("id has whitespace: %q", a)
	}
	if host, _ := os.Hostname(); host == "" && a == "" {
		t.Fatal("unreachable")
	}
}
