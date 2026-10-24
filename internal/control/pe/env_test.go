package pe

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

type fakeStorage struct {
	filesDir string
	extract  func(storage.ExtractWinREReq) (storage.WinREExtract, error)
	reqs     []storage.ExtractWinREReq
}

func (f *fakeStorage) EnsureFilesDir(context.Context) (string, error) { return f.filesDir, nil }
func (f *fakeStorage) ExtractWinRE(_ context.Context, req storage.ExtractWinREReq) (storage.WinREExtract, error) {
	f.reqs = append(f.reqs, req)
	if f.extract != nil {
		return f.extract(req)
	}
	wim := filepath.Join(req.DestDir, "boot.wim")
	_ = os.MkdirAll(req.DestDir, 0o755)
	_ = os.WriteFile(wim, []byte("wim"), 0o644)
	_ = os.WriteFile(filepath.Join(req.DestDir, "boot.sdi"), []byte("sdi"), 0o644)
	return storage.WinREExtract{WIMPath: wim, SDIPath: filepath.Join(req.DestDir, "boot.sdi"), WIMSHA256: "w", SDISHA256: "s", WIMSize: 3}, nil
}

func newEnvFixture(t *testing.T) (EnvService, *store.SQLStore, *fakeStorage) {
	t.Helper()
	st, err := store.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	img := domain.Image{ID: "win11", Name: "Win11", OSType: domain.OSTypeWindows, Purpose: domain.ImagePurposeSystem, State: domain.ImageStateNormal, CreatedAt: now}
	if err := st.Images().Create(ctx, img); err != nil {
		t.Fatal(err)
	}
	red := "win11_0"
	if err := st.Configs().Create(ctx, domain.Config{ID: "win11_default", ImageID: "win11", Name: "default", DefaultReductionID: &red, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: red, ConfigID: "win11_default", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Images().Create(ctx, domain.Image{ID: "data", Name: "游戏盘", OSType: domain.OSTypeWindows, Purpose: domain.ImagePurposeData, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeStorage{filesDir: t.TempDir()}
	svc := EnvService{Store: st, Storage: fs, Now: func() time.Time { return now }, ReadWIM: func(string) (WIMInfo, error) {
		return WIMInfo{Version: "10.0.22621.4037", Build: 22621}, nil
	}}
	return svc, st, fs
}

// 提取用镜像默认配置的当前还原点，文件落在文件库 winpe/<镜像> 下，记录带版本与哈希。
func TestExtractForImageRecordsEnvironment(t *testing.T) {
	svc, st, fs := newEnvFixture(t)
	ctx := context.Background()
	res, err := svc.ExtractForImage(ctx, "win11")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 1 || fs.reqs[0].ConfigID != "win11_default" || fs.reqs[0].SnapshotName != "0" {
		t.Fatalf("extract req = %+v", fs.reqs)
	}
	if !strings.HasPrefix(fs.reqs[0].DestDir, filepath.Join(fs.filesDir, "winpe", "win11")) {
		t.Fatalf("dest = %q", fs.reqs[0].DestDir)
	}
	env, err := st.WinPEEnvs().ByImage(ctx, "win11")
	if err != nil || env.Kind != domain.WinPEEnvImageWinRE || env.Build != 22621 || env.Files["boot.wim"] != "w" {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	task, err := st.Tasks().Get(ctx, res.TaskID)
	if err != nil || task.Status != domain.TaskStatusSuccess || task.Type != domain.TaskTypeWinPEExtract {
		t.Fatalf("task = %+v err = %v", task, err)
	}
	// 重跑覆盖旧记录，不会多出一条。
	if _, err := svc.ExtractForImage(ctx, "win11"); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.WinPEEnvs().List(ctx); len(all) != 1 {
		t.Fatalf("envs = %+v", all)
	}
}

func TestExtractForImageRejectsDataDiskAndRecordsFailure(t *testing.T) {
	svc, st, fs := newEnvFixture(t)
	ctx := context.Background()
	if _, err := svc.ExtractForImage(ctx, "data"); err == nil || !strings.Contains(err.Error(), "数据盘") {
		t.Fatalf("err = %v", err)
	}
	fs.extract = func(storage.ExtractWinREReq) (storage.WinREExtract, error) {
		return storage.WinREExtract{}, errors.New("镜像里没有恢复环境（Winre.wim），请上传用 ADK 生成的适配环境包")
	}
	res, err := svc.ExtractForImage(ctx, "win11")
	if err == nil {
		t.Fatal("提取失败要报出来")
	}
	task, _ := st.Tasks().Get(ctx, res.TaskID)
	if task.Status != domain.TaskStatusFailed || !strings.Contains(task.Error, "恢复环境") {
		t.Fatalf("task = %+v", task)
	}
	if _, err := st.WinPEEnvs().ByImage(ctx, "win11"); err == nil {
		t.Fatal("失败不能留下环境记录")
	}
}

func TestUploadBCDValidatesAndReplaces(t *testing.T) {
	svc, st, fs := newEnvFixture(t)
	ctx := context.Background()
	if _, err := svc.UploadBCD(ctx, []byte("MZ not a hive")); err == nil {
		t.Fatal("非 BCD 要拒绝")
	}
	good := make([]byte, 8192)
	copy(good, "regf")
	env, err := svc.UploadBCD(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != domain.WinPEEnvBCD || env.Files["BCD"] == "" {
		t.Fatalf("env = %+v", env)
	}
	if _, err := os.Stat(filepath.Join(fs.filesDir, "winpe", "bcd", "BCD")); err != nil {
		t.Fatal(err)
	}
	// 再传一次是替换，不是第二份。
	good[100] = 1
	if _, err := svc.UploadBCD(ctx, good); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.WinPEEnvs().ByKind(ctx, domain.WinPEEnvBCD); len(list) != 1 {
		t.Fatalf("bcd envs = %+v", list)
	}
}

func buildADKZip(t *testing.T, files map[string][]byte, manifest map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	if manifest != nil {
		w, _ := zw.Create("manifest.json")
		_ = json.NewEncoder(w).Encode(manifest)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestUploadADKChecksManifestHashesAndVersion(t *testing.T) {
	svc, st, _ := newEnvFixture(t)
	ctx := context.Background()
	bcd := make([]byte, 4096)
	copy(bcd, "regf")
	files := map[string][]byte{"boot.wim": []byte("wim-bytes"), "boot.sdi": []byte("sdi"), "BCD": bcd}
	manifest := map[string]any{"kind": "ndwinpe", "version": "10.0.22621.4037", "build": 22621,
		"files": map[string]string{"boot.wim": sum(files["boot.wim"]), "boot.sdi": sum(files["boot.sdi"]), "BCD": sum(bcd)}}

	env, err := svc.UploadADK(ctx, bytes.NewReader(buildADKZip(t, files, manifest)))
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != domain.WinPEEnvADK || env.Build != 22621 || env.Files["boot.wim"] != sum(files["boot.wim"]) {
		t.Fatalf("env = %+v", env)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "boot.wim")); err != nil {
		t.Fatal(err)
	}

	bad := map[string]any{"kind": "ndwinpe", "version": "10.0.22621.4037", "build": 22621,
		"files": map[string]string{"boot.wim": "0000", "boot.sdi": sum(files["boot.sdi"]), "BCD": sum(bcd)}}
	if _, err := svc.UploadADK(ctx, bytes.NewReader(buildADKZip(t, files, bad))); err == nil || !strings.Contains(err.Error(), "boot.wim") {
		t.Fatalf("哈希不符要点名文件：%v", err)
	}
	if _, err := svc.UploadADK(ctx, bytes.NewReader(buildADKZip(t, files, nil))); err == nil {
		t.Fatal("缺 manifest 要拒绝")
	}
	delete(files, "boot.sdi")
	if _, err := svc.UploadADK(ctx, bytes.NewReader(buildADKZip(t, files, manifest))); err == nil || !strings.Contains(err.Error(), "boot.sdi") {
		t.Fatalf("缺文件要点名：%v", err)
	}
	if list, _ := st.WinPEEnvs().ByKind(ctx, domain.WinPEEnvADK); len(list) != 1 {
		t.Fatalf("失败的上传不能留记录：%+v", list)
	}
}

// 列表把 BCD、ADK 包和每个 Windows 系统镜像的 WinRE 提取状态放在一起；数据盘不出现。
func TestListShowsPerImageWinREStatus(t *testing.T) {
	svc, _, _ := newEnvFixture(t)
	ctx := context.Background()
	view, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.BCD != nil || len(view.ADK) != 0 || len(view.Images) != 1 || view.Images[0].ImageID != "win11" || view.Images[0].WinRE != nil {
		t.Fatalf("view = %+v", view)
	}
	if _, err := svc.ExtractForImage(ctx, "win11"); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.List(ctx)
	if view.Images[0].WinRE == nil || view.Images[0].WinRE.Build != 22621 {
		t.Fatalf("images = %+v", view.Images)
	}
}

func TestDeleteRefusesEnvironmentInUse(t *testing.T) {
	svc, _, _ := newEnvFixture(t)
	ctx := context.Background()
	good := make([]byte, 4096)
	copy(good, "regf")
	env, err := svc.UploadBCD(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	svc.InUse = func(context.Context, string) (string, error) { return "客户机 前台02 的适配任务", nil }
	if err := svc.Delete(ctx, env.ID); err == nil || !strings.Contains(err.Error(), "前台02") {
		t.Fatalf("在用的环境要点名拒绝：%v", err)
	}
	svc.InUse = nil
	if err := svc.Delete(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.Dir); !os.IsNotExist(err) {
		t.Fatal("删除要连文件一起删")
	}
}
