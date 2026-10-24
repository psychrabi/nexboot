package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/install"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func TestInstallationTokenEndpointsRequireAssignedIPAndAdminViewsHideSecrets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := domain.InstallSession{ID: "install-test", MAC: "001122334455", IP: "192.168.50.10", Token: "secret-token", Password: "secret-password", Phase: "verifying", Attempt: 1, BootID: "boot-proof"}
	if err := st.InstallSessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	svc := &install.Service{Store: st}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{Install: svc, Auth: &fakeAuthService{}})
	request := func(path, remote, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("/install/secret-token/boot.json", "192.168.50.11:1234", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("neighbor obtained installation config: %d %s", rec.Code, rec.Body.String())
	}
	rec := request("/install/secret-token/boot.json", "192.168.50.10:1234", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("client boot.json=%d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["boot_id"] != "boot-proof" {
		t.Fatalf("boot config=%v, %v", body, err)
	}
	if rec := request("/api/installations", "192.168.50.10:1234", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("installation administration must require login: %d", rec.Code)
	}
	rec = request("/api/installations/install-test", "192.168.50.10:1234", "admin")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "secret-password") {
		t.Fatalf("admin view leaked session credentials: %d %s", rec.Code, rec.Body.String())
	}
}
func TestInstallationRequestLogsHideBearerTokens(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := requestLogger(logger)(http.NotFoundHandler())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/install/private-bearer-token/boot.json", nil))
	if strings.Contains(logs.String(), "private-bearer-token") {
		t.Fatal("installation bearer token leaked into server access log")
	}
}

func TestPERequestLogsHideBearerTokens(t *testing.T) {
	for _, from := range []string{"192.168.10.40", "192.168.10.99"} {
		t.Run(from, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			router := NewRouter(logger, nil, Services{PE: &fakePEService{terminalIP: "192.168.10.40"}})
			req := httptest.NewRequest(http.MethodGet, "/boot/pe/tok/ndpe.ini", nil)
			req.RemoteAddr = from + ":1234"
			router.ServeHTTP(httptest.NewRecorder(), req)
			if strings.Contains(logs.String(), "/boot/pe/tok/") {
				t.Fatalf("PE bearer token leaked into server logs: %s", logs.String())
			}
			if !strings.Contains(logs.String(), "/boot/pe/[redacted]/ndpe.ini") {
				t.Fatalf("PE operation must remain identifiable without its token: %s", logs.String())
			}
		})
	}
}
