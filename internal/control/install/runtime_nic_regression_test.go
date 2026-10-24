package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/store"
)

func TestWindowsPrepareRecoversVMXNET3FromSelectedWIMDespiteEmptyLegacyCache(t *testing.T) {
	r, s := windowsPrepareFixture(t, "wInDoWs", "", true)
	ctx := context.Background()
	root, err := r.FilesDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "file:"+filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.PE.Store = st
	wim := os.Getenv("FIXTURE_WIM")
	tree := filepath.Join(filepath.Dir(wim), "tree")
	pkg := filepath.Join(tree, "wInDoWs", "sYsTeM32", "DriverStore", "FileRepository", "vmxnet3.inf_amd64_fixture")
	inf := "[Version]\nClass=Net\nCatalogFile=vmxnet3.cat\n[Manufacturer]\nVMware=Models,NTamd64\n[Models.NTamd64]\nVMXNET3=Install,PCI\\VEN_15AD&DEV_07B0\n"
	for name, data := range map[string]string{"vmxnet3.inf": inf, "vmxnet3.sys": "vmxnet3 driver", "vmxnet3.cat": "signed catalog", "extra/helper.dll": "driver dependency"} {
		path := filepath.Join(pkg, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(wim); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("wimlib-imagex", "capture", tree, wim, "--no-acls").CombinedOutput(); err != nil {
		t.Fatalf("capture driver fixture: %v: %s", err, out)
	}
	// The old implementation recorded a successful HWID cache even with no staged files.
	initial, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	s.HWID = `PCI\VEN_15ad&DEV_07b0`
	if err := os.WriteFile(filepath.Join(initial.Dir, "driver-hwid"), []byte(s.HWID), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := r.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(prepared.Content["nd-driver-files.txt"])
	for _, name := range []string{"vmxnet3.inf", "vmxnet3.sys", "vmxnet3.cat", `extra\helper.dll`} {
		if !strings.Contains(strings.ToLower(listing), name) {
			t.Errorf("WinPE did not receive complete matching driver package: missing %s in %q", name, listing)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(fields) != 2 {
			t.Fatalf("invalid WinPE driver list entry: %q", line)
		}
		if _, err := os.Stat(prepared.Files[fields[0]]); err != nil {
			t.Fatalf("WinPE alias %s has no staged file: %v", fields[0], err)
		}
	}
}
