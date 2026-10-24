package ops

import (
	"context"
	"testing"

	"github.com/tianwei/diskless/internal/ha"
	"github.com/tianwei/diskless/internal/storage/local"
	"github.com/tianwei/diskless/internal/storage/zfs"
)

// Use the actual local release validator, not a recorder which accepts any source.
type markerIdentityPool struct {
	*zfs.Client
	origins     map[string]string
	originReads int
}

func (p *markerIdentityPool) ListOrigins(context.Context) (map[string]string, error) {
	p.originReads++
	return p.origins, nil
}

type markerSessions struct {
	local.Exporter
	active []string
}

func (s markerSessions) ActiveSessions(context.Context) ([]string, error) { return s.active, nil }

func TestPullOnceDoesNotReleaseIdentityFromCatalogueRootMarkers(t *testing.T) {
	for _, which := range []string{"root-only", "root-and-child", "busy-child"} {
		t.Run(which, func(t *testing.T) {
			ctx := context.Background()
			gate := ha.NewOpenGate()
			gate.Close("备机", "http://a:8080")
			entries := []zfs.GUIDEntry{
				{Name: "tank/nd/win11", GUID: "volume"},
				{Name: "tank/nd@rep-1", GUID: "common"},
				{Name: "tank/nd@rep-9", GUID: "local-round"},
			}
			if which != "root-only" {
				entries = append(entries, zfs.GUIDEntry{Name: "tank/nd/win11@rep-9", GUID: "child-round"})
			}
			z := &fakeReplZFS{guids: map[string]zfs.GUIDInventory{"tank/nd": {Entries: entries}}}
			peer := &fakeReplPeer{inv: ha.ReplicationInventory{Root: "tank/nd", Entries: []zfs.GUIDEntry{
				{Name: "tank/nd/win11", GUID: "volume"}, {Name: "tank/nd@rep-1", GUID: "common"}, {Name: "tank/nd@rep-2", GUID: "new-round"},
			}}}
			pool := &markerIdentityPool{Client: zfs.New("tank", nil, nil)}
			sessions := markerSessions{}
			if which == "busy-child" {
				pool.origins = map[string]string{"tank/run/IDENT-001122334455": "tank/nd/win11@rep-9"}
				sessions.active = []string{"001122334455"}
			}
			r := &Replicator{Store: newImageTestStore(t), ZFS: z, Root: "tank/nd", Gate: gate, Now: replTestClock(), Peer: peer, PeerURL: "http://a:8080", IdentityLayers: local.New("node", pool, sessions)}
			err := r.PullOnce(ctx)
			if which == "busy-child" {
				if err == nil || len(z.received) > 0 {
					t.Fatalf("busy child must block receive: err=%v received=%v", err, z.received)
				}
				if pool.originReads < 2 {
					t.Fatalf("child did not reach real local origin recheck: reads=%d err=%v", pool.originReads, err)
				}
			} else {
				if err != nil || len(z.received) != 1 {
					t.Fatalf("root marker must not block replication: err=%v received=%v", err, z.received)
				}
				if which == "root-and-child" && pool.originReads == 0 {
					t.Fatal("child snapshot was also filtered instead of reaching local validation")
				}
				if which == "root-only" && pool.originReads != 0 {
					t.Fatal("root-only marker must not invoke identity cleanup")
				}
			}
		})
	}
}
