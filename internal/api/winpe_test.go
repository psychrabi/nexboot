package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/ha"
	"github.com/tianwei/diskless/internal/store"
)

func TestPEIdentityResultDoesNotConfirmFailedServerSave(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "identity-result.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "image", Name: "Windows", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "config", ImageID: "image", Name: "default", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red", ConfigID: "config", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g", Name: "test", StartIP: "192.168.10.10", ClientMax: 10, Netmask: "255.255.255.0", SystemImageID: "image", SystemConfigID: "config", SystemReductionID: "red"}); err != nil {
		t.Fatal(err)
	}
	terminal := domain.Terminal{ID: "client", MAC: "0050562EF650", IP: "192.168.10.11", GroupID: "g", State: domain.TerminalStateOffline}
	if err := st.Terminals().Create(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "identity-task", Type: domain.TaskTypeIdentityCapture, Status: domain.TaskStatusRunning, CreatedAt: now}
	if err := st.Tasks().Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	sess := domain.PESession{ID: "identity-session", TerminalID: terminal.ID, MAC: terminal.MAC, TaskID: task.ID, Token: "identity-result-token", Mode: domain.PEModeIdentity, State: domain.PEStateBooted, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.PESessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	svc := pe.SessionService{Store: st, OnDone: func(context.Context, domain.PESession, pe.PEResult) error {
		return errors.New("保存网卡身份失败")
	}}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{PE: svc})
	req := httptest.NewRequest(http.MethodPost, "/boot/pe/"+sess.Token+"/result", strings.NewReader(`{"ok":true,"stage":"identity","message":"local capture succeeded"}`))
	req.RemoteAddr = terminal.IP + ":1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed server save must prevent the client treating capture as complete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := st.PESessions().Get(ctx, sess.ID)
	if err != nil || stored.State != domain.PEStateFailed {
		t.Fatalf("session=%+v err=%v", stored, err)
	}
	storedTask, err := st.Tasks().Get(ctx, task.ID)
	if err != nil || storedTask.Status != domain.TaskStatusFailed || !strings.Contains(storedTask.Error, "保存网卡身份失败") {
		t.Fatalf("task=%+v err=%v", storedTask, err)
	}
}

func TestClosedWriterGateRejectsPEBootFailureButServesFiles(t *testing.T) {
	service := &fakePEService{}
	gate := ha.NewOpenGate()
	gate.Close("本节点为备机", "http://192.168.10.3:8080")
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{PE: service, Gate: gate})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boot/pe/tok/failed?err=1058021379", nil))
	if rec.Code != http.StatusServiceUnavailable || service.result != nil {
		t.Fatalf("standby must not fail or clean PE: status=%d result=%+v", rec.Code, service.result)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boot/pe/tok/ndpe.ini", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("standby must still serve PE files: status=%d", rec.Code)
	}
}

func TestPEBootFailureClosesSessionAndCleansAdaptationImmediately(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "boot-failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "win11", Name: "Windows", OSType: domain.OSTypeWindows, Purpose: domain.ImagePurposeSystem, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "win11_default", ImageID: "win11", Name: "default", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "win11_0", ConfigID: "win11_default", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g1", Name: "测试分组", StartIP: "192.168.10.40", ClientMax: 10, SystemImageID: "win11", SystemConfigID: "win11_default", SystemReductionID: "win11_0"}); err != nil {
		t.Fatal(err)
	}
	terminal := domain.Terminal{ID: "t1", MAC: "0050562EF650", IP: "192.168.10.42", GroupID: "g1", State: domain.TerminalStateOffline}
	if err := st.Terminals().Create(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-adapt", Type: domain.TaskTypeModelAdapt, Status: domain.TaskStatusRunning, CreatedAt: now}
	if err := st.Tasks().Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	sess := domain.PESession{ID: "pe-adapt", TaskID: task.ID, TerminalID: terminal.ID, MAC: terminal.MAC, Token: "boot-failed-token", Mode: domain.PEModeAdapt, State: domain.PEStatePending, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := st.PESessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	cleaned := 0
	svc := pe.SessionService{Store: st, OnFailure: func(_ context.Context, session domain.PESession) error {
		if session.Mode != domain.PEModeAdapt || session.ID != sess.ID {
			t.Fatalf("wrong adaptation cleanup: %+v", session)
		}
		cleaned++
		return nil
	}}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{PE: svc})
	report := func(from string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/boot/pe/"+sess.Token+"/failed?err=1058021379&platform=efi", nil)
		req.RemoteAddr = from + ":4567"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := report("192.168.10.99"); rec.Code != http.StatusNotFound || cleaned != 0 {
		t.Fatalf("another client's report: status=%d cleaned=%d", rec.Code, cleaned)
	}
	if rec := report(terminal.IP); rec.Code != http.StatusOK || cleaned != 1 {
		t.Fatalf("boot failure must clean immediately: status=%d cleaned=%d body=%s", rec.Code, cleaned, rec.Body.String())
	}
	sess, _ = st.PESessions().Get(ctx, sess.ID)
	task, _ = st.Tasks().Get(ctx, task.ID)
	if sess.State != domain.PEStateFailed || task.Status != domain.TaskStatusFailed || !strings.Contains(task.Error, "1058021379") {
		t.Fatalf("failure must retain iPXE errno: session=%s task=%+v", sess.State, task)
	}
	if rec := report(terminal.IP); rec.Code != http.StatusNotFound || cleaned != 1 {
		t.Fatalf("completed token must not clean twice: status=%d cleaned=%d", rec.Code, cleaned)
	}
}

type fakeWinPEService struct {
	bcd       []byte
	adk       []byte
	extracted []string
	deleted   []string
}

func (f *fakeWinPEService) List(context.Context) (pe.EnvView, error) {
	return pe.EnvView{Images: []pe.ImageWinRE{{ImageID: "win11", Name: "Win11"}}}, nil
}
func (f *fakeWinPEService) ExtractForImage(_ context.Context, id string) (pe.ExtractResult, error) {
	f.extracted = append(f.extracted, id)
	return pe.ExtractResult{TaskID: "task-winpe_extract-1"}, nil
}
func (f *fakeWinPEService) UploadBCD(_ context.Context, data []byte) (domain.WinPEEnv, error) {
	f.bcd = data
	return domain.WinPEEnv{ID: "winpe-bcd", Kind: domain.WinPEEnvBCD}, nil
}
func (f *fakeWinPEService) UploadADK(_ context.Context, r io.Reader) (domain.WinPEEnv, error) {
	data, err := io.ReadAll(r)
	f.adk = data
	return domain.WinPEEnv{ID: "winpe-adk-1", Kind: domain.WinPEEnvADK, Build: 22621}, err
}
func (f *fakeWinPEService) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func multipartFile(t *testing.T, field, name string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = w.Close()
	return &body, w.FormDataContentType()
}

func TestWinPEEndpoints(t *testing.T) {
	svc := &fakeWinPEService{}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.FS(nil), Services{WinPE: svc})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/winpe", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"image_id":"win11"`) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	body, ctype := multipartFile(t, "file", "BCD", []byte("regf...."))
	req := httptest.NewRequest(http.MethodPost, "/api/winpe/bcd", body)
	req.Header.Set("Content-Type", ctype)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || string(svc.bcd) != "regf...." {
		t.Fatalf("bcd status=%d body=%s got=%q", rec.Code, rec.Body.String(), svc.bcd)
	}

	body, ctype = multipartFile(t, "file", "ndwinpe.zip", bytes.Repeat([]byte("z"), 1000))
	req = httptest.NewRequest(http.MethodPost, "/api/winpe/adk", body)
	req.Header.Set("Content-Type", ctype)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || len(svc.adk) != 1000 {
		t.Fatalf("adk status=%d body=%s got=%d", rec.Code, rec.Body.String(), len(svc.adk))
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/images/win11/winpe", nil))
	if rec.Code != http.StatusAccepted || len(svc.extracted) != 1 || svc.extracted[0] != "win11" {
		t.Fatalf("extract status=%d body=%s extracted=%v", rec.Code, rec.Body.String(), svc.extracted)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/winpe/winpe-adk-1", nil))
	if rec.Code != http.StatusNoContent || len(svc.deleted) != 1 {
		t.Fatalf("delete status=%d deleted=%v", rec.Code, svc.deleted)
	}

	// 没有文件字段的上传要说清楚缺什么。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/winpe/bcd", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad upload status=%d", rec.Code)
	}
}

type fakePEService struct {
	terminalIP string
	booted     int
	result     *pe.PEResult
	logs       map[string]string
	probes     []string
}

func (f *fakePEService) StartProbe(_ context.Context, id string, _ pe.ProbeRequest) (pe.StartResult, error) {
	f.probes = append(f.probes, id)
	return pe.StartResult{TaskID: "task-pe_probe-1", SessionID: "pe-1"}, nil
}
func (f *fakePEService) SessionByToken(_ context.Context, token string) (domain.PESession, domain.Terminal, error) {
	if token != "tok" {
		return domain.PESession{}, domain.Terminal{}, io.ErrUnexpectedEOF
	}
	return domain.PESession{ID: "pe-1", Token: token}, domain.Terminal{ID: "t1", IP: f.terminalIP}, nil
}
func (f *fakePEService) File(_ context.Context, _ string, name, platform string) (pe.PEFile, error) {
	if name == "ndpe.ini" {
		return pe.PEFile{Name: name, Content: []byte("mode=probe\r\n")}, nil
	}
	if name == "BCD" {
		return pe.PEFile{Name: name, Content: []byte("bcd-for-" + platform)}, nil
	}
	return pe.PEFile{}, io.ErrUnexpectedEOF
}
func (f *fakePEService) MarkBooted(context.Context, string) error { f.booted++; return nil }
func (f *fakePEService) TaskLog(context.Context, string, string) (pe.PEFile, error) {
	return pe.PEFile{}, store.ErrNotFound
}
func (f *fakePEService) Complete(_ context.Context, _ string, r pe.PEResult) error {
	f.result = &r
	return nil
}
func (f *fakePEService) SaveLog(_ context.Context, _ string, name string, r io.Reader) error {
	data, _ := io.ReadAll(r)
	if f.logs == nil {
		f.logs = map[string]string{}
	}
	f.logs[name] = string(data)
	return nil
}

func TestPESessionEndpointsCheckTokenAndSource(t *testing.T) {
	svc := &fakePEService{terminalIP: "192.168.10.40"}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.FS(nil), Services{PE: svc})
	do := func(method, target, from string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, body)
		req.RemoteAddr = from + ":4567"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, "/boot/pe/tok/ndpe.ini", "192.168.10.40", nil); rec.Code != http.StatusOK || rec.Body.String() != "mode=probe\r\n" {
		t.Fatalf("file status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/boot/pe/tok/BCD?platform=pcbios", "192.168.10.40", nil); rec.Code != http.StatusOK || rec.Body.String() != "bcd-for-pcbios" {
		t.Fatalf("BCD by platform status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/boot/pe/tok/ndpe.ini", "192.168.10.99", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("别的地址来取文件应 404，得到 %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/boot/pe/bad/ndpe.ini", "192.168.10.40", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("错令牌应 404，得到 %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/boot/pe/tok/booted", "192.168.10.40", nil); rec.Code != http.StatusNoContent || svc.booted != 1 {
		t.Fatalf("booted status=%d booted=%d", rec.Code, svc.booted)
	}
	if rec := do(http.MethodPost, "/boot/pe/tok/result", "192.168.10.40", strings.NewReader(`{"ok":true,"stage":"probe","message":"fine"}`)); rec.Code != http.StatusNoContent || svc.result == nil || !svc.result.OK || svc.result.Message != "fine" {
		t.Fatalf("result status=%d result=%+v", rec.Code, svc.result)
	}
	if rec := do(http.MethodPost, "/boot/pe/tok/result", "192.168.10.40", strings.NewReader("not json")); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，得到 %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/boot/pe/tok/log/console.log", "192.168.10.40", strings.NewReader("line1")); rec.Code != http.StatusNoContent || svc.logs["console.log"] != "line1" {
		t.Fatalf("log status=%d logs=%v", rec.Code, svc.logs)
	}
	rec := do(http.MethodPost, "/api/terminals/t1/pe-probe", "10.0.0.1", strings.NewReader(`{}`))
	if rec.Code != http.StatusAccepted || len(svc.probes) != 1 || svc.probes[0] != "t1" {
		t.Fatalf("probe status=%d probes=%v body=%s", rec.Code, svc.probes, rec.Body.String())
	}
}

func TestCompletedPETaskLogRequiresLoginAndKeepsOriginalBytes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	sess := domain.PESession{ID: "pe-done", TaskID: "task-model_adapt-done", State: domain.PEStateDone}
	if err := st.PESessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "pe-sessions", sess.ID, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte{'O', 'K', '\r', '\n', 0xd6, 0xd0}
	if err := os.WriteFile(filepath.Join(logDir, "console.log"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := pe.SessionService{Store: st, FilesDir: func(context.Context) (string, error) { return dir, nil }}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{PE: svc, Auth: &fakeAuthService{}})
	get := func(name string, login bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+sess.TaskID+"/logs/"+name, nil)
		if login {
			req.Header.Set("Authorization", "Bearer user-token")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("console.log", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", rec.Code)
	}
	if rec := get("console.log", true); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) || !strings.Contains(rec.Header().Get("Content-Disposition"), "console.log") {
		t.Fatalf("completed log status=%d body=%q headers=%v", rec.Code, rec.Body.Bytes(), rec.Header())
	}
	if rec := get("dism.log", true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing log status=%d", rec.Code)
	}
	if rec := get("ndpe.ini", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("private file status=%d", rec.Code)
	}
}
