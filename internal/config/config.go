package config

import (
	"flag"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultImportDir = "/var/lib/ndiskless/imports"
const DefaultDriverDir = "/var/lib/ndiskless/drivers"

type Config struct {
	Addr          string
	DBDSN         string
	Pool          string
	ImportDir     string
	DriverDir     string
	DnsmasqConf   string
	BootURL       string
	Workers       int
	MigrateOnly   bool
	JWTSecret     string
	BootstrapUser string
	BootstrapPass string
	// NodeID is the local node's stable database identity from machine-id; servers, pools and clones reference it.
	// Changing the address or moving the portal to VIP does not change database keys.
	NodeID string
	// PortalAddr is the client-facing boot and disk address placed in sanhook. Defaults to the boot URL host; HA uses VIP.
	PortalAddr string
	// Role is the initial process role: "all" by default or "active"/"standby" from the HA role marker.
	Role string
	// PeerURL is the cluster rendezvous API address (VIP or peer in a two-node setup); empty means standalone.
	PeerURL string
	// NodeAddr is the node's own reachable IP, never VIP. If empty, falls back to PortalAddr (the same on standalone hosts).
	NodeAddr string
	// ClusterToken authenticates inter-node replication channels and other cluster APIs.
	ClusterToken string
	// ReplicationInterval controls both active-node snapshots and standby pulls.
	ReplicationInterval time.Duration
	// ReplicationRateMBPerSec limits bulk transfers during first sync and post-promotion rebuilding
	// to protect iSCSI bandwidth on shared uplinks. Zero means unlimited.
	ReplicationRateMBPerSec int
}

func Load(args []string) Config {
	cfg := Config{
		Addr:          env("NDISKLESS_ADDR", ":8080"),
		DBDSN:         env("NDISKLESS_DB_DSN", "file:ndiskless.db"),
		Pool:          env("NDISKLESS_POOL", "tank"),
		ImportDir:     env("NDISKLESS_IMPORT_DIR", DefaultImportDir),
		DriverDir:     env("NDISKLESS_DRIVER_DIR", DefaultDriverDir),
		DnsmasqConf:   env("NDISKLESS_DNSMASQ_CONF", "/etc/dnsmasq.d/ndiskless.conf"),
		BootURL:       env("NDISKLESS_BOOT_URL", ""),
		Workers:       envInt("NDISKLESS_WORKERS", 4),
		JWTSecret:     env("NDISKLESS_JWT_SECRET", ""),
		BootstrapUser: env("NDISKLESS_BOOTSTRAP_USERNAME", "admin"),
		BootstrapPass: env("NDISKLESS_BOOTSTRAP_PASSWORD", ""),
		NodeID:        env("NDISKLESS_NODE_ID", ""),
		PortalAddr:    env("NDISKLESS_PORTAL_ADDR", ""),
		Role:          env("NDISKLESS_ROLE", "all"),
		PeerURL:       env("NDISKLESS_PEER_URL", ""),
		NodeAddr:      env("NDISKLESS_NODE_ADDR", ""),
		ClusterToken:  env("NDISKLESS_CLUSTER_TOKEN", ""),
	}
	cfg.ReplicationInterval = envDuration("NDISKLESS_REPLICATION_INTERVAL", time.Minute)
	cfg.ReplicationRateMBPerSec = envInt("NDISKLESS_REPLICATION_RATE_MBPS", 100)

	fs := flag.NewFlagSet("ndiskless", flag.ExitOnError)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "HTTP listen address")
	fs.StringVar(&cfg.DBDSN, "db-dsn", cfg.DBDSN, "database DSN")
	fs.StringVar(&cfg.Pool, "pool", cfg.Pool, "default ZFS pool name")
	fs.StringVar(&cfg.ImportDir, "import-dir", cfg.ImportDir, "server-side ZFS send import directory")
	fs.StringVar(&cfg.DriverDir, "driver-dir", cfg.DriverDir, "driver pack storage directory")
	fs.StringVar(&cfg.DnsmasqConf, "dnsmasq-conf", cfg.DnsmasqConf, "dnsmasq managed config path")
	fs.StringVar(&cfg.BootURL, "boot-url", cfg.BootURL, "iPXE HTTP chainload URL, e.g. http://<server>:8080/boot?mac=${net0/mac} (empty disables HTTP chainload; hardware-profile params platform/busid/manufacturer/product are appended automatically unless already present)")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "background worker count")
	fs.StringVar(&cfg.JWTSecret, "jwt-secret", cfg.JWTSecret, "JWT signing secret")
	fs.StringVar(&cfg.BootstrapUser, "bootstrap-user", cfg.BootstrapUser, "initial admin username")
	fs.StringVar(&cfg.BootstrapPass, "bootstrap-password", cfg.BootstrapPass, "initial admin password")
	fs.BoolVar(&cfg.MigrateOnly, "migrate-only", false, "run database migrations and exit")
	fs.StringVar(&cfg.NodeID, "node-id", cfg.NodeID, "stable identity of this node in the DB (default: derived from /etc/machine-id)")
	fs.StringVar(&cfg.PortalAddr, "portal-addr", cfg.PortalAddr, "address clients reach this node's disks at (default: the boot URL's host)")
	fs.StringVar(&cfg.Role, "role", cfg.Role, "process role: all")
	_ = fs.Parse(args)

	if cfg.PortalAddr == "" {
		cfg.PortalAddr = hostOfURL(cfg.BootURL)
	}
	if cfg.NodeID == "" {
		cfg.NodeID = machineNodeID()
	}
	return cfg
}

// hostOfURL extracts the hostname from a URL, returning an empty string on parse failure.
func hostOfURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// machineNodeID derives a stable ID from the first 12 hex characters of /etc/machine-id, falling back to hostname.
// Servers, pools and clones use that key, so its stability is essential.
func machineNodeID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(b)); len(id) >= 12 {
			return id[:12]
		} else if id != "" {
			return id
		}
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "local"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
