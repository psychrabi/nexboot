package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

func seedBootModes(t *testing.T, st *store.SQLStore, modes ...string) {
	t.Helper()
	err := st.ImageHealthReports().Create(context.Background(), domain.ImageHealthReport{
		ID: "health-img-1", ImageID: "img-1", Level: domain.HealthOK, BootModes: modes, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func onlyFailure(t *testing.T, st *store.SQLStore) domain.BootFailure {
	t.Helper()
	rows, err := st.BootFailures().List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("failures = %#v err=%v", rows, err)
	}
	return rows[0]
}

func TestRecordBootFailureNamesTheImageItTriedToBoot(t *testing.T) {
	st := seedBootStore(t)
	at := time.Date(2026, 9, 30, 7, 26, 5, 0, time.UTC)
	service := BootService{Store: st, Now: func() time.Time { return at }}

	if err := service.RecordBootFailure(context.Background(), "aa:bb:cc:dd:ee:ff", domain.BootStageSanhook, "1058021379", "pcbios"); err != nil {
		t.Fatal(err)
	}
	got := onlyFailure(t, st)
	want := domain.BootFailure{MAC: "AABBCCDDEEFF", Stage: domain.BootStageSanhook, Code: "0x3f102003", Platform: "pcbios", ImageID: "img-1", At: at}
	if got != want {
		t.Fatalf("failure = %#v, want %#v", got, want)
	}
}

func TestRecordBootFailureRejectsUnknownMachinesAndStages(t *testing.T) {
	service := BootService{Store: seedBootStore(t)}
	if err := service.RecordBootFailure(context.Background(), "112233445566", domain.BootStageSanboot, "", "efi"); !errors.Is(err, ErrUnknownTerminal) {
		t.Fatalf("unknown mac err = %v", err)
	}
	if err := service.RecordBootFailure(context.Background(), "AABBCCDDEEFF", "whatever", "", "efi"); err == nil {
		t.Fatal("unknown stage accepted")
	}
}

func TestSanbootFailureOnAMismatchedImageIsReportedAsBootMode(t *testing.T) {
	st := seedBootStore(t)
	seedBootModes(t, st, domain.BootModeBIOS)
	service := BootService{Store: st}
	if err := service.RecordBootFailure(context.Background(), "AABBCCDDEEFF", domain.BootStageSanboot, "", "efi"); err != nil {
		t.Fatal(err)
	}
	if got := onlyFailure(t, st).Stage; got != domain.BootStageBootMode {
		t.Fatalf("stage = %s", got)
	}
}

func TestCheckBootModeRecordsOnlyAMismatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		modes    []string
		platform string
		want     bool
	}{
		{"UEFI 客户机开仅 BIOS 的镜像", []string{domain.BootModeBIOS}, "efi", true},
		{"BIOS 客户机开仅 UEFI 的镜像", []string{domain.BootModeUEFI}, "pcbios", true},
		{"两种都支持", []string{domain.BootModeBIOS, domain.BootModeUEFI}, "efi", false},
		{"体检没判出来", nil, "efi", false},
		{"Windows 无体检 + BIOS", nil, "pcbios", true},
		{"客户机没报平台", []string{domain.BootModeBIOS}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := seedBootStore(t)
			seedBootModes(t, st, tc.modes...)
			service := BootService{Store: st}
			if err := service.CheckBootMode(context.Background(), "AABBCCDDEEFF", tc.platform); err != nil {
				t.Fatal(err)
			}
			rows, _ := st.BootFailures().List(context.Background())
			if got := len(rows) == 1; got != tc.want {
				t.Fatalf("recorded = %v, want %v (%#v)", got, tc.want, rows)
			}
			if tc.want && rows[0].Stage != domain.BootStageBootMode {
				t.Fatalf("stage = %s", rows[0].Stage)
			}
		})
	}
}

func TestDisklessFirmwareRejectBlocksBIOSWindowsWithoutSanboot(t *testing.T) {
	st := seedBootStore(t)
	service := BootService{Store: st}

	script, reject, err := service.DisklessFirmwareReject(context.Background(), "aa:bb:cc:dd:ee:ff", "pcbios")
	if err != nil || !reject {
		t.Fatalf("reject=%v err=%v", reject, err)
	}
	for _, want := range []string{
		"#!ipxe",
		"Diskless boot blocked",
		"BIOS/Legacy",
		"platform=pcbios",
		"UEFI-only",
		"Booting from SAN device",
		"snponly.efi",
		"undionly.kpxe",
		"Firmware = EFI",
		"Network only",
		"sleep 30",
		"exit",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("prompt missing %q in:\n%s", want, script)
		}
	}
	for _, r := range script {
		if r > 127 {
			t.Fatalf("iPXE prompt must be ASCII-only, found %q (U+%04X)", r, r)
		}
	}
	if rows, _ := st.BootFailures().List(context.Background()); len(rows) != 1 || rows[0].Stage != domain.BootStageBootMode {
		t.Fatalf("failures = %#v", rows)
	}

	// UEFI client must not be rejected.
	if script, reject, err := service.DisklessFirmwareReject(context.Background(), "aa:bb:cc:dd:ee:ff", "efi"); err != nil || reject || script != "" {
		t.Fatalf("efi reject=%v script=%q err=%v", reject, script, err)
	}
}

func TestDisklessFirmwareRejectAllowsBIOSWhenImageListsBIOS(t *testing.T) {
	st := seedBootStore(t)
	seedBootModes(t, st, domain.BootModeBIOS, domain.BootModeUEFI)
	service := BootService{Store: st}
	if _, reject, err := service.DisklessFirmwareReject(context.Background(), "AABBCCDDEEFF", "pcbios"); err != nil || reject {
		t.Fatalf("reject=%v err=%v", reject, err)
	}
}

func TestDisklessFirmwareRejectSkipsLinuxWithoutHealthModes(t *testing.T) {
	st := seedBootStore(t)
	ctx := context.Background()
	img, err := st.Images().Get(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	img.OSType = domain.OSTypeLinux
	if err := st.Images().Update(ctx, img); err != nil {
		t.Fatal(err)
	}
	service := BootService{Store: st}
	if _, reject, err := service.DisklessFirmwareReject(ctx, "AABBCCDDEEFF", "pcbios"); err != nil || reject {
		t.Fatalf("linux without modes must not reject: reject=%v err=%v", reject, err)
	}
}
