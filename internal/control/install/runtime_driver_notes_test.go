package install

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 安装环境只看得到「网卡没装上驱动」；驱动要求的系统版本由服务端从 INF 读出来告诉它，失败时才能说清原因。
func TestWindowsPrepareTellsWinPEWhichSystemsTheDriverSupports(t *testing.T) {
	r, s := windowsPrepareFixture(t, "Windows", "", true)
	ctx := context.Background()
	root, _ := r.FilesDir(ctx)
	st, err := store.Open(ctx, "file:"+filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.PE.Store, r.PE.FilesDir = st, r.FilesDir
	s.HWID = `PCI\VEN_15AD&DEV_07B0`

	packPath := filepath.Join(root, "vmxnet3.zip")
	f, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range map[string]string{
		"Win10/vmxnet3.inf": "[Version]\nClass=Net\nDriverVer=11/30/2022,1.9.12.0\n[Manufacturer]\n%VMware% = vmxnet3,NTamd64.10.0...20124\n[vmxnet3.NTamd64.10.0...20124]\n%d% = inst, PCI\\VEN_15AD&DEV_07B0\n[Strings]\nVMware=\"VMware, Inc.\"\n",
		"Win10/vmxnet3.sys": "driver", "Win10/vmxnet3.cat": "catalog",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "vmxnet3", Name: "VMware vmxnet3", Category: domain.DriverCategoryBootCriticalNIC, OSType: domain.OSTypeWindows, Arch: "amd64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{s.HWID}, StoragePath: packPath}); err != nil {
		t.Fatal(err)
	}

	m, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Drivers []struct {
			INF      string `json:"inf"`
			Version  string `json:"version"`
			Source   string `json:"source"`
			MinBuild int    `json:"minbuild"`
		} `json:"drivers"`
	}
	if err := json.Unmarshal(m.Content["nd-install.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Drivers) != 1 {
		t.Fatalf("drivers = %+v", cfg.Drivers)
	}
	d := cfg.Drivers[0]
	if d.INF != `uploaded\Win10\vmxnet3.inf` || d.Version != "1.9.12.0" || d.Source != "uploaded" || d.MinBuild != 20124 {
		t.Fatalf("driver = %+v", d)
	}
}
