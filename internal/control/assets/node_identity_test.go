package assets

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/remote"
	"github.com/tianwei/diskless/internal/store"
)

// peerNode 是另一台节点：记下在它上面清掉的身份层和删掉的挪开副本数据。
type peerNode struct {
	macEditStorage
	asideImages     []string
	currentOrigin   string
	releasedSources []string
	releaseErr      error
}

func (p *peerNode) DeleteAsideImage(_ context.Context, imageID string, configIDs []string) error {
	p.asideImages = append(p.asideImages, imageID+":"+strings.Join(configIDs, ","))
	return nil
}

func (p *peerNode) ReleaseIdentityLayers(_ context.Context, sources []string) ([]string, error) {
	p.releasedSources = append(p.releasedSources, sources...)
	if p.releaseErr != nil {
		return nil, p.releaseErr
	}
	if p.currentOrigin != "" {
		matched := false
		for _, source := range sources {
			if p.currentOrigin == source || strings.HasPrefix(p.currentOrigin, source+"@") {
				matched = true
			}
		}
		if !matched {
			return nil, nil
		}
	}
	p.deleted = append(p.deleted, identityMAC)
	return []string{identityMAC}, nil
}

// localWithAside 是本机：在 imageStorageWithIdentity 之外也记下删挪开副本数据的调用。
type localWithAside struct {
	imageStorageWithIdentity
}

func (l localWithAside) DeleteAsideImage(_ context.Context, imageID string, _ []string) error {
	l.events = append(l.events, "清挪开副本 "+imageID)
	return nil
}

func withPeer(t *testing.T, st store.Store, local storage.ClientHostAgent) (*place.Router, *peerNode) {
	t.Helper()
	peer := &peerNode{}
	server := httptest.NewServer(remote.Handler{Agent: peer, Token: "tok"})
	t.Cleanup(server.Close)
	now := time.Now().UTC()
	if err := st.Servers().Create(context.Background(), domain.Server{ID: "node-b", Name: "b", IP: "10.9.0.2", PortalIP: "10.9.0.2", APIURL: server.URL, Role: domain.ServerRoleAll, Status: domain.ServerStatusUp, LastSeenAt: &now}); err != nil {
		t.Fatal(err)
	}
	return &place.Router{Store: st, NodeID: "node-a", Local: local, Token: "tok", Now: func() time.Time { return now }}, peer
}

func layerOn(t *testing.T, st store.Store, terminalID, node, reductionID string) {
	t.Helper()
	if err := st.TerminalIdentities().Create(context.Background(), domain.TerminalIdentity{ID: terminalID, TerminalID: terminalID, LayerServerID: node, LayerReductionRef: reductionID, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

// 客户机的身份层建在别的节点上时，本机池里看不到它；不清掉就钉住那台节点上的副本，复制删不掉，最后整份重建。
// 删完还要让每个节点删掉挪开副本里这个镜像的旧数据。
func TestDeleteImageClearsIdentityLayersAndAsideDataOnEveryNode(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)

	if err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if !slices.Equal(peer.deleted, []string{identityMAC}) {
		t.Fatalf("另一节点上的身份层没清：%v", peer.deleted)
	}
	if !slices.Equal(peer.asideImages, []string{"img-old:cfg-img-old"}) {
		t.Fatalf("另一节点的挪开副本没清：%v", peer.asideImages)
	}
	if want := []string{"删镜像", "清挪开副本 img-old"}; !slices.Equal(local.events, want) {
		t.Fatalf("本机 events = %v, want %v", local.events, want)
	}
}

// 机器还在用（开着或刚关机）时照常拦下并点名，任何节点上都不动。
func TestDeleteImageRefusesWhileAClientWithALayerOnAnotherNodeIsInUse(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOnline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)

	err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "请先关闭这些客户机，关机满 5 分钟后重试：00:50:56:2E:F6:4E") {
		t.Fatalf("err = %v", err)
	}
	if len(peer.deleted) != 0 || len(peer.asideImages) != 0 || len(local.events) != 0 {
		t.Fatalf("拦下时不该动任何东西：peer=%v %v local=%v", peer.deleted, peer.asideImages, local.events)
	}
}

func TestDeleteConfigClearsIdentityLayersOnOtherNodes(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-img-old-2", ImageID: "img-old", Name: "其它", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := configStorageWithIdentity{&fakeConfigStorage{}, &identityRecorder{}}
	router, peer := withPeer(t, st, local)

	if _, err := (ConfigService{Store: st, Storage: local, Place: router}).Delete(ctx, "cfg-img-old"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if !slices.Equal(peer.deleted, []string{identityMAC}) {
		t.Fatalf("另一节点上的身份层没清：%v", peer.deleted)
	}
}

func TestDeleteReductionClearsIdentityLayersOnOtherNodes(t *testing.T) {
	ctx := context.Background()
	st := seedReductionStore(t)
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "windows", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: "red-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: identityMAC, IP: "10.0.0.5", GroupID: "g-1", State: domain.TerminalStateOffline, OfflineAt: ago(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	layerOn(t, st, "term-1", "node-b", "red-2")
	local := reductionStorageWithIdentity{&fakeReductionStorage{}, &identityRecorder{}}
	router, peer := withPeer(t, st, local)

	if _, err := (ReductionService{Store: st, Storage: local, Place: router}).Delete(ctx, "red-2"); err != nil {
		t.Fatalf("delete refused: %v", err)
	}
	if !slices.Equal(peer.deleted, []string{identityMAC}) {
		t.Fatalf("另一节点上的身份层没清：%v", peer.deleted)
	}
}

func TestDeleteImageStillChecksAKnownLayerNodeWithAnOldHeartbeat(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	srv, _ := st.Servers().Get(ctx, "node-b")
	srv.LastSeenAt = ago(time.Hour)
	if err := st.Servers().Update(ctx, srv); err != nil {
		t.Fatal(err)
	}
	if err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(peer.deleted, []string{identityMAC}) {
		t.Fatalf("known layer node was silently skipped: %v", peer.deleted)
	}
}

func TestDeleteImageCannotFallbackAnUnknownLayerNodeToLocal(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "missing-node", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, _ := withPeer(t, st, local)
	err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "missing-node") || len(local.events) > 0 {
		t.Fatalf("unknown node allowed destructive deletion: err=%v events=%v", err, local.events)
	}
}

func TestDeleteImageUsesExpectedSourceRatherThanStaleLayerMAC(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	// The old DB layer reference was read before this node rebuilt the same MAC for another point.
	peer.currentOrigin = "cfg-img-new@1"
	if err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatal(err)
	}
	if len(peer.deleted) > 0 {
		t.Fatalf("new layer deleted via stale MAC-only request: %v", peer.deleted)
	}
	if !slices.Equal(peer.releasedSources, []string{"cfg-img-old@1"}) {
		t.Fatalf("expected snapshot was not sent: %v", peer.releasedSources)
	}
}

func TestMergeConfigAlsoReleasesKnownLayersOnOtherNodes(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := configStorageWithIdentity{&fakeConfigStorage{}, &identityRecorder{}}
	router, peer := withPeer(t, st, local)
	if _, err := (ConfigService{Store: st, Storage: local, Place: router}).Merge(ctx, "cfg-img-old"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(peer.releasedSources, []string{"cfg-img-old@1"}) {
		t.Fatalf("remote layer pinned through config merge: %v", peer.releasedSources)
	}
}

func TestMergeReductionsAlsoReleasesKnownLayersOnOtherNodes(t *testing.T) {
	ctx := context.Background()
	st := seedReductionStore(t)
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "windows", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-1", SystemReductionID: "red-2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: identityMAC, IP: "10.0.0.5", GroupID: "g-1", State: domain.TerminalStateOffline, OfflineAt: ago(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	layerOn(t, st, "term-1", "node-b", "red-1")
	local := reductionStorageWithIdentity{&fakeReductionStorage{}, &identityRecorder{}}
	router, peer := withPeer(t, st, local)
	if _, err := (ReductionService{Store: st, Storage: local, Place: router}).Merge(ctx, "cfg-1", MergeReductionsRequest{KeepReductionID: "red-2"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(peer.releasedSources, []string{"cfg-1@0"}) {
		t.Fatalf("remote layer pinned through snapshot merge: %v", peer.releasedSources)
	}
}

func TestDeleteImageCannotProceedWhenItsRecordedLayerNodeIsUnreachable(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	srv, _ := st.Servers().Get(ctx, "node-b")
	srv.APIURL = "http://127.0.0.1:1"
	if err := st.Servers().Update(ctx, srv); err != nil {
		t.Fatal(err)
	}
	err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "node-b") || len(peer.deleted) > 0 || len(local.events) > 0 {
		t.Fatalf("unreachable owner was ignored: %v %v", err, local.events)
	}
	if _, err := st.Images().Get(ctx, "img-old"); err != nil {
		t.Fatalf("image record deleted before owner release: %v", err)
	}
}

func TestDeleteImageContactsStaleNodesForHistoricalCopies(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	srv, _ := st.Servers().Get(ctx, "node-b")
	srv.LastSeenAt = ago(time.Hour)
	if err := st.Servers().Update(ctx, srv); err != nil {
		t.Fatal(err)
	}
	if err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(peer.asideImages, []string{"img-old:cfg-img-old"}) {
		t.Fatalf("historical copies silently skipped on stale node: %v", peer.asideImages)
	}
}

func TestDeleteImageKeepsMetadataAndCanRetryHistoricalCleanupFailure(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	srv, _ := st.Servers().Get(ctx, "node-b")
	workingURL := srv.APIURL
	srv.APIURL = "http://127.0.0.1:1"
	srv.LastSeenAt = ago(time.Hour)
	if err := st.Servers().Update(ctx, srv); err != nil {
		t.Fatal(err)
	}
	svc := ImageService{Store: st, Storage: local, Place: router}
	err := svc.DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "node-b") {
		t.Fatalf("remote historical cleanup failure ignored: %v", err)
	}
	if _, err := st.Images().Get(ctx, "img-old"); err != nil {
		t.Fatalf("image metadata lost, cannot retry: %v", err)
	}
	if _, err := st.Configs().Get(ctx, "cfg-img-old"); err != nil {
		t.Fatalf("configuration metadata lost, cannot retry: %v", err)
	}
	srv.APIURL = workingURL
	if err := st.Servers().Update(ctx, srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteImage(ctx, "img-old"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(peer.asideImages, []string{"img-old:cfg-img-old"}) {
		t.Fatalf("retry did not clean node: %v", peer.asideImages)
	}
}

func TestDeleteImageRefusesRemoteBusyIdentityEvenWhenDBSaysOffline(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedSwitchedGroup(t, st, domain.TerminalStateOffline)
	layerOn(t, st, "term-1", "node-b", "red-img-old")
	local := localWithAside{imageStorageWithIdentity{&fakeImageStorage{}, &identityRecorder{}}}
	router, peer := withPeer(t, st, local)
	peer.releaseErr = fmt.Errorf("请先关闭客户机 %s：身份层仍在使用配置 cfg-img-old@1", identityMAC)
	err := (ImageService{Store: st, Storage: local, Place: router}).DeleteImage(ctx, "img-old")
	if err == nil || !strings.Contains(err.Error(), "node-b") || !strings.Contains(err.Error(), identityMAC) {
		t.Fatalf("remote busy result ignored: %v", err)
	}
	if len(local.events) > 0 || len(peer.asideImages) > 0 {
		t.Fatalf("mutation before busy owner released: local=%v aside=%v", local.events, peer.asideImages)
	}
	if _, err := st.Images().Get(ctx, "img-old"); err != nil {
		t.Fatalf("image metadata deleted while remote busy: %v", err)
	}
	if !slices.Equal(peer.releasedSources, []string{"cfg-img-old@1"}) {
		t.Fatalf("wrong expected source: %v", peer.releasedSources)
	}
}
