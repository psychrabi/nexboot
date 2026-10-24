package identity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/adapt"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
	"github.com/tianwei/diskless/internal/winreg"
)

const e1000eReg = "[HKEY_LOCAL_MACHINE\\SYSTEM\\{{CONTROLSET}}\\Enum\\{{INSTANCE}}]\n\"Service\"=\"e1iexpress\"\n"

func vmwareE1000E() TemplateEntry {
	return TemplateEntry{Vendor: "8086", Device: "10d3", Manufacturer: "vmware", InstancePrefix: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\`, SuffixRule: SuffixVMwareMAC, Reg: e1000eReg, BuiltinDriver: true}
}

// 机型没有任何适配记录的夹具：e1000e，跑在 VMware 上，允许名单里有它。
func newTemplateFixture(t *testing.T) (*Service, *store.SQLStore, domain.Terminal, *identityHost) {
	t.Helper()
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	if err := st.ModelAdaptations().Delete(ctx, "model"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.HardwareProfiles().Get(ctx, "e1000")
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "8086", "10d3"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.Templates = []TemplateEntry{vmwareE1000E()}
	return s, st, terminal, host
}

func TestUnadaptedNICGeneratesATemplateWithoutAHandwrittenReg(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.created != 1 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
	if host.last.Template == nil || !host.last.Template.Generate || host.last.Template.Reg != "" || host.last.Template.Vendor != "8086" || host.last.Template.Device != "10d3" {
		t.Fatalf("layer request %+v", host.last)
	}
	if host.last.InstanceID != `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000` {
		t.Fatalf("instance %q", host.last.InstanceID)
	}
	if _, err := st.ModelAdaptations().ForProfileReduction(ctx, "e1000", "red"); err == nil {
		t.Fatal("generated template must never write the adaptation table")
	}
}

func TestBuiltinTemplateCoversOtherNICs(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	p, err := st.HardwareProfiles().Get(ctx, "e1000")
	if err != nil {
		t.Fatal(err)
	}
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "15ad", "07b0"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.InstanceID != `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF65000` {
		t.Fatalf("vmxnet3: ready=%v err=%v instance %q", ready, err, host.last.InstanceID)
	}
	p.Manufacturer, p.NICVendor, p.NICDevice = "Dell Inc.", "10ec", "8168"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	ready, err = s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if ready || !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("physical nic should capture first: ready=%v err=%v", ready, err)
	}
}

// VMware 的 E1000 是老式 PCI 设备，Windows 按插槽位置给它编号（如 4&bbf9765&0&2088），
// 不能像 E1000E、vmxnet3 那样由 MAC 推出；按 MAC 写进身份层，Windows 认不出这块网卡，开机 0x7B。
func TestVMwareE1000InstanceIsNotDerivedFromMAC(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	p, err := st.HardwareProfiles().Get(ctx, "e1000")
	if err != nil {
		t.Fatal(err)
	}
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "8086", "100f"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if ready || !errors.Is(err, ErrCaptureStarted) || host.created != 0 {
		t.Fatalf("e1000 must capture its instance first: ready=%v err=%v created=%d instance %q", ready, err, host.created, host.last.InstanceID)
	}
}

// 失败记录只对当时用的实例算数：之前按 MAC 推错了实例、两次没进系统，改为取证后不能还被这条记录挡住。
func TestFailureOnAWrongInstanceDoesNotBlockTheCapturedOne(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	p, _ := st.HardwareProfiles().Get(ctx, "e1000")
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "8086", "100f"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	wrong := `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\005056FFFF2EF65000`
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: wrong, Source: "derived", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 2, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("stale failure on a wrong instance must not block capture: %v", err)
	}
	captured := `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`
	if err := s.put(ctx, &domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: captured, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateFailedRef != "" || v.TemplateStrikes != 0 {
		t.Fatalf("a new instance starts with a clean record: %+v", v)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.InstanceID != captured {
		t.Fatalf("captured instance: ready=%v err=%v instance %q", ready, err, host.last.InstanceID)
	}
	// 同一实例上真的失败过，才挡住。
	v, _ = st.TerminalIdentities().Get(ctx, terminal.ID)
	v.TemplateFailedRef, v.TemplateStrikes = "red", 0
	if err := st.TerminalIdentities().Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	created := host.created
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrAdaptationRequired) || host.created != created {
		t.Fatalf("failure on the same instance still stops the template: ready=%v err=%v", ready, err)
	}
}

// 镜像里已经装着这台机器自己的网卡（例如它就是装系统的那台），原样开机即可，不该再套模板，
// 也不能因此把同型号其他机器记成「没有驱动」。
func TestNICAlreadyInTheImageBootsDirectlyWithoutBlockingOthers(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	ctx := context.Background()
	host.err = errors.New(winreg.InstancePresentPrefix + "PCI\\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\\005056FFFF2EF65000")
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if ready || err != nil {
		t.Fatalf("installed nic boots directly: ready=%v err=%v", ready, err)
	}
	created := host.created
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || err != nil || host.created != created {
		t.Fatalf("the same machine is not re-checked on every boot: ready=%v err=%v created=%d", ready, err, host.created)
	}
	host.err = nil
	other := terminal
	other.ID, other.MAC, other.IP = "t2", "0050562EF651", "192.168.10.43"
	if err := st.Terminals().Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); err != nil || !ready {
		t.Fatalf("another machine of the same model still gets the template: ready=%v err=%v", ready, err)
	}
}

func TestBrokenAllowlistFileDoesNotGenerate(t *testing.T) {
	s, _, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	s.RestrictTemplates = true
	ready, err := s.Prepare(context.Background(), terminal, "cfg", "red", host, "a")
	if ready || err != nil || host.created != 0 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
}

func TestAllowlistedModelWithoutAdaptationBootsFromTemplateWithoutAdaptationRecord(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.created != 1 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
	if host.last.Template == nil || host.last.Template.Reg != e1000eReg || host.last.TemplateInstanceID != "" {
		t.Fatalf("layer request %+v", host.last)
	}
	if host.last.InstanceID != `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000` {
		t.Fatalf("instance %q", host.last.InstanceID)
	}
	if g := host.last.Template.GUID; len(g) != 38 || g[0] != '{' {
		t.Fatalf("guid %q", g)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateBootRef != "red" || v.LayerReductionRef != "red" || v.TemplateBootAt.IsZero() {
		t.Fatalf("identity %+v", v)
	}
	if _, err := st.ModelAdaptations().ForProfileReduction(ctx, "e1000", "red"); err == nil {
		t.Fatal("template boot must never write the adaptation table")
	}
	// 同一台机器每次得到同一个 GUID，身份层才能被复用。
	again := host.last.Template.GUID
	if _, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || host.last.Template.GUID != again {
		t.Fatalf("guid changed: %v %q", err, host.last.Template.GUID)
	}
}

func TestRealAdaptationAlwaysWinsOverTemplate(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	if err := st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "real", ProfileID: "e1000", ConfigID: "cfg", ReductionID: "red", Source: domain.AdaptationAdapted, TemplateInstanceID: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\OLD`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\3&1&0&FE`, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.Template != nil || host.last.TemplateInstanceID == "" {
		t.Fatalf("ready=%v err=%v req=%+v", ready, err, host.last)
	}
}

func TestObservedBootIsProofEnoughSoNoTemplate(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	if err := st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "obs", ProfileID: "e1000", ConfigID: "cfg", ReductionID: "red", Source: domain.AdaptationObserved, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || err != nil || host.created != 0 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
}

func TestUnknownPhysicalInstanceCapturesFirstThenUsesTemplate(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	p, _ := st.HardwareProfiles().Get(ctx, "e1000")
	p.Manufacturer = "Dell Inc."
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	e := vmwareE1000E()
	e.Manufacturer, e.SuffixRule = "", ""
	s.Templates = []TemplateEntry{e}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrCaptureStarted) || host.created != 0 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
	sess, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID)
	if err != nil || sess.Mode != domain.PEModeIdentity {
		t.Fatalf("session %+v %v", sess, err)
	}
	if err := s.PE.Complete(ctx, sess.Token, pe.PEResult{OK: true, Data: []byte(`{"instance_id":"PCI\\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\\3&11583659&0&FE","hwid":"PCI\\VEN_8086&DEV_10D3"}`)}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready || host.last.Template == nil || !strings.HasSuffix(host.last.InstanceID, `\3&11583659&0&FE`) {
		t.Fatalf("ready=%v err=%v req=%+v", ready, err, host.last)
	}
}

func TestTemplateNeedsADriverTheServerActuallyHas(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	e := vmwareE1000E()
	e.BuiltinDriver, e.DriverHWID = false, `PCI\VEN_8086&DEV_10D3`
	s.Templates = []TemplateEntry{e}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || err != nil || host.created != 0 {
		t.Fatalf("no driver must fall back: ready=%v err=%v created=%d", ready, err, host.created)
	}
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "d", Name: "e1000e", Category: domain.DriverCategoryNIC, OSType: domain.OSTypeWindows, Arch: "x64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD`}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready || host.last.Template == nil {
		t.Fatalf("uploaded pack should enable template: ready=%v err=%v", ready, err)
	}
	_ = adapt.DriverService{}
}

func TestRepeatedUnreportedBootsKeepOtherClientsIndependent(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
			t.Fatalf("boot %d: %v %v", i, ready, err)
		}
		now = now.Add(2 * time.Minute)
	}
	other := terminal
	other.ID, other.MAC, other.IP = "t2", "0050562EF651", "192.168.10.43"
	if err := st.Terminals().Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); err != nil || !ready {
		t.Fatalf("other boot %v %v", ready, err)
	}
}

func TestTemplateCallbackResetsFailureCounting(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
			t.Fatalf("boot %d: ready=%v err=%v", i, ready, err)
		}
		v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
		v.TemplateOKRef = "red" // 开机回调
		if err := st.TerminalIdentities().Update(ctx, v); err != nil {
			t.Fatal(err)
		}
		now = now.Add(10 * time.Minute)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateStrikes != 0 || v.TemplateFailedRef != "" {
		t.Fatalf("healthy machine penalised: %+v", v)
	}
}

func TestModelBreakerStopsTemplateAfterTwoMachinesFailed(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	for _, id := range []string{"x1", "x2"} {
		if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: id, TerminalID: id, ProfileID: "e1000", InstanceID: "i", Source: "computed", TemplateFailedRef: "red", TemplateStrikes: 0, TemplateGen: winreg.GeneratorVersion, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrAdaptationRequired) || host.created != 0 {
		t.Fatalf("breaker open: ready=%v err=%v created=%d", ready, err, host.created)
	}
}

func TestBackgroundPrepareNeverCountsFailuresOrStartsPE(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if _, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		now = now.Add(5 * time.Minute)
		if _, err := s.prepare(ctx, terminal, "cfg", "red", host, "a", true); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateStrikes != 0 {
		t.Fatalf("background sweep counted as a failed boot: %+v", v)
	}
}

func TestNextBootPreviewPromisesTemplateWithoutTouchingState(t *testing.T) {
	s, st, terminal, _ := newTemplateFixture(t)
	ctx := context.Background()
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "automatic_template" {
		t.Fatalf("preview %+v %v", v.NextBoot, err)
	}
	if _, err := st.TerminalIdentities().Get(ctx, terminal.ID); err == nil {
		t.Fatal("preview wrote an identity row")
	}
}

func TestLoadTemplatesValidatesBeforeAnyMachineUsesIt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("e1000e.reg", e1000eReg)
	write("evil.reg", "[HKEY_LOCAL_MACHINE\\SOFTWARE\\Evil]\n")
	write("ok.json", `[{"vendor":"8086","device":"10D3","manufacturer":"vmware","instance_prefix":"PCI\\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\\","suffix_rule":"vmware-mac","reg_file":"e1000e.reg","builtin_driver":true}]`)
	write("gen.json", `[{"vendor":"10ec","device":"8168","generate":true}]`)
	list, err := LoadTemplates(filepath.Join(dir, "ok.json"))
	if err != nil || len(list) != 1 || list[0].Device != "10d3" || list[0].Reg != e1000eReg {
		t.Fatalf("%+v %v", list, err)
	}
	generated, err := LoadTemplates(filepath.Join(dir, "gen.json"))
	if err != nil || len(generated) != 1 || !generated[0].Generate || generated[0].Reg != "" || !generated[0].BuiltinDriver {
		t.Fatalf("generate entry %+v %v", generated, err)
	}
	for name, body := range map[string]string{
		"badid.json":    `[{"vendor":"8086","device":"zz","reg_file":"e1000e.reg","builtin_driver":true}]`,
		"badrule.json":  `[{"vendor":"8086","device":"10d3","suffix_rule":"magic","reg_file":"e1000e.reg","builtin_driver":true}]`,
		"nodriver.json": `[{"vendor":"8086","device":"10d3","reg_file":"e1000e.reg"}]`,
		"evil.json":     `[{"vendor":"8086","device":"10d3","reg_file":"evil.reg","builtin_driver":true}]`,
		"missing.json":  `[{"vendor":"8086","device":"10d3","reg_file":"nope.reg","builtin_driver":true}]`,
		"notjson.json":  `nope`,
	} {
		write(name, body)
		if _, err := LoadTemplates(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestBreakerForgetsFailuresOnAnOlderRestorePoint(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	for _, id := range []string{"x1", "x2"} {
		if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: id, TerminalID: id, ProfileID: "e1000", InstanceID: "i", Source: "derived", TemplateFailedRef: "older-point", TemplateStrikes: 2, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
		t.Fatalf("old failures must not block a new restore point: ready=%v err=%v", ready, err)
	}
}

func e1000Model(t *testing.T, st *store.SQLStore) {
	t.Helper()
	ctx := context.Background()
	p, _ := st.HardwareProfiles().Get(ctx, "e1000")
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "8086", "100f"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func secondMachine(t *testing.T, st *store.SQLStore, first domain.Terminal) domain.Terminal {
	t.Helper()
	other := first
	other.ID, other.MAC, other.IP = "t2", "0050562EF651", "192.168.10.43"
	if err := st.Terminals().Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	return other
}

const slotInstance = `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`

// 同型号、同插槽布局的机器，按位置编的实例号相同：一台取过证，其他机器直接用，不再每台进 WinPE。
func TestSameModelReusesAnotherMachinesCapturedInstance(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: slotInstance, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	other := secondMachine(t, st, terminal)
	ready, err := s.Prepare(ctx, other, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.InstanceID != slotInstance || host.last.MAC != other.MAC {
		t.Fatalf("reuse: ready=%v err=%v req=%+v", ready, err, host.last)
	}
	v, _ := st.TerminalIdentities().Get(ctx, other.ID)
	if v.Source != "model" || v.InstanceID != slotInstance {
		t.Fatalf("identity %+v", v)
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, other.ID); err == nil {
		t.Fatal("no WinPE session for a machine of a known model")
	}
}

// 按序列号编的实例号（PCIe 网卡带序列号时）只属于那一台，不能借给别的机器。
func TestMachineSpecificInstancesAreNotShared(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\00A0C9FFFF0A0B0C00`, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	other := secondMachine(t, st, terminal)
	if _, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("serial-numbered instance must not be shared: %v", err)
	}
}

// 借来的实例号这台用不了（两次没进系统），说明插槽不一样：改为给它自己取证，而不是放弃模板。
func TestReusedInstanceThatFailsFallsBackToCapture(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: slotInstance, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	other := secondMachine(t, st, terminal)
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: other.ID, TerminalID: other.ID, ProfileID: "e1000", InstanceID: slotInstance, Source: "model", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 0, TemplateGen: winreg.GeneratorVersion, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, other, "cfg", "red", host, "a"); !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("failed borrowed instance should capture its own: %v", err)
	}
}

// 机型已真实适配、实例号按位置编：同型号其他机器直接用适配那台的实例号，不再取证。
func TestAdaptedModelReusesTheAdaptedSlotInstance(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	e1000Model(t, st)
	ctx := context.Background()
	a, err := st.ModelAdaptations().ForProfileReduction(ctx, "e1000", "red")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ModelAdaptations().Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	a.TemplateInstanceID = slotInstance
	if err := st.ModelAdaptations().Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.InstanceID != slotInstance || host.last.TemplateInstanceID != slotInstance {
		t.Fatalf("reuse adapted slot: ready=%v err=%v req=%+v", ready, err, host.last)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.Source != "model" {
		t.Fatalf("identity %+v", v)
	}
	// 后台重建不把借来的实例当成过期。
	stale, err := s.computedStale(ctx, terminal, v)
	if err != nil || stale {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
}

// 失败是在旧版生成规则下记的：规则改了以后，这台机器和型号熔断都按新规则从头计。
func TestFailuresUnderAnOlderGeneratorDoNotBlock(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	s.Templates = nil
	e1000Model(t, st)
	ctx := context.Background()
	for _, id := range []string{"x1", "x2"} {
		if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: id, TerminalID: id, ProfileID: "e1000", InstanceID: slotInstance, Source: "captured", TemplateFailedRef: "red", TemplateStrikes: 2, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "e1000", InstanceID: slotInstance, Source: "captured", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 2, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.last.InstanceID != slotInstance {
		t.Fatalf("new generator retries: ready=%v err=%v", ready, err)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateFailedRef != "" || v.TemplateStrikes != 0 || v.TemplateGen != winreg.GeneratorVersion || v.TemplateBootRef != "red" {
		t.Fatalf("identity %+v", v)
	}
}

func TestMissingCallbackNeverStrikesOrBlocksTemplate(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
			t.Fatalf("missing callback blocked boot %d: %v %v", i, ready, err)
		}
		now = now.Add(2 * time.Minute)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateStrikes != 0 || v.TemplateFailedRef != "" {
		t.Fatalf("missing callback became proof: %+v", v)
	}
}

func TestLegacyInferredStrikesDoNotBlockPreviewOrPrepare(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	instance, _ := vmwareE1000E().instanceFor(terminal.MAC)
	for _, id := range []string{terminal.ID, "legacy-other"} {
		if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: id, TerminalID: id, ProfileID: terminal.HardwareProfileID, InstanceID: instance, Source: "derived", TemplateFailedRef: "red", TemplateStrikes: 2, TemplateGen: winreg.GeneratorVersion}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Reason != "automatic_template" {
		t.Fatalf("legacy preview %+v %v", view.NextBoot, err)
	}
	old, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if old.TemplateStrikes != 2 {
		t.Fatal("preview wrote state")
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
		t.Fatalf("legacy prepare %v %v", ready, err)
	}
	v, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if v.TemplateFailedRef != "" || v.TemplateStrikes != 0 {
		t.Fatalf("legacy state %+v", v)
	}
}
