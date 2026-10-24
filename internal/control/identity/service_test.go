package identity

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tianwei/diskless/internal/control/adapt"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/control/place"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVMwareInstanceIsComputedOnlyForVmxnet3(t *testing.T) {
	p := domain.HardwareProfile{Manufacturer: "VMware, Inc.", NICVendor: "15ad", NICDevice: "07b0"}
	got, ok := ComputedInstance(p, "00:50:56:2e:f6:50", `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\OLD`)
	if !ok || got != `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF65000` {
		t.Fatalf("got %q %v", got, ok)
	}
	p.NICVendor = "8086"
	p.NICDevice = "100f"
	if _, ok := ComputedInstance(p, "0050562EF650", `PCI\VEN_8086&DEV_100F\OLD`); ok {
		t.Fatal("e1000 PCI location identity must be measured")
	}
}

type identityHost struct {
	storage.ClientHostAgent
	created int
	deleted int
	last    storage.IdentityLayerReq
	err     error
}

func (h *identityHost) CreateIdentityLayer(_ context.Context, req storage.IdentityLayerReq) error {
	h.created++
	h.last = req
	return h.err
}
func (h *identityHost) DeleteIdentityLayer(context.Context, string) error  { h.deleted++; return nil }
func (h *identityHost) ActiveClientMACs(context.Context) ([]string, error) { return nil, nil }
func newCaptureFixture(t *testing.T) (*Service, *store.SQLStore, domain.Terminal, *identityHost, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Images().Create(ctx, domain.Image{ID: "win", Name: "Win11", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}))
	red := "red"
	must(st.Configs().Create(ctx, domain.Config{ID: "cfg", ImageID: "win", Name: "default", DefaultReductionID: &red, CreatedAt: now}))
	must(st.Reductions().Create(ctx, domain.Reduction{ID: red, ConfigID: "cfg", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}))
	must(st.Groups().Create(ctx, domain.Group{ID: "g", Name: "windows", SystemImageID: "win", SystemConfigID: "cfg", SystemReductionID: red, StartIP: "192.168.10.40", ClientMax: 5, Netmask: "255.255.255.0"}))
	must(st.HardwareProfiles().Create(ctx, domain.HardwareProfile{ID: "e1000", Name: "测试e1000", Fingerprint: "8086:100f", NICVendor: "8086", NICDevice: "100f", CreatedAt: now, UpdatedAt: now}))
	terminal := domain.Terminal{ID: "t", MAC: "0050562EF650", IP: "192.168.10.42", GroupID: "g", HardwareProfileID: "e1000", State: domain.TerminalStateOffline}
	must(st.Terminals().Create(ctx, terminal))
	must(st.ModelAdaptations().Create(ctx, domain.ModelAdaptation{ID: "model", ProfileID: "e1000", ConfigID: "cfg", ReductionID: red, Source: domain.AdaptationAdapted, TemplateInstanceID: `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\OLD`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", TaskID: "old-task", CreatedAt: now}))
	filesDir := t.TempDir()
	must(st.WinPEEnvs().Create(ctx, domain.WinPEEnv{ID: "env", Kind: domain.WinPEEnvImageWinRE, ImageID: "win", Build: 22621, Dir: filesDir, Files: map[string]string{"boot.wim": "x", "boot.sdi": "y", "BCD": "z"}, CreatedAt: now}))
	peService := &pe.SessionService{Store: st, FilesDir: func(context.Context) (string, error) { return filesDir, nil }, ReportBase: "http://192.168.10.4:8080"}
	host := &identityHost{}
	s := &Service{Store: st, PE: peService, Storage: host, NodeID: "a"}
	peService.OnDone = s.OnDone
	return s, st, terminal, host, filesDir
}
func TestNewE1000TerminalAutomaticallyCapturesThenBuildsIdentityLayer(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if ready || !errors.Is(err, ErrCaptureStarted) || host.created != 0 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
	session, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID)
	if err != nil || session.Mode != domain.PEModeIdentity {
		t.Fatalf("session %+v %v", session, err)
	}
	err = s.PE.Complete(ctx, session.Token, pe.PEResult{OK: true, Data: json.RawMessage(`{"instance_id":"PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&123&0&0088","hwid":"PCI\\VEN_8086&DEV_100F"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ready, err = s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready || host.created != 1 {
		t.Fatalf("ready=%v err=%v created=%d", ready, err, host.created)
	}
	identity, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if identity.Source != "captured" || identity.LayerReductionRef != "red" || identity.LayerServerID != "a" {
		t.Fatalf("identity %+v", identity)
	}
}

const captureNICINF = `[Version]
Class=Net
Provider=VMware
CatalogFile=vmxnet3.cat
DriverVer=01/01/2026,1.9.19.0
[Manufacturer]
VMware=Models,NTamd64
[Models.NTamd64]
NIC=Install,PCI\VEN_15AD&DEV_07B0
`

func captureNICZIP(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, data := range map[string]string{"vmxnet3.inf": captureNICINF, "vmxnet3.sys": "MZ-nic-driver", "vmxnet3.cat": "signed-catalog"} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertCaptureNICPayload(t *testing.T, svc *pe.SessionService, sess domain.PESession, terminal domain.Terminal) {
	t.Helper()
	script, err := svc.Script(context.Background(), sess, terminal)
	if err != nil || !strings.Contains(script, "nd-driver-files.txt") {
		t.Fatalf("capture must boot with its uploaded NIC driver manifest: %v\n%s", err, script)
	}
	manifest, err := svc.File(context.Background(), sess.Token, "nd-driver-files.txt", "efi")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"vmxnet3.inf": captureNICINF, "vmxnet3.sys": "MZ-nic-driver", "vmxnet3.cat": "signed-catalog"}
	for i, line := range strings.Split(strings.TrimSpace(string(manifest.Content)), "\r\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid driver manifest: %q", manifest.Content)
		}
		file, err := svc.File(context.Background(), sess.Token, fmt.Sprintf("drivers/%04d", i), "efi")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(file.Path)
		if expected, ok := want[parts[1]]; err != nil || !ok || string(data) != expected {
			t.Fatalf("served %s: %q, %v", parts[1], data, err)
		}
		delete(want, parts[1])
	}
	if len(want) != 0 {
		t.Fatalf("capture omitted required NIC files: %v", want)
	}
}

func TestFirstIdentityCaptureServesUploadedNICWithoutModelTemplate(t *testing.T) {
	s, st, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	profile := domain.HardwareProfile{ID: "vmxnet3", Fingerprint: "15ad:07b0", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: time.Now().UTC()}
	if err := st.HardwareProfiles().Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	terminal.HardwareProfileID = profile.ID
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	drivers := adapt.DriverService{Store: st, Dir: t.TempDir()}
	if _, err := drivers.UploadPack(ctx, adapt.UploadDriverPackRequest{Name: "VMXNET3 x64", Data: captureNICZIP(t)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DriverPacks().Create(ctx, domain.DriverPack{ID: "x86-match", Name: "VMXNET3 x86", Category: domain.DriverCategoryNIC, Version: "99", Arch: "x86", OSType: domain.OSTypeWindows, Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_15AD&DEV_07B0`}, StoragePath: filepath.Join(t.TempDir(), "unsupported-driver.zip"), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	res, err := s.StartCapture(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.PESessions().Get(ctx, res.SessionID)
	if err != nil || sess.State != domain.PEStatePending {
		t.Fatalf("prepared capture must be ready to boot: %+v %v", sess, err)
	}
	assertCaptureNICPayload(t, s.PE, sess, terminal)
}

func TestIdentityCaptureCannotBootUntilDriverPreparationFinishes(t *testing.T) {
	s, st, terminal, _, filesDir := newCaptureFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.PESessions().Create(ctx, domain.PESession{ID: "old", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: domain.PEModeAdapt, TaskID: "old-task", Token: "oldtoken", State: domain.PEStateDone, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	preparing := make(chan domain.PESession, 1)
	allow := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(allow) }) }
	defer release()
	s.PE.FilesDir = func(context.Context) (string, error) {
		if sess, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID); err == nil {
			preparing <- sess
			<-allow
		}
		return filesDir, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.StartCapture(ctx, terminal.ID); done <- err }()
	var sess domain.PESession
	select {
	case sess = <-preparing:
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not reach driver preparation")
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.NextBoot.Kind != "wait" || view.NextBoot.Reason != "preparing" {
		t.Errorf("capture must wait for driver preparation: %+v %v", view.NextBoot, err)
	}
	if _, ready, err := s.PE.Active(ctx, terminal.ID); err == nil || ready {
		t.Errorf("incomplete capture must not be bootable: ready=%v err=%v", ready, err)
	}
	if _, err := s.PE.File(ctx, sess.Token, "ndpe.ini", "efi"); !errs.IsNotFound(err) {
		t.Errorf("preparing token must not serve boot files: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ready, err := s.PE.Active(ctx, terminal.ID); err != nil || !ready {
		t.Fatalf("finished preparation must be bootable: ready=%v err=%v", ready, err)
	}
}

type captureModelDisk struct{ pe.ModelAdaptStorage }

func (captureModelDisk) ActiveClientMACs(context.Context) ([]string, error) { return nil, nil }
func (captureModelDisk) CreateModelAdaptation(context.Context, storage.ModelAdaptReq) (storage.LUNInfo, error) {
	return storage.LUNInfo{Server: "192.168.10.4", Target: "iqn.test:adapt"}, nil
}

func TestPEModesServeNICFromMovedFilesLibrary(t *testing.T) {
	for _, mode := range []string{"identity", "adapt", "probe"} {
		t.Run(mode, func(t *testing.T) {
			s, st, terminal, _, current := newCaptureFixture(t)
			ctx := context.Background()
			profile := domain.HardwareProfile{ID: "vmxnet3", Fingerprint: "15ad:07b0", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: time.Now().UTC()}
			if err := st.HardwareProfiles().Create(ctx, profile); err != nil {
				t.Fatal(err)
			}
			terminal.HardwareProfileID = profile.ID
			if err := st.Terminals().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			s.PE.FilesDir = func(context.Context) (string, error) { return current, nil }
			drivers := adapt.DriverService{Store: st, FilesDir: s.PE.FilesDir}
			data := captureNICZIP(t)
			item, err := drivers.UploadPack(ctx, adapt.UploadDriverPackRequest{Name: "replicated VMXNET3", Data: data})
			if err != nil {
				t.Fatal(err)
			}
			current = t.TempDir()
			canonical := filepath.Join(current, "drivers", "packs", item.Pack.ID+".zip")
			if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(canonical, data, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(item.Pack.StoragePath, []byte("stale-local-copy"), 0644); err != nil {
				t.Fatal(err)
			}
			var res pe.StartResult
			if mode == "identity" {
				res, err = s.StartCapture(ctx, terminal.ID)
			} else if mode == "adapt" {
				model := pe.ModelAdaptService{Store: st, Storage: captureModelDisk{}, PE: s.PE}
				res, err = model.Start(ctx, terminal.ID, pe.ModelAdaptRequest{ConfigID: "cfg", Name: "vmx-current"})
			} else {
				res, err = s.PE.StartProbe(ctx, terminal.ID, pe.ProbeRequest{})
			}
			if err != nil {
				t.Fatalf("%s must prefer the current files library over stale metadata: %v", mode, err)
			}
			sess, err := st.PESessions().Get(ctx, res.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			assertCaptureNICPayload(t, s.PE, sess, terminal)
		})
	}
}

func TestUnreadableSelectedNICPackFailsCaptureWithoutPendingBoot(t *testing.T) {
	for _, cause := range []string{"missing", "corrupt"} {
		t.Run(cause, func(t *testing.T) {
			s, st, terminal, _, _ := newCaptureFixture(t)
			ctx := context.Background()
			file := filepath.Join(t.TempDir(), "selected.zip")
			if cause == "corrupt" {
				if err := os.WriteFile(file, []byte("broken ZIP"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			pack := domain.DriverPack{ID: "selected-nic", Name: "测试启动网卡驱动", Category: domain.DriverCategoryNIC, Version: "1", Arch: "x64", OSType: domain.OSTypeWindows, Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_8086&DEV_100F`}, StoragePath: file, CreatedAt: time.Now().UTC()}
			if err := st.DriverPacks().Create(ctx, pack); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartCapture(ctx, terminal.ID); err == nil || !strings.Contains(err.Error(), pack.Name) {
				t.Errorf("unreadable selected package must name the package: %v", err)
			}
			if _, ready, err := s.PE.Active(ctx, terminal.ID); err != nil || ready {
				t.Fatalf("failed preparation must leave no pending boot: ready=%v err=%v", ready, err)
			}
		})
	}
}

func TestIdentityCaptureDoesNotIgnoreInvalidDriverDirectory(t *testing.T) {
	s, _, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	invalid := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalid, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	s.PE.FilesDir = func(context.Context) (string, error) { return invalid, nil }
	if _, err := s.StartCapture(ctx, terminal.ID); err == nil {
		t.Error("an invalid existing driver directory must not be treated as no matching driver")
	}
	if _, ready, err := s.PE.Active(ctx, terminal.ID); err != nil || ready {
		t.Fatalf("invalid preparation must leave no pending boot: ready=%v err=%v", ready, err)
	}
}

func TestIdentityViewExplainsFirstBootWithoutStartingCapture(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	tasksBefore, err := st.Tasks().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		NextBoot struct {
			Kind        string `json:"kind"`
			Reason      string `json:"reason"`
			ReductionID string `json:"reduction_id"`
		} `json:"next_boot"`
	}
	if err := json.Unmarshal(b, &response); err != nil {
		t.Fatal(err)
	}
	if response.NextBoot.Kind != "winpe" || response.NextBoot.Reason != "automatic_identity" || response.NextBoot.ReductionID != "red" {
		t.Fatalf("first e1000 boot must explain automatic capture: %s", b)
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID); !errs.IsNotFound(err) || host.created != 0 {
		t.Fatalf("view started boot work: err=%v created=%d", err, host.created)
	}
	tasksAfter, err := st.Tasks().List(ctx)
	if err != nil || !reflect.DeepEqual(tasksBefore, tasksAfter) {
		t.Fatalf("GET changed tasks: %v", err)
	}
	terminalAfter, err := st.Terminals().Get(ctx, terminal.ID)
	if err != nil || !reflect.DeepEqual(terminal, terminalAfter) {
		t.Fatalf("GET changed terminal: %+v %v", terminalAfter, err)
	}
	_, err = s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if !errors.Is(err, ErrCaptureStarted) {
		t.Fatalf("view disagrees with actual boot: %v", err)
	}
}

func TestIdentityViewDistinguishesReadyFromServerRebuild(t *testing.T) {
	s, st, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	id := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: `PCI\VEN_8086&DEV_100F\CAPTURED`, Source: "captured", LayerReductionRef: "red", LayerServerID: "a", UpdatedAt: time.Now().UTC()}
	if err := st.TerminalIdentities().Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "direct" || v.Stale {
		t.Fatalf("ready captured client: %+v err=%v", v, err)
	}
	id.LayerReductionRef = "old-point"
	if err := st.TerminalIdentities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	v, err = s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "identity_rebuild" || !v.Stale {
		t.Fatalf("point changed needs server work, not PE: %+v err=%v", v, err)
	}
}

func TestIdentityViewPEPriorityAndExpirationAreReadOnly(t *testing.T) {
	for _, mode := range []domain.PEMode{domain.PEModeAdapt, domain.PEModeIdentity, domain.PEModeProbe} {
		for _, state := range []domain.PESessionState{domain.PEStatePending, domain.PEStateBooted, domain.PEStatePreparing, domain.PEStateFinalizing} {
			for _, expired := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/expired=%v", mode, state, expired), func(t *testing.T) {
					s, st, terminal, host, _ := newCaptureFixture(t)
					ctx := context.Background()
					now := time.Now().UTC()
					expires := now.Add(time.Hour)
					if expired {
						expires = now.Add(-time.Hour)
					}
					sess := domain.PESession{ID: "pending", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: mode, State: state, TaskID: "selected-task", Token: "must-not-expose", ConfigID: "another-config", ReductionID: "another-point", CreatedAt: now, UpdatedAt: now, ExpiresAt: expires}
					if err := st.PESessions().Create(ctx, sess); err != nil {
						t.Fatal(err)
					}
					before, err := st.PESessions().Get(ctx, sess.ID)
					if err != nil {
						t.Fatal(err)
					}
					v, err := s.Get(ctx, terminal.ID)
					if err != nil {
						t.Fatal(err)
					}
					if state == domain.PEStatePreparing || state == domain.PEStateFinalizing {
						if v.NextBoot.Kind != "wait" || v.NextBoot.Reason != string(state) || v.NextBoot.ExpiresAt != nil {
							t.Fatalf("boot is blocked before expiration: %+v", v.NextBoot)
						}
					} else if expired {
						if v.NextBoot.Kind != "winpe" || v.NextBoot.Reason != "automatic_identity" || v.NextBoot.TaskID != "" || v.NextBoot.State != "" {
							t.Fatalf("expired task must not hijack fresh capture: %+v", v.NextBoot)
						}
					} else if v.NextBoot.Kind != "winpe" || v.NextBoot.Mode != mode || v.NextBoot.State != state || v.NextBoot.TaskID != "selected-task" {
						t.Fatalf("active PE takes precedence: %+v", v.NextBoot)
					}
					if !expired || state == domain.PEStatePreparing || state == domain.PEStateFinalizing {
						if v.NextBoot.ConfigID != "another-config" || v.NextBoot.ReductionID != "another-point" {
							t.Fatalf("selected adaptation is not the group current point: %+v", v.NextBoot)
						}
					}
					b, _ := json.Marshal(v)
					if strings.Contains(string(b), "must-not-expose") {
						t.Fatal("view exposed PE token")
					}
					unchanged, err := st.PESessions().Get(ctx, sess.ID)
					if err != nil || !reflect.DeepEqual(before, unchanged) || host.created != 0 || host.deleted != 0 {
						t.Fatalf("GET mutated boot state: %+v err=%v", unchanged, err)
					}
					if _, err := st.TerminalIdentities().Get(ctx, terminal.ID); !errs.IsNotFound(err) {
						t.Fatalf("GET created identity: %v", err)
					}
				})
			}
		}
	}
}

type identityReadFailure struct {
	store.TerminalIdentityRepo
	calls int
}

func (r *identityReadFailure) Get(context.Context, string) (domain.TerminalIdentity, error) {
	r.calls++
	return domain.TerminalIdentity{}, errors.New("identity database read failed")
}

type storeWithIdentityReadFailure struct {
	store.Store
	identities *identityReadFailure
}

func (s storeWithIdentityReadFailure) TerminalIdentities() store.TerminalIdentityRepo {
	return s.identities
}

func TestIncompleteTemplateBootStillSkipsIdentityRepositoryAndViewReportsReadErrors(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	a, err := st.ModelAdaptations().ForProfileReduction(ctx, "e1000", "red")
	if err != nil {
		t.Fatal(err)
	}
	a.ClassKey = ""
	if err := st.ModelAdaptations().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	fault := &identityReadFailure{TerminalIdentityRepo: st.TerminalIdentities()}
	s.Store = storeWithIdentityReadFailure{Store: st, identities: fault}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || ready || fault.calls != 0 || host.created != 0 || host.deleted != 0 {
		t.Fatalf("incomplete template changed ordinary boot: ready=%v calls=%d err=%v", ready, fault.calls, err)
	}
	if _, err := s.Get(ctx, terminal.ID); err == nil || !strings.Contains(err.Error(), "identity database read failed") {
		t.Fatalf("read-only view swallowed database failure: %v", err)
	}
}

func TestIdentityViewIncompleteTemplatesDoNotPromiseAutomaticPE(t *testing.T) {
	for _, missing := range []string{"record", "instance", "class", "guid"} {
		t.Run(missing, func(t *testing.T) {
			s, st, terminal, host, _ := newCaptureFixture(t)
			ctx := context.Background()
			a, err := st.ModelAdaptations().ForProfileReduction(ctx, "e1000", "red")
			if err != nil {
				t.Fatal(err)
			}
			if missing == "record" {
				err = st.ModelAdaptations().Delete(ctx, a.ID)
			} else {
				a.Source = domain.AdaptationObserved
				switch missing {
				case "instance":
					a.TemplateInstanceID = ""
				case "class":
					a.ClassKey = ""
				case "guid":
					a.NetCfgInstanceID = ""
				}
				err = st.ModelAdaptations().Update(ctx, a)
			}
			if err != nil {
				t.Fatal(err)
			}
			v, err := s.Get(ctx, terminal.ID)
			if missing == "record" {
				// 没有适配记录时，这块网卡走模板。实例号未知，先取证。
				if err != nil || v.NextBoot.Kind != "winpe" || v.NextBoot.Reason != "automatic_identity" {
					t.Fatalf("missing record must capture the nic: %+v err=%v", v.NextBoot, err)
				}
				ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
				if ready || !errors.Is(err, ErrCaptureStarted) || host.created != 0 {
					t.Fatalf("missing record boot: ready=%v err=%v created=%d", ready, err, host.created)
				}
				return
			}
			if err != nil || v.NextBoot.Kind != "system" {
				t.Fatalf("missing %s must try Windows directly: %+v err=%v", missing, v, err)
			}
			ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
			if err != nil || ready || host.created != 0 {
				t.Fatalf("view disagrees with boot: ready=%v err=%v", ready, err)
			}
		})
	}
}

func TestIdentityViewUsesEffectiveCurrentPointAndSuperLatestReady(t *testing.T) {
	s, st, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	g, _ := st.Groups().Get(ctx, "g")
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "stale-group-point", Name: "old", ConfigID: "cfg", Status: domain.ReductionStatusReady, CreatedAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	g.SystemReductionID = "stale-group-point"
	if err := st.Groups().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.ReductionID != "red" {
		t.Fatalf("config current point wins: %+v %v", v, err)
	}
	now := time.Now().UTC()
	for _, r := range []domain.Reduction{
		{ID: "latest-ready", Name: "new", ConfigID: "cfg", Status: domain.ReductionStatusReady, CreatedAt: now.Add(time.Hour)},
		{ID: "latest-not-ready", Name: "pending", ConfigID: "cfg", Status: domain.ReductionStatusCreating, CreatedAt: now.Add(2 * time.Hour)},
	} {
		if err := st.Reductions().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	terminal.IsSuper = true
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	v, err = s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "super" || v.NextBoot.ReductionID != "latest-ready" {
		t.Fatalf("super uses latest ready and skips identity: %+v %v", v, err)
	}
}

func TestIdentityViewAdaptationUsesSelectedImageInsteadOfGroupImage(t *testing.T) {
	s, st, terminal, _, _ := newCaptureFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "other-image", Name: "Other Windows", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "other-config", ImageID: "other-image", Name: "Other config", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "other-point", ConfigID: "other-config", Name: "Other point", Status: domain.ReductionStatusReady, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PESessions().Create(ctx, domain.PESession{ID: "pending", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: domain.PEModeAdapt, State: domain.PEStatePending, TaskID: "selected-task", Token: "hidden", ConfigID: "other-config", ReductionID: "other-point", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "winpe" || v.NextBoot.ImageID != "other-image" || v.NextBoot.ConfigID != "other-config" || v.NextBoot.ReductionID != "other-point" {
		t.Fatalf("adaptation target must not inherit group image: %+v err=%v", v, err)
	}
}

func TestIdentityViewVMXNET3ComputesWithoutPEAndLinuxSkipsIdentity(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	p, _ := st.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
	p.Manufacturer, p.NICVendor, p.NICDevice = "VMware, Inc.", "15ad", "07b0"
	if err := st.HardwareProfiles().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	a, err := st.ModelAdaptations().Get(ctx, "model")
	if err != nil {
		t.Fatal(err)
	}
	a.TemplateInstanceID = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\OLD`
	if err := st.ModelAdaptations().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "automatic_config" || host.created != 0 {
		t.Fatalf("VMXNET3 can calculate identity: %+v %v", v, err)
	}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a")
	if err != nil || !ready {
		t.Fatalf("view disagrees with calculated boot: ready=%v err=%v", ready, err)
	}
	img, _ := st.Images().Get(ctx, "win")
	img.OSType = domain.OSTypeLinux
	if err := st.Images().Update(ctx, img); err != nil {
		t.Fatal(err)
	}
	v, err = s.Get(ctx, terminal.ID)
	if err != nil || v.NextBoot.Kind != "system" || v.NextBoot.Reason != "linux" {
		t.Fatalf("Linux skips Windows preparation: %+v %v", v, err)
	}
}
func TestCaptureDriverPreparationFailureLeavesNoPendingSession(t *testing.T) {
	s, st, terminal, _, filesDir := newCaptureFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.PESessions().Create(ctx, domain.PESession{ID: "old", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: domain.PEModeAdapt, TaskID: "old-task", Token: "oldtoken", State: domain.PEStateDone, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filesDir, "pe-sessions", "old")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drivers"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCapture(ctx, terminal.ID); err == nil {
		t.Fatal("invalid driver staging must fail")
	}
	if _, err := st.PESessions().ActiveByTerminal(ctx, terminal.ID); !errs.IsNotFound(err) {
		t.Fatalf("capture remained active: %v", err)
	}
}

func TestIdentityMovesToNewNodeAndCleansOldLayer(t *testing.T) {
	s, st, terminal, old, _ := newCaptureFixture(t)
	ctx := context.Background()
	id := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&123&0&0088`, Source: "captured", LayerReductionRef: "red", LayerServerID: "a", UpdatedAt: time.Now().UTC()}
	if err := st.TerminalIdentities().Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.Place = &place.Router{Store: st, NodeID: "a", Local: old}
	newHost := &identityHost{}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", newHost, "b")
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	got, _ := st.TerminalIdentities().Get(ctx, terminal.ID)
	if newHost.created != 1 || old.deleted != 1 || got.LayerServerID != "b" {
		t.Fatalf("new=%d deleted=%d metadata=%+v", newHost.created, old.deleted, got)
	}
	node := "b"
	terminal.StorageServerID = &node
	if err := st.Terminals().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, terminal.ID)
	if err != nil || view.Stale {
		t.Fatalf("view %+v err %v", view, err)
	}
}

func TestRecaptureRetainsOldLayerForCleanup(t *testing.T) {
	s, st, terminal, old, _ := newCaptureFixture(t)
	ctx := context.Background()
	id := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: "old", Source: "captured", LayerReductionRef: "red", LayerServerID: "a", UpdatedAt: time.Now().UTC()}
	if err := st.TerminalIdentities().Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	err := s.OnDone(ctx, domain.PESession{Mode: domain.PEModeIdentity, TerminalID: terminal.ID}, pe.PEResult{Data: json.RawMessage(`{"instance_id":"PCI\\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\\4&123&0&0088"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || got.LayerServerID != "a" || got.LayerReductionRef != "" {
		t.Fatalf("recaptured identity=%+v err=%v", got, err)
	}
	s.Place = &place.Router{Store: st, NodeID: "a", Local: old}
	ready, err := s.Prepare(ctx, terminal, "cfg", "red", &identityHost{}, "b")
	if err != nil || !ready || old.deleted != 1 {
		t.Fatalf("ready=%v err=%v old layers deleted=%d", ready, err, old.deleted)
	}
}

func TestComputedIdentityTracksCurrentMACInBootAndBackground(t *testing.T) {
	for _, mode := range []string{"boot", "background"} {
		t.Run(mode, func(t *testing.T) {
			s, st, terminal, host, _ := newCaptureFixture(t)
			ctx := context.Background()
			p := domain.HardwareProfile{ID: "vmxnet3", Fingerprint: "vmxnet3", Manufacturer: "VMware, Inc.", NICVendor: "15ad", NICDevice: "07b0", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := st.HardwareProfiles().Create(ctx, p); err != nil {
				t.Fatal(err)
			}
			a, _ := st.ModelAdaptations().Get(ctx, "model")
			a.ProfileID = p.ID
			a.TemplateInstanceID = `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64E00`
			if err := st.ModelAdaptations().Update(ctx, a); err != nil {
				t.Fatal(err)
			}
			terminal.HardwareProfileID = p.ID
			if err := st.Terminals().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
				t.Fatalf("initial identity: ready=%v err=%v", ready, err)
			}
			terminal.MAC = "0050562EF64F"
			if err := st.Terminals().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			if view, err := s.Get(ctx, terminal.ID); err != nil || !view.Stale {
				t.Errorf("old MAC identity must be stale: %+v %v", view, err)
			}
			want := `PCI\VEN_15AD&DEV_07B0&SUBSYS_07B015AD&REV_01\005056FFFF2EF64F00`
			if mode == "boot" {
				if ready, err := s.Prepare(ctx, terminal, "cfg", "red", host, "a"); err != nil || !ready {
					t.Fatalf("new MAC identity: ready=%v err=%v", ready, err)
				}
			} else {
				workerCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() { s.RunRebuilder(workerCtx, time.Millisecond); close(done) }()
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					view, err := s.Get(ctx, terminal.ID)
					if err == nil && !view.Stale && view.Identity.InstanceID == want {
						break
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
				<-done
			}
			if host.last.MAC != terminal.MAC || host.last.InstanceID != want {
				t.Errorf("identity layer must use current MAC: %+v", host.last)
			}
			view, err := s.Get(ctx, terminal.ID)
			if err != nil || view.Stale || view.Identity.InstanceID != want {
				t.Fatalf("rebuilt identity view: %+v %v", view, err)
			}
		})
	}
}

// 旧层在离线节点上：清不掉旧层就记不下新层，后台不该每分钟白建一次。
func TestRebuildWaitsForOfflineOldLayerNode(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	week := time.Now().UTC().Add(-7 * 24 * time.Hour)
	if err := st.Servers().Create(ctx, domain.Server{ID: "gone", Name: "gone", IP: "192.168.10.3", APIURL: "http://192.168.10.3:8080", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp, LastSeenAt: &week}); err != nil {
		t.Fatal(err)
	}
	id := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&123&0&0088`, Source: "captured", LayerServerID: "gone", UpdatedAt: time.Now().UTC()}
	if err := st.TerminalIdentities().Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.Place = &place.Router{Store: st, NodeID: "a", Local: host}
	for i := 0; i < 3; i++ {
		s.rebuild(ctx)
	}
	if host.created != 0 {
		t.Fatalf("old layer node is offline; background rebuilt %d times", host.created)
	}
}

// 旧节点已摘出集群：没有可清的了，照常记下新层，后台不再重复。
func TestPrepareRecordsNewLayerWhenOldNodeWasRemoved(t *testing.T) {
	s, st, terminal, host, _ := newCaptureFixture(t)
	ctx := context.Background()
	id := domain.TerminalIdentity{ID: terminal.ID, TerminalID: terminal.ID, ProfileID: terminal.HardwareProfileID, InstanceID: `PCI\VEN_8086&DEV_100F&SUBSYS_075015AD&REV_01\4&123&0&0088`, Source: "captured", LayerServerID: "removed", UpdatedAt: time.Now().UTC()}
	if err := st.TerminalIdentities().Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.Place = &place.Router{Store: st, NodeID: "a", Local: host}
	s.rebuild(ctx)
	got, err := st.TerminalIdentities().Get(ctx, terminal.ID)
	if err != nil || got.LayerServerID != "a" || got.LayerReductionRef != "red" {
		t.Fatalf("identity=%+v err=%v", got, err)
	}
	s.rebuild(ctx)
	if host.created != 1 {
		t.Fatalf("rebuilt %d times", host.created)
	}
}

// 开机时 ISO 安装排在维护环境和分组系统盘之前，预计下次启动要照实说。
func TestIdentityViewISOInstallTakesOverNextBoot(t *testing.T) {
	for _, phase := range []string{"preparing", "waiting", "installing", "initializing", "installed", "saving", "verifying", "cancelling", "failed"} {
		t.Run(phase, func(t *testing.T) {
			s, st, terminal, _, _ := newCaptureFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			if err := st.PESessions().Create(ctx, domain.PESession{ID: "pe", TerminalID: terminal.ID, MAC: terminal.MAC, Mode: domain.PEModeAdapt, State: domain.PEStatePending, TaskID: "pe-task", Token: "pe-token", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := st.InstallSessions().Create(ctx, domain.InstallSession{ID: "install-1", MAC: terminal.MAC, Token: "install-token", Phase: phase, TaskID: "install-task", ImageID: "iso_1", Name: "win11", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			v, err := s.Get(ctx, terminal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.NextBoot.Kind != "install" || v.NextBoot.Reason != phase || v.NextBoot.TaskID != "install-task" || v.NextBoot.InstallID != "install-1" {
				t.Fatalf("next boot must follow the ISO install: %+v", v.NextBoot)
			}
			if v.NextBoot.ImageID != "" || v.NextBoot.ConfigID != "" || v.NextBoot.ReductionID != "" {
				t.Fatalf("install target is not a catalogue image: %+v", v.NextBoot)
			}
			if b, _ := json.Marshal(v); strings.Contains(string(b), "install-token") {
				t.Fatal("view exposed install token")
			}
		})
	}
	for _, phase := range []string{"ready", "cancelled"} {
		t.Run(phase+" returns to the group", func(t *testing.T) {
			s, st, terminal, _, _ := newCaptureFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			if err := st.InstallSessions().Create(ctx, domain.InstallSession{ID: "install-1", MAC: terminal.MAC, Token: "install-token", Phase: phase, TaskID: "install-task", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			v, err := s.Get(ctx, terminal.ID)
			if err != nil || v.NextBoot.Kind == "install" || v.NextBoot.InstallID != "" {
				t.Fatalf("finished install must not hold the client: %+v err=%v", v.NextBoot, err)
			}
		})
	}
}
