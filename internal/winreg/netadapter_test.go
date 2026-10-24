package winreg

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 自建最小 amd64 PE，只含公开 WDF 类绑定结构和名称，不包含第三方驱动代码。
func testDriverPE(class string) []byte {
	b := make([]byte, 2048)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[0x84:], 0x8664)
	binary.LittleEndian.PutUint16(b[0x86:], 1)
	binary.LittleEndian.PutUint16(b[0x94:], 240)
	o := 0x98
	binary.LittleEndian.PutUint16(b[o:], 0x20b)
	binary.LittleEndian.PutUint64(b[o+24:], 0x140000000)
	binary.LittleEndian.PutUint32(b[o+32:], 0x1000)
	binary.LittleEndian.PutUint32(b[o+36:], 0x200)
	binary.LittleEndian.PutUint32(b[o+56:], 0x2000)
	binary.LittleEndian.PutUint32(b[o+60:], 0x200)
	binary.LittleEndian.PutUint32(b[o+108:], 16)
	s := o + 240
	copy(b[s:], ".data")
	binary.LittleEndian.PutUint32(b[s+8:], 0x600)
	binary.LittleEndian.PutUint32(b[s+12:], 0x1000)
	binary.LittleEndian.PutUint32(b[s+16:], 0x600)
	binary.LittleEndian.PutUint32(b[s+20:], 0x200)
	binary.LittleEndian.PutUint32(b[s+36:], 0xc0000040)
	if class != "" {
		binary.LittleEndian.PutUint32(b[0x200:], 80)
		binary.LittleEndian.PutUint64(b[0x208:], 0x140001100)
		binary.LittleEndian.PutUint32(b[0x210:], 2)
		for i, c := range []byte(class) {
			b[0x300+2*i] = c
		}
	}
	return b
}

func stageSelectorDriver(t *testing.T, root, name, hwid string, sys []byte) {
	t.Helper()
	pkg := filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", name+".inf_amd64_test")
	inf := "[Version]\nClass=Net\n[Manufacturer]\nMaker=Models,NTamd64\n[Models.NTamd64]\nNIC=Install," + hwid + "\n[Install.Services]\nAddService=" + name + ",2,Service\n[Service]\nServiceBinary=%12%\\" + name + ".sys\n[Install.Wdf]\nKmdfService=" + name + ",Wdf\n[Wdf]\nKmdfLibraryVersion=1.31\n"
	for _, p := range []string{pkg, filepath.Join(root, "Windows", "INF"), filepath.Join(root, "Windows", "System32", "config")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for p, b := range map[string][]byte{filepath.Join(pkg, name+".inf"): []byte(inf), filepath.Join(root, "Windows", "INF", name+".inf"): []byte(inf), filepath.Join(pkg, name+".sys"): sys} {
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func selectorSpec() GenerateSpec {
	return GenerateSpec{Instance: `PCI\VEN_10EC&DEV_8168&SUBSYS_012310EC&REV_15\01000000684CE00000`, GUID: "{71BAE816-BED8-A49D-4051-CA3B9867AF1A}", MAC: "7070FC0587A2", Vendor: "10EC", Device: "8168"}
}

func TestApplyGeneratedChoosesISCSIDriverBeforeHardwareRanking(t *testing.T) {
	for _, existingCX := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact_CX_vs_compatible_NDIS", true: "installed_SYS_wins_over_repository"}[existingCX], func(t *testing.T) {
			root := t.TempDir()
			precise := testDriverPE("NetAdapterCx")
			if existingCX {
				precise = testDriverPE("")
			}
			stageSelectorDriver(t, root, "precise", `PCI\VEN_10EC&DEV_8168&REV_15`, precise)
			safe := testDriverPE("Ucx") // 普通 WDF 绑定不能被一律当作 NetAdapterCx。
			for i, c := range []byte("NetAdapterCx") {
				safe[0x400+2*i] = c
			} // 单纯 UTF16 名称不是类绑定证据。
			stageSelectorDriver(t, root, "compatible", `PCI\VEN_10EC&DEV_8168`, safe)
			if existingCX {
				dir := filepath.Join(root, "Windows", "System32", "drivers")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "precise.sys"), testDriverPE("NetAdapterCx"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
			if err := os.WriteFile(hive, []byte("not-a-hive"), 0600); err != nil {
				t.Fatal(err)
			}
			err := ApplyGenerated(context.Background(), root, hive, selectorSpec())
			if err == nil || IsSkipTemplate(err) {
				t.Fatalf("expected only the synthetic hive to reject: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(root, "Windows", "System32", "drivers", "compatible.sys"))
			if err != nil || !bytes.Equal(got, safe) {
				t.Fatalf("did not select compatible traditional/WDF driver: %v", err)
			}
		})
	}
}

func TestApplyGeneratedBlocksOnlyNetAdapterCxWithoutWriting(t *testing.T) {
	root := t.TempDir()
	stageSelectorDriver(t, root, "any_service_name", `PCI\VEN_10EC&DEV_8168&REV_15`, testDriverPE("NetAdapterCx"))
	hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
	before := []byte("not-a-hive")
	if err := os.WriteFile(hive, before, 0600); err != nil {
		t.Fatal(err)
	}
	err := ApplyGenerated(context.Background(), root, hive, selectorSpec())
	if !IsGenerationFailure(err) || IsSkipTemplate(err) || !strings.Contains(err.Error(), "NetAdapterCx") || !strings.Contains(err.Error(), "NDIS") {
		t.Fatalf("must block with an actionable unsupported-driver error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Windows", "System32", "drivers")); !os.IsNotExist(err) {
		t.Fatalf("unsupported candidate wrote driver directory: %v", err)
	}
	after, _ := os.ReadFile(hive)
	if !bytes.Equal(before, after) {
		t.Fatal("unsupported candidate touched hive")
	}
}

func TestApplyGeneratedRejectsUninspectableServiceBinary(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid_PE", true: "missing_SYS"}[missing], func(t *testing.T) {
			root := t.TempDir()
			stageSelectorDriver(t, root, "broken", `PCI\VEN_10EC&DEV_8168`, []byte("not-a-PE"))
			if missing {
				if err := os.Remove(filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", "broken.inf_amd64_test", "broken.sys")); err != nil {
					t.Fatal(err)
				}
			}
			hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
			if err := os.WriteFile(hive, []byte("not-a-hive"), 0600); err != nil {
				t.Fatal(err)
			}
			err := ApplyGenerated(context.Background(), root, hive, selectorSpec())
			if !IsGenerationFailure(err) || IsSkipTemplate(err) || !strings.Contains(err.Error(), "broken.sys") {
				t.Fatalf("cannot classify malformed/missing SYS as safe: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "Windows", "System32", "drivers")); !os.IsNotExist(err) {
				t.Fatalf("uninspectable candidate wrote driver directory: %v", err)
			}
		})
	}
}

func TestNetAdapterCxUnknownClassBindingLayoutFailsClosed(t *testing.T) {
	b := testDriverPE("NetAdapterCx")
	binary.LittleEndian.PutUint32(b[0x200:], 96)
	p := filepath.Join(t.TempDir(), "new-layout.sys")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	cx, err := bindsNetAdapterCx(p)
	if !cx && (err == nil || !strings.Contains(err.Error(), "NetAdapterCx")) {
		t.Fatalf("unknown class binding was classified as safe: %v %v", cx, err)
	}
}

func TestNetAdapterCxClassPointerCannotOverflowPEBounds(t *testing.T) {
	b := testDriverPE("NetAdapterCx")
	binary.LittleEndian.PutUint64(b[0x98+24:], 0)
	binary.LittleEndian.PutUint32(b[0x98+240+12:], 0)
	binary.LittleEndian.PutUint64(b[0x208:], ^uint64(0))
	p := filepath.Join(t.TempDir(), "bad-pointer.sys")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if cx, _ := bindsNetAdapterCx(p); cx {
		t.Fatal("out-of-bounds class pointer was used")
	}
}

func TestApplyGeneratedDoesNotCacheTransientSYSReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks need an unprivileged test process")
	}
	root := t.TempDir()
	stageSelectorDriver(t, root, "read_failure", `PCI\VEN_10EC&DEV_8168`, testDriverPE(""))
	p := filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", "read_failure.inf_amd64_test", "read_failure.sys")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0600)
	err := ApplyGenerated(context.Background(), root, filepath.Join(root, "Windows", "System32", "config", "SYSTEM"), selectorSpec())
	if err == nil || IsSkipTemplate(err) || IsGenerationFailure(err) || !strings.Contains(err.Error(), "read_failure.sys") {
		t.Fatalf("read failure must block without deterministic negative caching: %v", err)
	}
}

func TestApplyGeneratedMixedRejectionsKeepTransientReadFailureRetryable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks need an unprivileged test process")
	}
	for _, readFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "CX_then_read_error", true: "read_error_then_CX"}[readFirst], func(t *testing.T) {
			root := t.TempDir()
			readName, cxName := "z_read", "a_cx"
			if readFirst {
				readName, cxName = "a_read", "z_cx"
			}
			safe := testDriverPE("")
			stageSelectorDriver(t, root, readName, `PCI\VEN_10EC&DEV_8168`, safe)
			stageSelectorDriver(t, root, cxName, `PCI\VEN_10EC&DEV_8168&REV_15`, testDriverPE("NetAdapterCx"))
			sys := filepath.Join(root, "Windows", "System32", "DriverStore", "FileRepository", readName+".inf_amd64_test", readName+".sys")
			if err := os.Chmod(sys, 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(sys, 0600)
			hive := filepath.Join(root, "Windows", "System32", "config", "SYSTEM")
			if err := os.WriteFile(hive, []byte("not-a-hive"), 0600); err != nil {
				t.Fatal(err)
			}
			err := ApplyGenerated(context.Background(), root, hive, selectorSpec())
			if err == nil || IsGenerationFailure(err) || IsSkipTemplate(err) || !strings.Contains(err.Error(), readName+".sys") {
				t.Fatalf("candidate order changed transient failure into permanent rejection: %v", err)
			}
			if err := os.Chmod(sys, 0600); err != nil {
				t.Fatal(err)
			}
			_ = ApplyGenerated(context.Background(), root, hive, selectorSpec())
			got, err := os.ReadFile(filepath.Join(root, "Windows", "System32", "drivers", readName+".sys"))
			if err != nil || !bytes.Equal(got, safe) {
				t.Fatalf("restored read did not retry/select traditional driver: %v", err)
			}
		})
	}
}
