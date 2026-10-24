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

func TestWindowsDriverStagingUsesSelectedEditionUTF16AndInvalidatesChangedHWID(t *testing.T) {
	r, s := windowsPrepareFixture(t, "wInDoWs", "", true)
	ctx := context.Background()
	wim := os.Getenv("FIXTURE_WIM")
	tree := filepath.Join(filepath.Dir(wim), "tree")
	pkg := filepath.Join(tree, "wInDoWs", "sYsTeM32", "DriverStore", "FileRepository", "vmxnet3.inf_amd64_second")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	inf := wideScript([]byte("[Version]\nClass=Net\n[Manufacturer]\nVMware=Models,NTamd64\n[Models.NTamd64]\nDevice=Install,,PCI\\VEN_15AD&DEV_07B0&SUBSYS_12345678\n"))
	for name, data := range map[string][]byte{"VMXNET3.INF": inf, "vmxnet3.sys": []byte("edition two driver")} {
		if err := os.WriteFile(filepath.Join(pkg, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("wimlib-imagex", "append", tree, wim, "second", "--no-acls").CombinedOutput(); err != nil {
		t.Fatalf("append second edition: %v %s", err, out)
	}
	s.EditionIndex = 2
	s.HWID = `pci\ven_15ad&dev_07b0`
	m, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(m.Content["nd-driver-files.txt"])), "vmxnet3.inf") {
		t.Fatal("selected edition UTF16 INF not staged")
	}
	var sysPath string
	for _, path := range m.Files {
		if strings.HasSuffix(path, "vmxnet3.sys") {
			sysPath = path
		}
	}
	if b, err := os.ReadFile(sysPath); err != nil || string(b) != "edition two driver" {
		t.Fatalf("wrong edition driver: %q %v", b, err)
	}
	if err := os.Remove(sysPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(ctx, s); err != nil {
		t.Fatalf("half-cache recovery failed: %v", err)
	}
	if _, err := os.Stat(sysPath); err != nil {
		t.Fatalf("half-cache driver was not rebuilt: %v", err)
	}
	s.HWID = `PCI\VEN_15AD&DEV_07B01`
	m, err = r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Content["nd-driver-files.txt"]) != 0 {
		t.Fatal("changed HWID reused previous package or matched without ID boundary")
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "driver-hwid")); !os.IsNotExist(err) {
		t.Fatal("no-match result must not cache success")
	}
	s.EditionIndex = 1
	s.HWID = `PCI\VEN_15AD&DEV_07B0`
	m, err = r.Prepare(ctx, s)
	if err != nil || len(m.Content["nd-driver-files.txt"]) != 0 {
		t.Fatalf("driver from another edition leaked: %v", err)
	}
}

func TestWindowsDriverStagingNoMatchCanRetryAfterDriverUpload(t *testing.T) {
	r, s := windowsPrepareFixture(t, "Windows", "", true)
	ctx := context.Background()
	root, _ := r.FilesDir(ctx)
	st, err := store.Open(ctx, "file:"+filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.PE.Store = st
	r.PE.FilesDir = r.FilesDir
	s.HWID = `PCI\VEN_15AD&DEV_07B0`
	m, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Content["nd-driver-files.txt"]) != 0 {
		t.Fatal("no-match must leave WinRE inbox path available")
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "driver-hwid")); !os.IsNotExist(err) {
		t.Fatal("empty result marked ready")
	}
	packPath := filepath.Join(root, "uploaded.zip")
	f, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"uploaded.inf", "uploaded.sys"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte("uploaded driver")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	pack := domain.DriverPack{ID: "uploaded", Name: "Uploaded NIC", Category: domain.DriverCategoryBootCriticalNIC, OSType: domain.OSTypeWindows, Arch: "amd64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{s.HWID}, StoragePath: packPath}
	if err := st.DriverPacks().Create(ctx, pack); err != nil {
		t.Fatal(err)
	}
	m, err = r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(m.Content["nd-driver-files.txt"]), "uploaded.sys") {
		t.Fatal("newly uploaded fallback ignored after empty result")
	}
}

func TestWindowsDriverStagingReadFailureDoesNotPublishCache(t *testing.T) {
	r, s := windowsPrepareFixture(t, "Windows", "", true)
	ctx := context.Background()
	m, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Files["install.wim"], []byte("corrupted WIM"), 0600); err != nil {
		t.Fatal(err)
	}
	s.HWID = `PCI\VEN_15AD&DEV_07B0`
	_, err = r.Prepare(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "读取 Windows ISO") {
		t.Fatalf("WIM read failure treated as no match: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "driver-hwid")); !os.IsNotExist(err) {
		t.Fatal("failed extraction published marker")
	}
	for _, pattern := range []string{".drivers-*", ".driver-inf-*"} {
		paths, err := filepath.Glob(filepath.Join(m.Dir, pattern))
		if err != nil || len(paths) > 0 {
			t.Fatalf("private staging not cleaned: %v %v", paths, err)
		}
	}
}
