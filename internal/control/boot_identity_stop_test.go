package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/boot"
	"github.com/tianwei/diskless/internal/control/identity"
	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
	"github.com/tianwei/diskless/internal/winreg"
)

func TestBootCapturedTemplateFailureRequiresAdaptationWithoutExportOrCapture(t *testing.T) {
	ctx := context.Background()
	st := seedBootStore(t)
	now := time.Now().UTC()
	p := domain.HardwareProfile{ID: "e1000-failed", Name: "Intel E1000", Fingerprint: "8086:100f//efi", Manufacturer: "VMware", NICVendor: "8086", NICDevice: "100f", CreatedAt: now, UpdatedAt: now}
	if err := st.HardwareProfiles().Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	terminal, _ := st.Terminals().Get(ctx, "term-1")
	terminal.Name, terminal.HardwareProfileID = "33", p.ID
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	instance := `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&bbf9765&0&2088`
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: p.ID, InstanceID: instance, Source: "captured", TemplateFailedRef: "red-1", TemplateStrikes: 0, TemplateGen: winreg.GeneratorVersion}); err != nil {
		t.Fatal(err)
	}
	host := &fallbackIdentityStorage{fakeStorage: &fakeStorage{}}
	ids := &identity.Service{Store: st, Storage: host, NodeID: "writer"}
	service := BootService{Store: st, Storage: host, Builder: boot.Builder{}, Identity: ids}
	script, err := service.BuildBootScript(ctx, terminal.MAC)
	if err == nil || script != "" || len(host.calls) != 0 || host.identityCalls != 0 {
		t.Fatalf("failed captured NIC must stop before disk export or capture: script=%q err=%v clientCalls=%v identityCalls=%d", script, err, host.calls, host.identityCalls)
	}
	for _, name := range []string{"33", terminal.MAC, "Intel E1000", "还原点", "WinPE", "机型适配"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("operator error must identify %q: %v", name, err)
		}
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("must not restart identity capture: %v", err)
	}
	view, err := ids.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Kind == "system" || view.NextBoot.Kind == "winpe" {
		t.Fatalf("preview must not promise boot/capture: %+v %v", view.NextBoot, err)
	}
}

func TestBootTemplateStopsKeepNativeAndAdaptedPathsAvailable(t *testing.T) {
	for _, mode := range []string{"failed", "breaker", "generation_failed", "linux", "super", "observed", "adapted", "installed", "no_driver"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st := seedBootStore(t)
			now := time.Now().UTC()
			p := domain.HardwareProfile{ID: "vmx", Name: "vmxnet3", Fingerprint: "15ad:07b0", Manufacturer: "VMware", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: now, UpdatedAt: now}
			if err := st.HardwareProfiles().Create(ctx, p); err != nil {
				t.Fatal(err)
			}
			term, _ := st.Terminals().Get(ctx, "term-1")
			term.HardwareProfileID = p.ID
			if mode == "super" {
				term.IsSuper = true
			}
			if err := st.Terminals().Update(ctx, term); err != nil {
				t.Fatal(err)
			}
			instance, _ := identity.ComputedInstance(p, term.MAC, `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\OLD`)
			id := domain.TerminalIdentity{ID: term.ID, TerminalID: term.ID, ProfileID: p.ID, InstanceID: instance, Source: "captured", TemplateFailedRef: "red-1", TemplateStrikes: 0, TemplateGen: winreg.GeneratorVersion}
			if mode != "generation_failed" && mode != "installed" && mode != "no_driver" {
				if err := st.TerminalIdentities().Create(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "breaker" {
				id.ID, id.TerminalID, id.InstanceID = "other", "other", "other-instance"
				if err := st.TerminalIdentities().Create(ctx, id); err != nil {
					t.Fatal(err)
				}
				if err := st.TerminalIdentities().Delete(ctx, term.ID); err != nil {
					t.Fatal(err)
				}
				id.ID, id.TerminalID = "other2", "other2"
				if err := st.TerminalIdentities().Create(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "linux" {
				img, _ := st.Images().Get(ctx, "img-1")
				img.OSType = domain.OSTypeLinux
				if err := st.Images().Update(ctx, img); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "observed" || mode == "adapted" {
				a := domain.ModelAdaptation{ID: "adapt", ProfileID: p.ID, ConfigID: "cfg-1", ReductionID: "red-1", Source: domain.AdaptationObserved, CreatedAt: now}
				if mode == "adapted" {
					a.Source = domain.AdaptationAdapted
					a.TemplateInstanceID = instance
					a.ClassKey = "0010"
					a.NetCfgInstanceID = "{11111111-2222-3333-4444-555555555555}"
				}
				if err := st.ModelAdaptations().Create(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			host := &fallbackIdentityStorage{fakeStorage: &fakeStorage{}}
			ids := &identity.Service{Store: st, Storage: host, NodeID: "writer"}
			if mode == "no_driver" {
				ids.Templates = []identity.TemplateEntry{{Vendor: "15ad", Device: "07b0", Manufacturer: "vmware", Reg: "sample", DriverHWID: `PCI\VEN_15AD&DEV_07B0`}}
			}
			if mode == "installed" {
				host.identityErr = fmt.Errorf("镜像里已装好这台机器的网卡：%s", instance)
			}
			if mode == "generation_failed" {
				host.identityErr = errors.New("网卡模板生成失败：注册表回读不一致")
			}
			service := BootService{Store: st, Storage: host, Identity: ids, Builder: boot.Builder{}}
			blocked := mode == "failed" || mode == "breaker" || mode == "generation_failed"
			for attempt := 0; attempt < 2; attempt++ {
				script, err := service.BuildBootScript(ctx, term.MAC)
				if blocked {
					if !errors.Is(err, identity.ErrAdaptationRequired) || script != "" || len(host.calls) != 0 {
						t.Fatalf("known failure must not export: mode=%s attempt=%d script=%q err=%v calls=%v", mode, attempt, script, err, host.calls)
					}
					view, e := ids.Get(ctx, term.ID)
					if e != nil || view.NextBoot.Kind == "system" {
						t.Fatalf("blocked preview=%+v err=%v", view.NextBoot, e)
					}
				} else if err != nil || !strings.Contains(script, "sanhook") {
					t.Fatalf("native/adapted path blocked: %s script=%q err=%v", mode, script, err)
				}
			}
			if blocked {
				if _, err := st.PESessions().ActiveByTerminal(ctx, term.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("unexpected identity recapture: %v", err)
				}
			}
		})
	}
}

func TestBootLocalIdentityFallbackDoesNotExportAfterGeneratorFailure(t *testing.T) {
	for _, missingSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(missingSnapshot), func(t *testing.T) {
			ctx := context.Background()
			st := seedBootStore(t)
			now := time.Now().UTC()
			p := domain.HardwareProfile{ID: "vmx", Name: "vmxnet3", Fingerprint: "15ad:07b0", Manufacturer: "VMware", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: now, UpdatedAt: now}
			if err := st.HardwareProfiles().Create(ctx, p); err != nil {
				t.Fatal(err)
			}
			term, _ := st.Terminals().Get(ctx, "term-1")
			term.HardwareProfileID = p.ID
			if err := st.Terminals().Update(ctx, term); err != nil {
				t.Fatal(err)
			}
			placed := &fallbackIdentityStorage{fakeStorage: &fakeStorage{}, identityErr: context.DeadlineExceeded}
			if missingSnapshot {
				placed.identityErr = nil
				placed.createErr = errors.New("cannot open 'tank/nd/win_default@0': dataset does not exist")
			}
			writer := &fallbackIdentityStorage{fakeStorage: &fakeStorage{}, identityErr: errors.New("网卡模板生成失败：注册表回读不一致")}
			ids := &identity.Service{Store: st, Storage: writer, NodeID: "writer"}
			service := BootService{Store: st, Storage: writer, Identity: ids, Builder: boot.Builder{}, Place: &place.Router{Store: st, NodeID: "placed", Local: placed, LocalPortal: "10.9.0.2"}}
			script, err := service.BuildBootScript(ctx, term.MAC)
			if !errors.Is(err, identity.ErrAdaptationRequired) || script != "" || len(writer.calls) != 0 || writer.identityCalls != 1 {
				t.Fatalf("local fallback exported unusable disk: script=%q err=%v creates=%v identities=%d", script, err, writer.calls, writer.identityCalls)
			}
			wantPlaced := 0
			if missingSnapshot {
				wantPlaced = 1
			}
			if len(placed.calls) != wantPlaced {
				t.Fatalf("placed calls=%v want %d", placed.calls, wantPlaced)
			}
		})
	}
}

func TestBootTransientTemplateInspectionFailureBlocksRawDiskAndRetries(t *testing.T) {
	for _, mode := range []string{"direct", "remote_inspection_fallback", "remote_snapshot_fallback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st := seedBootStore(t)
			now := time.Now().UTC()
			profile := domain.HardwareProfile{ID: "vmx", Name: "vmxnet3", Fingerprint: "15ad:07b0", Manufacturer: "VMware", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: now, UpdatedAt: now}
			if err := st.HardwareProfiles().Create(ctx, profile); err != nil {
				t.Fatal(err)
			}
			term, _ := st.Terminals().Get(ctx, "term-1")
			term.HardwareProfileID = profile.ID
			if err := st.Terminals().Update(ctx, term); err != nil {
				t.Fatal(err)
			}
			transient := errors.New("请检查网卡驱动 sample.sys：临时读取失败")
			writer := &fallbackIdentityStorage{fakeStorage: &fakeStorage{}, identityErr: transient}
			ids := &identity.Service{Store: st, Storage: writer, NodeID: "writer"}
			svc := BootService{Store: st, Storage: writer, Identity: ids, Builder: boot.Builder{}}
			var placed *fallbackIdentityStorage
			if mode != "direct" {
				placed = &fallbackIdentityStorage{fakeStorage: &fakeStorage{}}
				if mode == "remote_inspection_fallback" {
					placed.identityErr = transient
				} else {
					placed.createErr = errors.New("cannot open 'tank/nd/win_default@0': dataset does not exist")
				}
				svc.Place = &place.Router{Store: st, NodeID: "placed", Local: placed, LocalPortal: "10.9.0.2"}
			}
			for attempt := 0; attempt < 2; attempt++ {
				script, err := svc.BuildBootScript(ctx, term.MAC)
				if !errors.Is(err, identity.ErrAdaptationRequired) || script != "" || len(writer.calls) != 0 || !strings.Contains(err.Error(), "sample.sys") {
					t.Fatalf("attempt%d must block before raw disk export: script=%q err=%v calls=%v", attempt, script, err, writer.calls)
				}
				if writer.identityCalls != attempt+1 {
					t.Fatalf("transient error was cached or remote fallback skipped: calls=%d", writer.identityCalls)
				}
			}
			writer.identityErr = nil
			script, err := svc.BuildBootScript(ctx, term.MAC)
			if err != nil || !strings.Contains(script, "sanhook") || writer.identityCalls != 3 || !writer.req.System.IdentityLayer {
				t.Fatalf("recovered inspection did not retry and export identity: script=%q err=%v calls=%d req=%+v", script, err, writer.identityCalls, writer.req.System)
			}
			if placed != nil && mode == "remote_inspection_fallback" && len(placed.calls) != 0 {
				t.Fatalf("failed remote inspection exported a raw disk: %v", placed.calls)
			}
		})
	}
}
