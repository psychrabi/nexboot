package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/assets"
	"github.com/tianwei/diskless/internal/control/platform"
)

type fakeLegacyImports struct{ moved string }

func (f *fakeLegacyImports) List(context.Context) (assets.LegacyImportList, error) {
	return assets.LegacyImportList{ImportDir: "/ndiskless/tank/nd/files/imports", Dirs: []string{"/var/lib/ndiskless/imports"},
		Items: []assets.LegacyImportFile{{Name: "win10.iso", Path: "/var/lib/ndiskless/imports/win10.iso", Dir: "/var/lib/ndiskless/imports", Size: 5}}}, nil
}

func (f *fakeLegacyImports) Move(_ context.Context, req assets.MoveLegacyImportRequest) (assets.LegacyImportFile, error) {
	f.moved = req.Path
	return assets.LegacyImportFile{Path: req.Path, Moving: true}, nil
}

func TestLegacyImportRoutes(t *testing.T) {
	fake := &fakeLegacyImports{}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{LegacyImports: fake})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/imports/legacy", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "win10.iso") {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/imports/legacy/move", bytes.NewBufferString(`{"path":"/var/lib/ndiskless/imports/win10.iso"}`)))
	if rec.Code != http.StatusAccepted || fake.moved != "/var/lib/ndiskless/imports/win10.iso" {
		t.Fatalf("move status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 没有存储池时 /api/runtime 不报错，导入目录为空并带上原因。
func TestRuntimeEndpointReportsNoPool(t *testing.T) {
	settings := &fakeSettingsService{view: platform.SystemSettingsView{ImportDirError: "请先创建存储池"}}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Services{Settings: settings})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	var got RuntimeConfig
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.ImportDir != "" || got.ImportDirError != "请先创建存储池" {
		t.Fatalf("status=%d runtime=%#v", rec.Code, got)
	}
}
