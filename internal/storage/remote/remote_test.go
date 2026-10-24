package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

func TestCleanupInstallationRejectsWrongNodeBeforeCleanup(t *testing.T) {
	req := storage.InstallationCleanupReq{NodeID: "other", ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "001122334455"}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/internal/storage/cleanup-installation-resources", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	Handler{Token: "tok"}.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong target status=%d, want 400", w.Code)
	}
}

func TestCleanupInstallationResourcesContract(t *testing.T) {
	req := storage.InstallationCleanupReq{NodeID: "owner", ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "001122334455", Password: "private-password", Token: "private-token"}
	calls := 0
	var got storage.InstallationCleanupReq
	var cleanupError error
	srv := httptest.NewServer(Handler{Token: "cluster", NodeID: "owner", CleanupInstallation: func(_ context.Context, r storage.InstallationCleanupReq) error { calls++; got = r; return cleanupError }})
	defer srv.Close()
	client := Client{BaseURL: srv.URL, Token: "cluster"}
	if err := client.CleanupInstallationResources(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(got, req) {
		t.Fatalf("cleanup request lost fields: %+v", got)
	}
	cleanupError = errors.New("请先关闭安装客户机")
	if err := client.CleanupInstallationResources(context.Background(), req); err == nil || err.Error() != cleanupError.Error() {
		t.Fatalf("cleanup error lost: %v", err)
	}
	for _, bad := range []string{"auth", "node", "id", "image", "mac", "media-path"} {
		t.Run(bad, func(t *testing.T) {
			badReq := req
			token := "cluster"
			switch bad {
			case "auth":
				token = "wrong"
			case "node":
				badReq.NodeID = "other"
			case "id":
				badReq.ID = "../outside"
			case "image":
				badReq.ImageID = "published-image"
			case "mac":
				badReq.MAC = "../outside"
			}
			body, _ := json.Marshal(badReq)
			if bad == "media-path" {
				body = append(body[:len(body)-1], []byte(`,"MediaDir":"/etc"}`)...)
			}
			r := httptest.NewRequest(http.MethodPost, "/internal/storage/cleanup-installation-resources", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			before := calls
			Handler{Token: "cluster", NodeID: "owner", CleanupInstallation: func(context.Context, storage.InstallationCleanupReq) error { calls++; return nil }}.ServeHTTP(w, r)
			want := http.StatusBadRequest
			if bad == "auth" {
				want = http.StatusUnauthorized
			}
			if w.Code != want || calls != before {
				t.Fatalf("unsafe cleanup: status=%d calls=%d", w.Code, calls-before)
			}
		})
	}
}

// fakeHost 记录收到的请求并返回预设结果；契约测试让每个 RPC 走一遍 Handler+Client，
// 断言参数、结果和错误类型都原样传递。
type fakeHost struct {
	gotClient    storage.ClientReq
	gotCleanup   []string
	gotReclaim   string
	gotDecidedAt time.Time
	gotStop      storage.SuperStopReq
	failStop     error
}

func (f *fakeHost) CreateClientLUN(_ context.Context, req storage.ClientReq) (storage.LUNInfo, error) {
	f.gotClient = req
	return storage.LUNInfo{Server: "10.0.0.3", Target: "iqn.t", System: storage.LUN{LUN: 0, Target: "iqn.t", VolPath: "/dev/zvol/x"}}, nil
}
func (f *fakeHost) CleanupClientClones(_ context.Context, pool string, macs []string) error {
	f.gotCleanup = append([]string{pool}, macs...)
	return nil
}
func (f *fakeHost) ReclaimIdleClientClones(_ context.Context, mac string, decidedAt time.Time) (bool, error) {
	f.gotReclaim, f.gotDecidedAt = mac, decidedAt
	return true, nil
}
func (f *fakeHost) ActiveClientMACs(context.Context) ([]string, error) {
	return []string{"aa:bb:cc:dd:ee:01"}, nil
}
func (f *fakeHost) ClientCloneMACs(context.Context) ([]string, error) {
	return []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"}, nil
}
func (f *fakeHost) SuperStart(_ context.Context, req storage.ClientReq) (storage.LUNInfo, error) {
	f.gotClient = req
	return storage.LUNInfo{Server: "10.0.0.3", Target: "iqn.s"}, nil
}
func (f *fakeHost) SuperStop(_ context.Context, req storage.SuperStopReq) (storage.SuperStopResult, error) {
	f.gotStop = req
	if f.failStop != nil {
		return storage.SuperStopResult{}, f.failStop
	}
	return storage.SuperStopResult{System: domain.Reduction{ID: "red-1", Name: "r"}}, nil
}

func (f *fakeHost) SpaceUsage(context.Context) (storage.SpaceUsage, error) {
	return storage.SpaceUsage{}, nil
}

func newPair(t *testing.T) (*fakeHost, Client) {
	t.Helper()
	host := &fakeHost{}
	srv := httptest.NewServer(Handler{Agent: host, Token: "tok"})
	t.Cleanup(srv.Close)
	return host, Client{BaseURL: srv.URL, Token: "tok"}
}

func TestRoundTrip(t *testing.T) {
	host, client := newPair(t)
	ctx := context.Background()

	req := storage.ClientReq{MAC: "aa:bb", IP: "1.2.3.4", GroupID: "g1",
		System:    storage.ClientSource{ImageID: "img", ConfigID: "cfg", SnapshotName: "final"},
		DataDisks: []storage.ClientSource{{ImageID: "d1", ConfigID: "d1c"}}, MountScript: []byte("ps1")}
	info, err := client.CreateClientLUN(ctx, req)
	if err != nil || info.Server != "10.0.0.3" || info.System.VolPath != "/dev/zvol/x" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if !reflect.DeepEqual(host.gotClient, req) {
		t.Fatalf("req mangled: %+v", host.gotClient)
	}

	if err := client.CleanupClientClones(ctx, "tank", []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host.gotCleanup, []string{"tank", "m1", "m2"}) {
		t.Fatalf("cleanup args: %v", host.gotCleanup)
	}

	// 传的是「决定过去多久」，对端按自己的时钟换算：两台机器的时钟不必一致。
	decided := time.Now().Add(-time.Minute)
	if done, err := client.ReclaimIdleClientClones(ctx, "m1", decided); err != nil || !done {
		t.Fatalf("reclaim done=%v err=%v", done, err)
	}
	if host.gotReclaim != "m1" || host.gotDecidedAt.Sub(decided).Abs() > 5*time.Second {
		t.Fatalf("reclaim args: %q %v, want m1 %v", host.gotReclaim, host.gotDecidedAt, decided)
	}

	macs, err := client.ActiveClientMACs(ctx)
	if err != nil || len(macs) != 1 {
		t.Fatalf("macs=%v err=%v", macs, err)
	}
	clones, err := client.ClientCloneMACs(ctx)
	if err != nil || len(clones) != 2 {
		t.Fatalf("clones=%v err=%v", clones, err)
	}

}

// 调用方据以分支的类型化错误必须跨网络原样还原。
func TestSuperSessionMissingSurvivesWire(t *testing.T) {
	host, client := newPair(t)
	host.failStop = storage.SuperSessionMissing{LUN: 2}
	_, err := client.SuperStop(context.Background(), storage.SuperStopReq{MAC: "aa:bb", ConfigID: "c"})
	var missing storage.SuperSessionMissing
	if !errors.As(err, &missing) || missing.LUN != 2 {
		t.Fatalf("err=%v", err)
	}
	if host.gotStop.ConfigID != "c" {
		t.Fatalf("stop req mangled: %+v", host.gotStop)
	}
}

func TestSuperHiveUnsafeSurvivesWire(t *testing.T) {
	for _, want := range []storage.SuperHiveUnsafe{{FastStartup: true}, {Reason: "没有可用于恢复的注册表事务日志"}} {
		host, client := newPair(t)
		host.failStop = want
		_, err := client.SuperStop(context.Background(), storage.SuperStopReq{MAC: "aa:bb", ConfigID: "c"})
		var got storage.SuperHiveUnsafe
		if !errors.As(err, &got) || got != want {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestTokenRequired(t *testing.T) {
	_, client := newPair(t)
	client.Token = "wrong"
	if _, err := client.ActiveClientMACs(context.Background()); err == nil {
		t.Fatal("want auth error")
	}
}

// 编译期检查：本地代理和远程客户端都实现放置接缝。
var _ storage.ClientHostAgent = Client{}

type asideHost struct {
	fakeHost
	gotImage   string
	gotConfigs []string
}

func (f *asideHost) DeleteAsideImage(_ context.Context, imageID string, configIDs []string) error {
	f.gotImage, f.gotConfigs = imageID, configIDs
	return nil
}

// 删镜像时主机要让每个节点删掉挪开副本里这个镜像的数据，参数原样到达对端。
func TestDeleteAsideImageRoundTrip(t *testing.T) {
	host := &asideHost{}
	srv := httptest.NewServer(Handler{Agent: host, Token: "tok"})
	t.Cleanup(srv.Close)
	client := Client{BaseURL: srv.URL, Token: "tok"}
	if err := client.DeleteAsideImage(context.Background(), "win10", []string{"win10_default", "win10_lab"}); err != nil {
		t.Fatal(err)
	}
	if host.gotImage != "win10" || !reflect.DeepEqual(host.gotConfigs, []string{"win10_default", "win10_lab"}) {
		t.Fatalf("args: %q %v", host.gotImage, host.gotConfigs)
	}
	_, plain := newPair(t)
	if err := plain.DeleteAsideImage(context.Background(), "win10", nil); err == nil {
		t.Fatal("不支持的节点应报错而不是假装删了")
	}
}

type expectedLayersAgent struct {
	storage.ClientHostAgent
	gone []string
}

func (a *expectedLayersAgent) ReleaseIdentityLayers(_ context.Context, gone []string) ([]string, error) {
	a.gone = append([]string(nil), gone...)
	return []string{"001122334455"}, nil
}
func TestReleaseIdentityLayersTransportsExpectedSources(t *testing.T) {
	agent := &expectedLayersAgent{}
	srv := httptest.NewServer(Handler{Agent: agent, Token: "cluster"})
	defer srv.Close()
	client := Client{BaseURL: srv.URL, Token: "cluster"}
	released, err := client.ReleaseIdentityLayers(context.Background(), []string{"cfg@old"})
	if err != nil || !reflect.DeepEqual(released, []string{"001122334455"}) || !reflect.DeepEqual(agent.gone, []string{"cfg@old"}) {
		t.Fatalf("expected source lost across RPC: %v %v %v", released, agent.gone, err)
	}
}
