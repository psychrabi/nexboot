package winreg

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestREGFValidationRejectsUncommittedAndCorruptHive(t *testing.T) {
	b := make([]byte, 512)
	copy(b, "regf")
	binary.LittleEndian.PutUint32(b[4:], 3)
	binary.LittleEndian.PutUint32(b[8:], 3)
	var sum uint32
	for i := 0; i < 508; i += 4 {
		sum ^= binary.LittleEndian.Uint32(b[i:])
	}
	binary.LittleEndian.PutUint32(b[508:], sum)
	if err := ValidateHeader(b); err != nil {
		t.Fatal(err)
	}
	b[8] = 4
	if err := ValidateHeader(b); err == nil {
		t.Fatal("uncommitted hive accepted")
	}
	b[8] = 3
	b[12] = 1
	if err := ValidateHeader(b); err == nil {
		t.Fatal("corrupt hive accepted")
	}
}

func TestCopyIdentityPreservesRealHive(t *testing.T) {
	if _, err := exec.LookPath("hivexregedit"); err != nil {
		t.Skip("hivexregedit not installed")
	}
	path := os.Getenv("NEXBOOT_TEST_SYSTEM_HIVE")
	if path == "" {
		t.Skip("set NEXBOOT_TEST_SYSTEM_HIVE to a verified template SYSTEM hive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hive := filepath.Join(t.TempDir(), "SYSTEM")
	if err := os.WriteFile(hive, data, 0600); err != nil {
		t.Fatal(err)
	}
	template := os.Getenv("NEXBOOT_TEST_TEMPLATE_INSTANCE")
	if template == "" {
		template = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
	}
	key := os.Getenv("NEXBOOT_TEST_CLASS_KEY")
	if key == "" {
		key = "0010"
	}
	instance := template[:strings.LastIndex(template, "\\")+1] + "005056FFFF2EF65100"
	if err := CopyIdentity(context.Background(), hive, template, instance, key); err != nil {
		t.Fatal(err)
	}
	if err := CopyIdentity(context.Background(), hive, template, instance, key); err != nil {
		t.Fatalf("idempotent copy: %v", err)
	}
}

// 只改 CrashDumpEnabled 一个值，其余键值原样保留。
func TestDisableCrashDumpChangesOnlyThatValue(t *testing.T) {
	if _, err := exec.LookPath("hivexregedit"); err != nil {
		t.Skip("hivexregedit not installed")
	}
	path := os.Getenv("NEXBOOT_TEST_SYSTEM_HIVE")
	if path == "" {
		t.Skip("set NEXBOOT_TEST_SYSTEM_HIVE to a verified template SYSTEM hive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hive := filepath.Join(t.TempDir(), "SYSTEM")
	if err := os.WriteFile(hive, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	whole := func() string {
		raw, err := run(ctx, "--export", "--prefix", prefix, hive, "\\")
		if err != nil {
			t.Fatal(err)
		}
		var keep []string
		for _, line := range strings.Split(canonical(raw, prefix), "\n") {
			if !strings.Contains(line, `"CrashDumpEnabled"=`) {
				keep = append(keep, line)
			}
		}
		return strings.Join(keep, "\n")
	}
	before := whole()
	if err := DisableCrashDump(ctx, hive); err != nil {
		t.Fatal(err)
	}
	cs, err := controlSet(ctx, hive)
	if err != nil {
		t.Fatal(err)
	}
	got, err := export(ctx, hive, cs+`\Control\CrashControl`)
	if err != nil || !strings.Contains(got, `"CrashDumpEnabled"=dword:00000000`) {
		t.Fatalf("CrashControl = %q, %v", got, err)
	}
	if whole() != before {
		t.Fatal("除 CrashDumpEnabled 外还有键值被改动")
	}
	before2, err := os.Stat(hive)
	if err != nil {
		t.Fatal(err)
	}
	if err := DisableCrashDump(ctx, hive); err != nil {
		t.Fatalf("再执行一次也要成功（幂等）：%v", err)
	}
	after2, err := os.Stat(hive)
	if err != nil {
		t.Fatal(err)
	}
	// 已经是 0 就不重写：开机逐台注入时每台都要过这一步。
	if !os.SameFile(before2, after2) {
		t.Fatal("值已经是 0 还重写了注册表文件")
	}
}

const sampleTemplate = "Windows Registry Editor Version 5.00\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\{{CONTROLSET}}\\Enum\\{{INSTANCE}}]\n\"Service\"=\"e1iexpress\"\n\"Driver\"=\"{4d36e972-e325-11ce-bfc1-08002be10318}\\\\{{CLASSKEY}}\"\n\n" +
	"[HKEY_LOCAL_MACHINE\\SYSTEM\\{{CONTROLSET}}\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\{{CLASSKEY}}]\n\"NetCfgInstanceId\"=\"{{GUID}}\"\n\"NetworkAddress\"=\"{{MAC}}\"\n\"Path\"=\"{{INSTANCE_ESC}}\"\n"

func TestRenderTemplateFillsMachineValues(t *testing.T) {
	p := TemplateParams{Instance: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF64E00`, GUID: "{11111111-2222-3333-4444-555555555555}", MAC: "005056ab12cd", ControlSet: "ControlSet001", ClassKey: "0012"}
	out, err := RenderTemplate(sampleTemplate, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`\Enum\PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF64E00]`,
		`\Class\{4d36e972-e325-11ce-bfc1-08002be10318}\0012]`,
		`"NetCfgInstanceId"="{11111111-2222-3333-4444-555555555555}"`,
		`"NetworkAddress"="005056AB12CD"`,
		`"Path"="PCI\\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\\005056FFFF2EF64E00"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Fatalf("placeholder left behind:\n%s", out)
	}
}

func TestRenderTemplateRefusesKeysOutsideControlSetAndBadInput(t *testing.T) {
	p := TemplateParams{Instance: `PCI\VEN_8086&DEV_10D3\X1`, GUID: "{11111111-2222-3333-4444-555555555555}", MAC: "005056ab12cd", ControlSet: "ControlSet001", ClassKey: "0012"}
	cases := map[string]string{
		"outside":     "[HKEY_LOCAL_MACHINE\\SOFTWARE\\Evil]\n\"a\"=\"b\"\n",
		"othercs":     "[HKEY_LOCAL_MACHINE\\SYSTEM\\Setup]\n\"a\"=\"b\"\n",
		"unknown":     "[HKEY_LOCAL_MACHINE\\SYSTEM\\{{CONTROLSET}}\\Services\\{{SERVICE}}]\n",
		"nokeys":      "Windows Registry Editor Version 5.00\n",
		"unescaped-p": "[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet002\\Services\\x]\n",
	}
	for name, tpl := range cases {
		if _, err := RenderTemplate(tpl, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	bad := p
	bad.Instance = `PCI\VEN_8086&DEV_10D3\X"]` + "\n[HKEY_LOCAL_MACHINE\\SOFTWARE\\Evil"
	if _, err := RenderTemplate(sampleTemplate, bad); err == nil {
		t.Error("injected instance accepted")
	}
	bad = p
	bad.MAC = "zz"
	if _, err := RenderTemplate(sampleTemplate, bad); err == nil {
		t.Error("bad mac accepted")
	}
}

func TestFreeClassKeyTakesNextAfterHighest(t *testing.T) {
	exported := "[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}]\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0000]\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0007]\n[HKEY_LOCAL_MACHINE\\SYSTEM\\ControlSet001\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\0007\\Linkage]\n"
	if got, err := freeClassKey(exported); err != nil || got != "0008" {
		t.Fatalf("got %q %v", got, err)
	}
	if got, _ := freeClassKey("[x]\n"); got != "0000" {
		t.Fatalf("empty class gave %q", got)
	}
}
