package install

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 系统自带的 vmxnet3 只有系统级签名，WinRE 里可能加载不了；上传的厂商驱动包必须一起带上并排在前面。
func TestWindowsDriverStagingCarriesUploadedAndISODrivers(t *testing.T) {
	r, s := windowsPrepareFixture(t, "Windows", "", true)
	ctx := context.Background()
	root, _ := r.FilesDir(ctx)
	st, err := store.Open(ctx, "file:"+filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.PE.Store, r.PE.FilesDir = st, r.FilesDir
	wim := os.Getenv("FIXTURE_WIM")
	tree := filepath.Join(filepath.Dir(wim), "tree")
	pkg := filepath.Join(tree, "Windows", "System32", "DriverStore", "FileRepository", "vmxnet3.inf_amd64_inbox")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	inf := "[Version]\nClass=Net\n[Manufacturer]\nVMware=Models,NTamd64\n[Models.NTamd64]\nVMXNET3=Install,,PCI\\VEN_15AD&DEV_07B0\n"
	for name, data := range map[string]string{"vmxnet3.inf": inf, "vmxnet3.sys": "inbox driver"} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(wim); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("wimlib-imagex", "capture", tree, wim, "--no-acls").CombinedOutput(); err != nil {
		t.Fatalf("capture: %v %s", err, out)
	}
	s.HWID = `PCI\VEN_15AD&DEV_07B0`

	// 先只有 ISO 自带驱动：缓存起来。
	m, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(m.Content["nd-driver-files.txt"])
	if !strings.Contains(listing, `windows\vmxnet3.inf_amd64_inbox\vmxnet3.sys`) || strings.Contains(listing, "uploaded") {
		t.Fatalf("inbox only listing = %q", listing)
	}

	// 之后上传厂商驱动包：不能被 ISO 驱动的缓存挡住，并且排在前面先加载。
	packPath := filepath.Join(root, "vendor.zip")
	f, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"vmxnet3.inf", "vmxnet3.sys", "vmxnet3.cat"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte("vendor " + name))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "vendor", Name: "VMware vmxnet3", Category: domain.DriverCategoryBootCriticalNIC, OSType: domain.OSTypeWindows, Arch: "amd64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{s.HWID}, StoragePath: packPath}); err != nil {
		t.Fatal(err)
	}
	m, err = r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(m.Content["nd-driver-files.txt"]), "\r", "")), "\n")
	var order []string
	for _, line := range lines {
		rel := strings.SplitN(line, "|", 2)[1]
		order = append(order, strings.SplitN(rel, `\`, 2)[0])
		alias := strings.SplitN(line, "|", 2)[0]
		if _, err := os.Stat(m.Files[alias]); err != nil {
			t.Fatalf("alias %s not staged: %v", alias, err)
		}
	}
	joined := strings.Join(order, ",")
	if !strings.HasPrefix(joined, "uploaded,uploaded,uploaded") || !strings.Contains(joined, "windows") {
		t.Fatalf("uploaded must come first and inbox must stay: %v", lines)
	}

	// 驱动包删掉后，安装环境里也不再带它。
	if err := st.DriverPacks().Delete(ctx, "vendor"); err != nil {
		t.Fatal(err)
	}
	m, err = r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(m.Content["nd-driver-files.txt"]), "uploaded") {
		t.Fatalf("deleted pack still staged: %q", m.Content["nd-driver-files.txt"])
	}
}
