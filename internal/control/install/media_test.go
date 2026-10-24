package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectorRejectsSourceOutsideImportDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "ubuntu.iso")
	if err := os.WriteFile(outside, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.iso")); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{ImportDir: func(context.Context) (string, error) { return dir, nil }}
	if _, err := inspector.Inspect(context.Background(), filepath.Join(dir, "escape.iso")); err == nil {
		t.Fatal("symlink must not escape the configured import directory")
	}
}

func TestWindowsEditionsAllowVersionsWithCompatibilityWarnings(t *testing.T) {
	for _, build := range []int{26100, 26200} {
		for _, tc := range []struct {
			name, revisionXML, warning string
			revision                   int
		}{
			{"earlier", "<SPBUILD>7704</SPBUILD>", fmt.Sprintf("%d.7704", build), 7704},
			{"fixed", "<SPBUILD>7705</SPBUILD>", "", 7705},
			{"later", "<SPBUILD>8000</SPBUILD>", "", 8000},
			{"missing", "", "无法确认", 0},
			{"zero", "<SPBUILD>0</SPBUILD>", "无法确认", 0},
		} {
			t.Run(fmt.Sprintf("%d/%s", build, tc.name), func(t *testing.T) {
				raw := fmt.Sprintf(`<WIM><IMAGE INDEX="6"><NAME>Windows 11 Pro</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>%d</BUILD>%s</VERSION></WINDOWS></IMAGE></WIM>`, build, tc.revisionXML)
				editions, err := parseWIMEditions([]byte(raw))
				if err != nil || len(editions) != 1 || editions[0].Index != 6 || editions[0].Revision != tc.revision {
					t.Fatalf("valid edition must remain installable: %+v %v", editions, err)
				}
				data, err := json.Marshal(editions)
				if err != nil {
					t.Fatal(err)
				}
				var wire []map[string]any
				if err := json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				warning, _ := wire[0]["warning"].(string)
				if tc.warning == "" {
					if warning != "" {
						t.Fatalf("unexpected compatibility warning: %s", warning)
					}
				} else if !strings.Contains(warning, tc.warning) || !strings.Contains(warning, "可以继续安装") {
					t.Fatalf("expected non-blocking version advisory, got %q", warning)
				}
			})
		}
	}
}

func TestWindowsEditionsKeepEligibleVersionsAndExcludeUnsupportedImages(t *testing.T) {
	raw := `<WIM>
<IMAGE INDEX="1"><NAME>Windows 11 Home</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>26100</BUILD><SPBUILD>7704</SPBUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="6"><NAME>Windows 11 Pro</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>26100</BUILD><SPBUILD>7705</SPBUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="7"><NAME>Windows 11 ARM</NAME><WINDOWS><ARCH>12</ARCH><VERSION><BUILD>26100</BUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="8"><NAME>Windows Server</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>26100</BUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="9"><NAME>Datacenter</NAME><WINDOWS><ARCH>9</ARCH><INSTALLATIONTYPE>Server</INSTALLATIONTYPE><VERSION><BUILD>26100</BUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="10"><NAME>Windows 8.1</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>9600</BUILD></VERSION></WINDOWS></IMAGE>
</WIM>`
	editions, err := parseWIMEditions([]byte(raw))
	if err != nil || len(editions) != 4 || editions[0].Index != 1 || editions[1].Index != 6 || editions[2].Index != 8 || editions[3].Index != 9 {
		t.Fatalf("eligible editions (client and server) must remain selectable: %+v %v", editions, err)
	}
	if editions[1].Core || editions[2].Core || editions[3].Core {
		t.Fatalf("only Server Core editions are marked core: %+v", editions)
	}
}

// Windows Server 可以装；Server Core（无图形界面）要在版本列表里标出来，免得误选。
func TestWindowsServerCoreEditionsAreMarked(t *testing.T) {
	raw := `<WIM>
<IMAGE INDEX="1"><NAME>Windows Server 2022 Standard</NAME><WINDOWS><ARCH>9</ARCH><INSTALLATIONTYPE>Server Core</INSTALLATIONTYPE><VERSION><BUILD>20348</BUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="2"><NAME>Windows Server 2022 Standard (Desktop Experience)</NAME><WINDOWS><ARCH>9</ARCH><INSTALLATIONTYPE>Server</INSTALLATIONTYPE><VERSION><BUILD>20348</BUILD></VERSION></WINDOWS></IMAGE>
</WIM>`
	editions, err := parseWIMEditions([]byte(raw))
	if err != nil || len(editions) != 2 || !editions[0].Core || editions[1].Core {
		t.Fatalf("editions = %+v %v", editions, err)
	}
}

func TestWindowsVersionLabelNamesServerReleases(t *testing.T) {
	for _, tc := range []struct {
		editions []Edition
		want     string
	}{
		{[]Edition{{Name: "Windows 11 Pro", Build: 26200}}, "Windows 11"},
		{[]Edition{{Name: "Windows 10 Pro", Build: 19045}}, "Windows 10"},
		{[]Edition{{Name: "Windows Server 2022 Standard", Build: 20348}}, "Windows Server 2022"},
		{[]Edition{{Name: "Windows Server 2025 Datacenter (Desktop Experience)", Build: 26100}}, "Windows Server 2025"},
		{[]Edition{{Name: "Windows Server 2019 Standard", Build: 17763}}, "Windows Server 2019"},
		{[]Edition{{Name: "Windows Server 2016 Standard", Build: 14393}}, "Windows Server 2016"},
		{[]Edition{{Name: "Windows Server Standard", Build: 99999}}, "Windows Server"},
	} {
		if got := windowsVersionLabel(tc.editions); got != tc.want {
			t.Errorf("%s/%d = %q, want %q", tc.editions[0].Name, tc.editions[0].Build, got, tc.want)
		}
	}
}
