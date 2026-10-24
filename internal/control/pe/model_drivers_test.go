package pe

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

func TestUploadedDriverPathsAreSafeForWindowsAndLinux(t *testing.T) {
	for _, tc := range []struct {
		name, output string
	}{
		{`..\Windows\System32\escape.inf`, ""},
		{`nested\..\..\escape.inf`, ""},
		{`.. \Windows\escape.inf`, ""},
		{`C:\Windows\escape.inf`, ""},
		{`\\server\share\escape.inf`, ""},
		{`driver"&whoami&".inf`, ""},
		{`nested\net.inf`, "nested/net.inf"},
		{"nested/net.inf", "nested/net.inf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newSessionFixture(t)
			ctx := context.Background()
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			w, err := zw.Create(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("driver"))
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "pack.zip")
			if err := os.WriteFile(file, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			pack := domain.DriverPack{ID: "pack", Name: "network", Category: domain.DriverCategoryBootCriticalNIC, OSType: domain.OSTypeWindows, Arch: "x64", Signed: true, Status: domain.DriverPackEnabled, HWIDs: []string{`PCI\VEN_8086&DEV_100F`}, StoragePath: file, CreatedAt: svc.now()}
			if err := st.DriverPacks().Create(ctx, pack); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			ids, err := svc.StageUploadedDriver(ctx, pack.HWIDs[0], dir)
			if tc.output == "" {
				if err == nil {
					t.Fatal("unsafe Windows path was accepted into the WinPE copy manifest")
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatalf("invalid archive entry must not be staged: %v", entries)
				}
				return
			}
			if err != nil || len(ids) != 1 || ids[0] != pack.ID {
				t.Fatalf("valid nested driver must be usable: ids=%v err=%v", ids, err)
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(tc.output))); err != nil {
				t.Fatalf("Windows and ZIP separators must resolve to the same staged path: %v", err)
			}
		})
	}
}
