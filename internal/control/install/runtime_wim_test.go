package install

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
)

func TestWindowsPrepareExtractsCaseInsensitiveWIMResources(t *testing.T) {
	for _, windows := range []string{"Windows", "wInDoWs"} {
		t.Run(windows, func(t *testing.T) {
			r, s := windowsPrepareFixture(t, windows, "", true)
			m, err := r.Prepare(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"boot.wim": "winre fixture", "BCD": "bcd fixture", "boot.sdi": "sdi fixture", "curl.exe": "curl fixture"} {
				b, err := os.ReadFile(m.Files[name])
				if err != nil || string(b) != want {
					t.Fatalf("prepared %s = %q, %v; want %q", name, b, err, want)
				}
			}
			if _, err := r.Prepare(context.Background(), s); err != nil {
				t.Fatalf("prepared manifest cannot be reused: %v", err)
			}
		})
	}
}

func TestWindowsPrepareResourceFailures(t *testing.T) {
	for _, tc := range []struct{ name, omit, failure, want string }{
		{"ready", "", "", ""},
		{"missing WinRE", "Winre.wim", "", "Windows/System32/Recovery/winre.wim"},
		{"missing BCD", "BCD", "", "Windows/Boot/DVD/EFI/BCD"},
		{"missing tool", "", "tool", "请在镜像所在服务器安装 wimtools"},
		{"read error", "", "50", "fixture WIM read failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := windowsPrepareFixture(t, "Windows", tc.omit, false)
			if tc.failure == "tool" {
				if err := os.Remove(filepath.Join(os.Getenv("PATH"), "wimlib-imagex")); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("FIXTURE_WIM_FAILURE", tc.failure)
			}
			m, err := r.Prepare(context.Background(), s)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				for name, want := range map[string]string{"boot.wim": "winre fixture", "BCD": "bcd fixture", "boot.sdi": "sdi fixture", "curl.exe": "curl fixture"} {
					data, err := os.ReadFile(m.Files[name])
					if err != nil || string(data) != want {
						t.Fatalf("%s = %q, %v", name, data, err)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if tc.failure != "tool" && strings.Contains(err.Error(), "安装 wimtools") {
				t.Fatalf("installed tool must not be blamed for extraction failure: %v", err)
			}
			if _, err := os.Stat(filepath.Join(m.Dir, "media.json")); !os.IsNotExist(err) {
				t.Fatalf("failed preparation published a manifest: %v", err)
			}
			files, err := filepath.Glob(filepath.Join(m.Dir, ".wim-member-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("partial extraction left private temporary files: %v %v", files, err)
			}
		})
	}
}

// Server Core 版的 install.wim 不带 Windows/Boot/DVD：启动文件和版本无关，退回用 ISO 自带的。
func TestWindowsPrepareFallsBackToMediaBootFiles(t *testing.T) {
	for _, omit := range []string{"BCD", "boot.sdi"} {
		t.Run(omit, func(t *testing.T) {
			r, s := windowsPrepareFixture(t, "Windows", omit, false)
			t.Setenv("FIXTURE_ISO_BOOT", "1")
			m, err := r.Prepare(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"boot.wim": "winre fixture", "BCD": "bcd fixture", "boot.sdi": "sdi fixture"}
			want[omit] = map[string]string{"BCD": "iso bcd", "boot.sdi": "iso sdi"}[omit]
			for name, w := range want {
				if data, err := os.ReadFile(m.Files[name]); err != nil || string(data) != w {
					t.Fatalf("%s = %q, %v; want %q", name, data, err, w)
				}
			}
		})
	}
}

func windowsPrepareFixture(t *testing.T, windows, omitted string, realWIM bool) (Runtime, domain.InstallSession) {
	t.Helper()
	wimlib, err := exec.LookPath("wimlib-imagex")
	if realWIM && err != nil {
		t.Skip("real WIM regression requires wimlib-imagex (wimtools)")
	}
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	for path, data := range map[string]string{
		windows + "/sYsTeM32/Recovery/Winre.wim": "winre fixture",
		windows + "/Boot/DVD/EFI/BCD":            "bcd fixture",
		windows + "/Boot/DVD/PCAT/boot.sdi":      "sdi fixture",
		windows + "/sYsTeM32/curl.exe":           "curl fixture",
	} {
		if strings.HasSuffix(path, omitted) && omitted != "" {
			continue
		}
		p := filepath.Join(tree, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wim := filepath.Join(root, "fixture.wim")
	if realWIM {
		if out, err := exec.Command(wimlib, "capture", tree, wim, "--no-acls").CombinedOutput(); err != nil {
			t.Fatalf("capture fixture: %v: %s", err, out)
		}
	} else if err := os.WriteFile(wim, []byte("fixture WIM"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Stub the ISO container so Prepare can exercise WIM extraction independently.
	// FIXTURE_ISO_BOOT 非空时 ISO 根目录还带介质自己的 BCD 和 boot.sdi（每张官方 Windows ISO 都有）。
	script := "#!/bin/sh\ncase \"$1\" in\nl) printf '%s\\n' '----------' 'Path = sources/install.wim'; [ -n \"$FIXTURE_ISO_BOOT\" ] && printf '%s\\n' '' 'Path = efi/microsoft/boot/bcd' '' 'Path = boot/boot.sdi'; exit 0;;\nx) case \"$5\" in efi/microsoft/boot/bcd) printf 'iso bcd';; boot/boot.sdi) printf 'iso sdi';; *) exec /bin/cat \"$FIXTURE_WIM\";; esac;;\n*) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if realWIM {
		if err := os.Symlink(wimlib, filepath.Join(bin, "wimlib-imagex")); err != nil {
			t.Fatal(err)
		}
	} else {
		script := `#!/bin/sh
test "$WIMLIB_IMAGEX_IGNORE_CASE" = 1 || exit 49
test "$5" = --to-stdout || exit 2
if test -n "$FIXTURE_WIM_FAILURE"; then
  printf 'partial'
  printf 'fixture WIM read failed\n' >&2
  exit "$FIXTURE_WIM_FAILURE"
fi
name=${4##*/}
case "$name" in winre.wim) actual=Winre.wim;; *) actual=$name;; esac
test "$actual" != "$FIXTURE_WIM_OMITTED" || exit 49
case "$name" in
  winre.wim) printf 'winre fixture';;
  BCD) printf 'bcd fixture';;
  boot.sdi) printf 'sdi fixture';;
  curl.exe) printf 'curl fixture';;
  *) exit 49;;
esac
`
		if err := os.WriteFile(filepath.Join(bin, "wimlib-imagex"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("FIXTURE_WIM", wim)
	t.Setenv("FIXTURE_WIM_OMITTED", omitted)
	iso := filepath.Join(root, "fixture.iso")
	data := []byte("fixture ISO container")
	if err := os.WriteFile(iso, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := Runtime{FilesDir: func(context.Context) (string, error) { return root, nil }, ReportBase: "http://192.168.50.1:8080"}
	s := domain.InstallSession{ID: "install-wim-case", Token: "test-token", OSType: domain.OSTypeWindows, SourcePath: iso, SourceSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), EditionIndex: 1}
	return r, s
}
