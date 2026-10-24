package platform

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestTaskLogCanBeReadFromOriginNodeWithClusterToken(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/node/tasks/task-model_adapt-1/logs/console.log" || r.Header.Get("Authorization") != "Bearer cluster-secret" {
			t.Errorf("unexpected peer request: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "native PE evidence\r\n")
	}))
	defer peer.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "peer", APIURL: peer.URL, Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	c := ClusterTasks{Store: st, NodeID: "self", Token: "cluster-secret", LocalTaskLog: func(context.Context, string, string) (pe.PEFile, error) { return pe.PEFile{}, store.ErrNotFound }}
	f, err := c.TaskLog(ctx, "task-model_adapt-1", "console.log")
	if err != nil || string(f.Content) != "native PE evidence\r\n" || f.Name != "console.log" {
		t.Fatalf("file=%+v err=%v", f, err)
	}
}

// 本机没有、在线节点也说没有：如实说日志不存在，点名离线没问到的节点，不把离线节点的连接错误当结果。
func TestTaskLogMissingEverywhereSaysSoAndSkipsOfflineNodes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer peer.Close()
	if err := st.Servers().Create(ctx, domain.Server{ID: "peer", IP: "192.0.2.10", APIURL: peer.URL, Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.Servers().Create(ctx, domain.Server{ID: "gone", IP: "192.0.2.1", APIURL: "http://192.0.2.1:8080", Role: domain.ServerRoleAll, Status: domain.ServerStatusDown}); err != nil {
		t.Fatal(err)
	}
	c := ClusterTasks{Store: st, NodeID: "self", Token: "cluster-secret", LocalTaskLog: func(context.Context, string, string) (pe.PEFile, error) {
		return pe.PEFile{}, errs.NotFound("安装日志尚未上传")
	}}
	_, err = c.TaskLog(ctx, "task-install_image-1", "console.log")
	if !errs.IsNotFound(err) || !strings.Contains(err.Error(), "安装日志尚未上传") || !strings.Contains(err.Error(), "gone") || strings.Contains(err.Error(), "dial") {
		t.Fatalf("err = %v", err)
	}
}
