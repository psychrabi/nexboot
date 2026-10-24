package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
	"github.com/tianwei/diskless/internal/winreg"
	"strings"
	"testing"
	"time"
)

func TestAdaptedE1000EUsesTheSameMACRuleAsGeneratedTemplates(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	p, err := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
	if err != nil {
		t.Fatal(err)
	}
	p.Manufacturer, p.NICDevice = "VMware, Inc.", "10d3"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	a, err := st.ModelAdaptations().Get(ctx, "model")
	if err != nil {
		t.Fatal(err)
	}
	a.TemplateInstanceID = `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF64E00`
	if err := st.ModelAdaptations().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "automatic_config" {
		t.Fatalf("E1000E preview must skip PE: %+v %v", v.NextBoot, err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if !ready || err != nil {
		t.Fatalf("E1000E must compute instance: %v %v", ready, err)
	}
	if got, want := host.last.InstanceID, `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\005056FFFF2EF65000`; got != want {
		t.Fatalf("instance=%q want %q", got, want)
	}
}

func TestComputedInstanceRequiresKnownCompleteVMwareHardwarePrefix(t *testing.T) {
	for _, tt := range []struct {
		name     string
		p        domain.HardwareProfile
		template string
	}{
		{"different-subsystem", domain.HardwareProfile{Manufacturer: "VMware", NICVendor: "15ad", NICDevice: "07b0"}, `PCI\VEN_15AD&DEV_07B0&SUBSYS_999915AD&REV_01\OLD`},
		{"physical-board", domain.HardwareProfile{Manufacturer: "Intel", NICVendor: "8086", NICDevice: "10d3"}, `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\OLD`},
		{"unknown-nic", domain.HardwareProfile{Manufacturer: "VMware", NICVendor: "8086", NICDevice: "9999"}, `PCI\VEN_8086&DEV_9999\OLD`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if instance, ok := ComputedInstance(tt.p, "0050562EF650", tt.template); ok {
				t.Fatalf("unsafe MAC derivation accepted %s", instance)
			}
		})
	}
}

func captureNow(t *testing.T, s *Service, st *store.SQLStore, terminal domain.Terminal, host *identityHost, instance string) string {
	t.Helper()
	ctx := context.Background()
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); ready || !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("capture start=%v %v", ready, err)
	}
	session, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"instance_id": instance, "hwid": strings.Join(strings.Split(instance, `\`)[:2], `\`), "probe_note": "keep-original-result"})
	if err := s.PE.Complete(ctx, session.Token, pe.PEResult{OK: true, Stage: "identity", Data: data}); err != nil {
		t.Fatal(err)
	}
	return session.ID
}

func TestOwnCapturedHistorySurvivesPhysicalNICRoundTrip(t *testing.T) {
	for _, adapted := range []bool{true, false} {
		t.Run(fmt.Sprint(adapted), func(t *testing.T) {
			s, st, terminal, host, _ := newCaptureFixture(t)
			ctx := context.Background()
			a, err := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
			if err != nil {
				t.Fatal(err)
			}
			a.BusLoc, a.Platform = "0x300", "efi"
			if err := st.HardwareProfiles().Update(ctx, a); err != nil {
				t.Fatal(err)
			}
			if !adapted {
				if err := st.ModelAdaptations().Delete(ctx, "model"); err != nil {
					t.Fatal(err)
				}
			}
			first := captureNow(t, s, st, terminal, host, slotInstance)
			b := domain.HardwareProfile{ID: "physical-b", Fingerprint: "8086:10d3/0x400/efi", NICVendor: "8086", NICDevice: "10d3", BusLoc: "0x400", Platform: "efi"}
			if err := st.HardwareProfiles().Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			terminal.HardwareProfileID = b.ID
			if err := st.Terminals().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			captureNow(t, s, st, terminal, host, `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\4&123&0&0100`)
			terminal.HardwareProfileID = a.ID
			if err := st.Terminals().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			view, err := s.Get(ctx, terminal.ID)
			if err != nil || view.NextBoot.Kind != "system" {
				t.Fatalf("own captured A must survive A->B->A: %+v %v", view.NextBoot, err)
			}
			if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
				t.Fatalf("history preparation=%v %v", ready, err)
			}
			current, err := st.TerminalIdentities().Get(ctx, terminal.ID)
			if err != nil || current.Source != "captured" || current.InstanceID != slotInstance || current.ProfileID != a.ID {
				t.Fatalf("restored identity=%+v %v", current, err)
			}
			old, err := st.PESessions().Get(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			var result pe.PEResult
			if err := json.Unmarshal([]byte(old.Result), &result); err != nil {
				t.Fatal(err)
			}
			var data map[string]string
			if err := json.Unmarshal(result.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !result.OK || data["probe_note"] != "keep-original-result" {
				t.Fatal("original capture result was changed")
			}
		})
	}
}

func TestDifferentProfileDoesNotInheritFailureStateForTheSameInstance(t *testing.T) {
	s, st, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	old := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "old-profile", InstanceID: slotInstance, Source: "derived", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 2, TemplateGen: "4"}
	if err := st.TerminalIdentities().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	next := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: slotInstance, Source: "captured"}
	if err := s.put(ctx, &next); err != nil {
		t.Fatal(err)
	}
	got, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || got.TemplateFailedRef != "" || got.TemplateStrikes != 0 || got.TemplateBootRef != "" {
		t.Fatalf("failure state crossed profiles: %+v %v", got, err)
	}
}

func TestOwnCapturedHistorySurvivesABorrowedOtherProfile(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	p, err := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
	if err != nil {
		t.Fatal(err)
	}
	p.BusLoc = "0x300"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	captureNow(t, s, st, terminal, host, slotInstance)
	oldID := terminal.HardwareProfileID
	terminal.HardwareProfileID = "borrowed-b"
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, &domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "borrowed-b", InstanceID: `PCI\VEN_8086&DEV_100F\4&different&0&0100`, Source: "model", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	terminal.HardwareProfileID = oldID
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Kind != "system" {
		t.Fatalf("another profile's borrowed identity blocked own history: %+v %v", view.NextBoot, err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("restore own capture=%v %v", ready, err)
	}
}

func TestOwnAdaptationRequiresItsSuccessfulSessionAndMAC(t *testing.T) {
	for _, sameMAC := range []bool{false, true} {
		t.Run(fmt.Sprint(sameMAC), func(t *testing.T) {
			s, st, terminal, host, _ := newCaptureFixture(t)
			ctx := context.Background()
			p, err := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
			if err != nil {
				t.Fatal(err)
			}
			p.BusLoc = "0x300"
			if err := st.HardwareProfiles().Update(ctx, p); err != nil {
				t.Fatal(err)
			}
			a, err := st.ModelAdaptations().Get(ctx, "model")
			if err != nil {
				t.Fatal(err)
			}
			a.TerminalID, a.TemplateInstanceID, a.TaskID = terminal.ID, slotInstance, "adapt-history"
			if err := st.ModelAdaptations().Update(ctx, a); err != nil {
				t.Fatal(err)
			}
			mac := terminal.MAC
			if !sameMAC {
				mac = "0050562EF699"
			}
			data, _ := json.Marshal(pe.NICIdentity{InstanceID: slotInstance})
			result, _ := json.Marshal(pe.PEResult{OK: true, Stage: "adapt", Data: data})
			now := time.Now().UTC()
			if err := st.PESessions().Create(ctx, domain.PESession{ID: "own-adapt", TerminalID: terminal.ID, MAC: mac, Mode: domain.PEModeAdapt, TaskID: a.TaskID, Token: "own-adapt-token", State: domain.PEStateDone, Result: string(result), CreatedAt: now, UpdatedAt: now, ExpiresAt: now}); err != nil {
				t.Fatal(err)
			}
			view, err := s.Get(ctx, terminal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if sameMAC {
				if view.NextBoot.Kind != "system" {
					t.Fatalf("own adaptation not reused: %+v", view.NextBoot)
				}
				if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
					t.Fatalf("own adaptation=%v %v", ready, err)
				}
			} else if view.NextBoot.Kind != "winpe" {
				t.Fatalf("another MAC's adaptation was reused: %+v", view.NextBoot)
			}
		})
	}
}

func TestGeneratedProfileDoesNotReuseAnotherProfilesFailureForTheSameInstance(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	instance, _ := vmwareE1000E().instanceFor(terminal.MAC)
	old := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "other-slot", InstanceID: instance, Source: "derived", TemplateBootRef: "red", TemplateFailedRef: "red", TemplateStrikes: 2, TemplateGen: winreg.GeneratorVersion}
	if err := st.TerminalIdentities().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Reason != "automatic_template" {
		t.Fatalf("another profile blocked new template: %+v %v", view.NextBoot, err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("new profile preparation=%v %v", ready, err)
	}
	got, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || got.TemplateFailedRef != "" || got.TemplateStrikes != 0 {
		t.Fatalf("generated identity inherited another profile's failure: %+v %v", got, err)
	}
}

func TestHistoricalCaptureRequiresOwnExactProfileAndKnownPhysicalPosition(t *testing.T) {
	for _, kind := range []string{"missing-metadata", "other-profile", "other-fingerprint", "other-position", "unknown-position", "other-terminal", "other-mac", "other-hwid", "failed-session", "failed-report"} {
		t.Run(kind, func(t *testing.T) {
			s, st, terminal, _, _ := newCaptureFixture(t)
			ctx := context.Background()
			p, err := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
			if err != nil {
				t.Fatal(err)
			}
			p.BusLoc, p.Platform = "0x300", "efi"
			if kind == "unknown-position" {
				p.BusLoc = ""
			}
			if err := st.HardwareProfiles().Update(ctx, p); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			session := domain.PESession{ID: "historical", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: domain.PEModeIdentity, State: domain.PEStateDone, Token: "history-token", CreatedAt: now, UpdatedAt: now, ExpiresAt: now}
			note := historyProfile(p, slotInstance)
			instance := slotInstance
			switch kind {
			case "other-profile":
				note.ProfileID = "other"
			case "other-fingerprint":
				note.Fingerprint = "other"
			case "other-position":
				note.BusLoc = "0x400"
			case "other-terminal":
				session.TerminalID = "another-terminal"
			case "other-mac":
				session.MAC = "0050562EF699"
			case "other-hwid":
				instance = `PCI\VEN_8086&DEV_10D3&SUBSYS_07D015AD&REV_00\4&123&0&0088`
				note.InstanceID = instance
			case "failed-session":
				session.State = domain.PEStateFailed
			}
			data, _ := json.Marshal(pe.NICIdentity{InstanceID: instance})
			result := captureReport{PEResult: pe.PEResult{OK: kind != "failed-report", Stage: "identity", Data: data}, ServerIdentity: &note}
			if kind == "missing-metadata" {
				result.ServerIdentity = nil
			}
			raw, _ := json.Marshal(result)
			session.Result = string(raw)
			if err := st.PESessions().Create(ctx, session); err != nil {
				t.Fatal(err)
			}
			view, err := s.Get(ctx, terminal.ID)
			if err != nil || view.NextBoot.Kind != "winpe" {
				t.Fatalf("unsafe history must still capture: %+v %v", view.NextBoot, err)
			}
		})
	}
}

type failingArchiveStore struct{ store.Store }
type failingArchiveSessions struct{ store.PESessionRepo }

func (s failingArchiveStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.Tx(ctx, func(tx store.Store) error { return fn(failingArchiveStore{tx}) })
}
func (s failingArchiveStore) PESessions() store.PESessionRepo {
	return failingArchiveSessions{s.Store.PESessions()}
}
func (s failingArchiveSessions) Update(context.Context, domain.PESession) error {
	return errors.New("archive write unavailable")
}

func TestArchiveFailureCannotOverwriteTheOnlyCapturedIdentity(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	first := captureNow(t, s, st, terminal, host, slotInstance)
	before, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldReport, err := st.PESessions().Get(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = failingArchiveStore{st}
	next := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "new-profile", InstanceID: "different", Source: "captured", UpdatedAt: time.Now().UTC()}
	if err := s.put(ctx, &next); err == nil {
		t.Fatal("archive failure must stop replacement")
	}
	after, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || after != before {
		t.Fatalf("captured identity was lost: %+v %v", after, err)
	}
	report, err := st.PESessions().Get(ctx, first)
	if err != nil || report.Result != oldReport.Result {
		t.Fatal("failed archive changed original report")
	}
}

func TestBorrowedInstanceCannotBecomeOwnCaptureThroughAnotherProfilesRow(t *testing.T) {
	s, st, terminal, host := newTemplateFixture(t)
	ctx := context.Background()
	s.Templates = nil
	e1000Model(t, st)
	peer := secondMachine(t, st, terminal)
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: peer.ID, TerminalID: peer.ID, ProfileID: terminal.HardwareProfileID, InstanceID: slotInstance, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: "another-profile", InstanceID: slotInstance, Source: "captured", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); !ready || err != nil {
		t.Fatalf("prepare=%v %v", ready, err)
	}
	got, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || got.Source != "model" {
		t.Fatalf("borrowed instance mislabeled own: %+v %v", got, err)
	}
}
