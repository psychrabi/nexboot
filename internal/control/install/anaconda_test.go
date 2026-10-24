package install

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func anacondaISOFixture(family, version string) map[string]string {
	files := map[string]string{
		".treeinfo":              "[general]\nfamily = " + family + "\nversion = " + version + "\narch = x86_64\n[stage2]\nmainimage = LiveOS/squashfs.img\n[images-x86_64]\nkernel = images/pxeboot/vmlinuz\ninitrd = images/pxeboot/initrd.img\n",
		"images/pxeboot/vmlinuz": "kernel", "images/pxeboot/initrd.img": "initrd", "LiveOS/squashfs.img": "stage2", "EFI/BOOT/BOOTX64.EFI": "efi", "repodata/repomd.xml": "repo",
	}
	for _, p := range []string{"iscsi-initiator-utils", "dracut-network", "curl", "python3", "grub2-efi-x64", "shim-x64", "openssh-server", "NetworkManager", "grubby"} {
		files["Packages/"+p+"-1.0-1.x86_64.rpm"] = "rpm"
	}
	return files
}

func TestInspectorRecognizesAnacondaDVDAndRejectsIncompleteMedia(t *testing.T) {
	for _, family := range []string{"CentOS", "Red Hat Enterprise Linux", "Rocky Linux", "AlmaLinux"} {
		t.Run(family, func(t *testing.T) {
			files := anacondaISOFixture(family, "7.9")
			meta, err := inspectUbuntuFixture(t, files)
			if err != nil || meta.OSType != domain.OSTypeLinux || !strings.HasPrefix(meta.Distribution, "anaconda-") || meta.Version != "7.9" {
				t.Fatalf("metadata=%+v err=%v", meta, err)
			}
			delete(files, "Packages/iscsi-initiator-utils-1.0-1.x86_64.rpm")
			if _, err = inspectUbuntuFixture(t, files); err == nil || !strings.Contains(err.Error(), "iscsi-initiator-utils") {
				t.Fatalf("incomplete DVD accepted: %v", err)
			}
		})
	}
	for _, invalid := range []struct{ field, value string }{{"arch = x86_64", "arch = aarch64"}, {"version = 7.9", "version = 6.10"}, {"family = CentOS", "family = Fedora"}} {
		files := anacondaISOFixture("CentOS", "7.9")
		files[".treeinfo"] = strings.ReplaceAll(files[".treeinfo"], invalid.field, invalid.value)
		if _, err := inspectUbuntuFixture(t, files); err == nil {
			t.Fatalf("unsupported media accepted: %v", invalid)
		}
	}
}

func TestAnacondaModernDVDRequiresBothOfflineRepositories(t *testing.T) {
	for _, version := range []string{"8.10", "9.4"} {
		files := anacondaISOFixture("Rocky Linux", version)
		if strings.HasPrefix(version, "8") {
			delete(files, "Packages/python3-1.0-1.x86_64.rpm")
			files["AppStream/Packages/p/python36-3.6.8-1.x86_64.rpm"] = "rpm"
		}
		delete(files, "repodata/repomd.xml")
		files["BaseOS/repodata/repomd.xml"] = "repo"
		files["AppStream/repodata/repomd.xml"] = "repo"
		meta, err := inspectUbuntuFixture(t, files)
		if err != nil || meta.Distribution != "anaconda-rocky" {
			t.Fatalf("modern DVD: %+v %v", meta, err)
		}
		delete(files, "AppStream/repodata/repomd.xml")
		if _, err := inspectUbuntuFixture(t, files); err == nil {
			t.Fatal("incomplete modern DVD accepted")
		}
	}
}

func TestAnacondaPrepareAndRenderUseOfflineDVDAndKickstart(t *testing.T) {
	r, s, _ := ubuntuPrepareFixture(t)
	s.Distribution = "anaconda-centos"
	s.Version = "7.9"
	s.Target = "iqn.test:install"
	s.Portal = "192.168.50.1"
	bin := strings.Split(os.Getenv("PATH"), ":")[0]
	stub := "#!/bin/sh\ncase \"$1\" in\nl) printf '%s\\n' '----------' 'Path = images/pxeboot/vmlinuz' 'Path = images/pxeboot/initrd.img';; x) printf '%s' \"$5\";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 17*2048)
	copy(data[16*2048:], []byte("\x01CD001\x01"))
	copy(data[16*2048+40:], []byte("CentOS 7 x86_64                  "))
	if err := os.WriteFile(s.SourcePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.SourceSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ks.cfg", "anaconda-early.py", "anaconda-finish.sh", "iso-startup.py", "nd-datadisks"} {
		if len(m.Content[name]) == 0 {
			t.Errorf("missing %s", name)
		}
	}
	zg, e := gzip.NewReader(bytes.NewReader(m.Content["nd-el7-updates.img"]))
	if e != nil {
		t.Fatal(e)
	}
	archive, e := io.ReadAll(zg)
	zg.Close()
	if e != nil {
		t.Fatal(e)
	}
	entries := readNewc(t, archive)
	// Execute the DVD's real dracut apply step before checking Anaconda's path.
	root := t.TempDir()
	unpack := filepath.Join(root, "updates")
	newroot := filepath.Join(root, "sysroot")
	os.MkdirAll(newroot, 0700)
	for name, entry := range entries {
		if name == "TRAILER!!!" {
			continue
		}
		p := filepath.Join(unpack, name)
		if entry.mode&0o040000 != 0 {
			os.MkdirAll(p, 0755)
		} else {
			os.MkdirAll(filepath.Dir(p), 0755)
			os.WriteFile(p, []byte(entry.data), 0644)
		}
	}
	apply, e := os.ReadFile("testdata/el7-apply-live-updates.sh")
	if e != nil {
		t.Fatal(e)
	}
	script := strings.ReplaceAll(string(apply), "-b /dev/mapper/live-rw", `-d "$NEWROOT"`)
	script = strings.ReplaceAll(script, "/updates", unpack)
	cmd := exec.Command("/bin/sh", "-c", "info() { :; }; mount() { :; }; umount() { :; };\n"+script)
	cmd.Env = append(os.Environ(), "NEWROOT="+newroot, "PATH=/usr/bin:/bin")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("real dracut apply: %v %s", e, out)
	}
	if _, e := os.Stat(filepath.Join(newroot, "run/install/updates/blivet/iscsi.py")); e != nil {
		t.Fatalf("dracut did not install shim where Anaconda loads updates: %v", e)
	}
	if os.Getenv("ND_TEST_EL7_UPDATE_OUT") != "" {
		os.WriteFile(os.Getenv("ND_TEST_EL7_UPDATE_OUT"), m.Content["nd-el7-updates.img"], 0600)
	}

	if len(m.Content["user-data"]) != 0 {
		t.Fatal("Anaconda must not receive Ubuntu autoinstall")
	}
	boot, err := r.Render(context.Background(), s, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inst.updates=" + r.base(s) + "nd-el7-updates.img", "rd.iscsi.firmware=1", "rd.iscsi.ibft=1", "inst.ks=", "inst.repo=cdrom:LABEL=CentOS\\x207\\x20x86_64", "inst.stage2=hd:LABEL=CentOS\\x207\\x20x86_64", "initrd=initrd"} {
		if !strings.Contains(boot, want) {
			t.Errorf("boot lacks %q: %s", want, boot)
		}
	}
	if strings.Count(boot, "sanhook ") != 1 || strings.Contains(boot, "casper") || strings.Contains(boot, "gateway 0.0.0.0") {
		t.Fatalf("bad Linux boot: %s", boot)
	}
	// Cached extraction must still regenerate the compatibility update per request.
	again, e := r.Prepare(context.Background(), s)
	if e != nil || !bytes.Equal(again.Content["nd-el7-updates.img"], m.Content["nd-el7-updates.img"]) {
		t.Fatalf("cached update: %v", e)
	}
	for _, version := range []string{"8.10", "9.4"} {
		modern := s
		modern.Version = version
		mm, e := r.Prepare(context.Background(), modern)
		if e != nil {
			t.Fatal(e)
		}
		bb, e := r.Render(context.Background(), modern, mm)
		if e != nil || strings.Contains(bb, "inst.updates=") || len(mm.Content["nd-el7-updates.img"]) != 0 {
			t.Fatalf("EL%s unexpected update: %s %v", version, bb, e)
		}
	}
	ks := string(m.Content["ks.cfg"])
	for _, want := range []string{"%pre --erroronfail", "%include /tmp/nd-partitions.ks", "%post --nochroot --erroronfail", "iscsi-initiator-utils", "dracut-network", "python3", "user --name=tester"} {
		if !strings.Contains(ks, want) {
			t.Errorf("kickstart lacks %q", want)
		}
	}
	if strings.Contains(ks, s.Password) || strings.Contains(ks, "clearpart --all\n") {
		t.Fatal("unsafe kickstart")
	}
}

func TestAnacondaEarlyScriptProtectsAllNonInstallationDisks(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for installer harness")
	}
	if out, err := exec.Command(python, "testdata/anaconda.py").CombinedOutput(); err != nil {
		t.Fatalf("installer identity: %v\n%s", err, out)
	}
}

// Optional real-media check: runs the same Inspector/Prepare/Render boundaries as production.
func TestAnacondaRealDVD(t *testing.T) {
	iso := os.Getenv("ND_TEST_ANACONDA_ISO")
	if iso == "" {
		t.Skip("set ND_TEST_ANACONDA_ISO for an actual DVD")
	}
	in := Inspector{ImportDir: func(context.Context) (string, error) { return filepath.Dir(iso), nil }}
	meta, err := in.Inspect(context.Background(), iso)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := Runtime{FilesDir: func(context.Context) (string, error) { return root, nil }, ReportBase: "http://192.0.2.1:8080"}
	s := domain.InstallSession{ID: "real-dvd", Token: "test-only", OSType: meta.OSType, Distribution: meta.Distribution, Version: meta.Version, SourcePath: iso, Username: "tester", Password: "Temporary-Test123!", MAC: "001122334455", Target: "iqn.test:install", Portal: "192.0.2.1", SizeBytes: 32 << 30, Attempt: 1}
	m, err := r.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Render(context.Background(), s, m); err != nil {
		t.Fatal(err)
	}
	t.Logf("real DVD: %s %s %s, kernel/initrd and kickstart prepared", meta.Distribution, meta.Version, meta.Architecture)
	if dir := os.Getenv("ND_TEST_ANACONDA_OUTPUT"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range m.Content {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAnacondaFinishPreservesEFIStubsAndUsesFullEFIConfigs(t *testing.T) {
	for _, stub := range []bool{false, true} {
		t.Run(fmt.Sprint(stub), func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "root")
			bin := filepath.Join(dir, "bin")
			record := filepath.Join(dir, "calls")
			for _, d := range []string{"etc", "boot/efi/EFI/rocky", "boot/grub2"} {
				if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
					t.Fatal(err)
				}
			}
			os.Mkdir(bin, 0700)
			cfg := "menuentry linux {}"
			if stub {
				cfg = "search --fs-uuid rootuuid\nset prefix=($dev)/grub2\nconfigfile $prefix/grub.cfg\n"
			}
			for name, data := range map[string]string{"etc/os-release": "VERSION_ID=\"8.10\"\n", "boot/efi/EFI/rocky/grub.cfg": cfg, "boot/efi/EFI/rocky/grubx64.efi": "binary"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, body := range map[string]string{"chroot": "#!/bin/sh\necho \"$*\" >> '" + record + "'\ncat >/dev/null\n", "curl": "#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && : > \"$2\"; shift; done\n"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			b, _ := installScripts.ReadFile("scripts/anaconda-finish.sh")
			script := strings.NewReplacer("/mnt/sysimage", root, "/tmp/", dir+"/", "__BASE__", "http://server/", "__ATTEMPT__", "1").Replace(string(b))
			p := filepath.Join(dir, "finish.sh")
			os.WriteFile(p, []byte(script), 0700)
			cmd := exec.Command("/bin/sh", p)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			if out, err := cmd.CombinedOutput(); err != nil {
				log, _ := os.ReadFile(filepath.Join(dir, "nd-install.log"))
				t.Fatalf("finish: %v %s %s", err, out, log)
			}
			calls, _ := os.ReadFile(record)
			want := "grub2-mkconfig -o /boot/efi/EFI/rocky/grub.cfg"
			if stub {
				want = "grub2-mkconfig -o /boot/grub2/grub.cfg"
			}
			if !strings.Contains(string(calls), want) {
				t.Fatalf("wrong EFI generation: %s", calls)
			}
			if !strings.Contains(string(calls), "dracut --force --regenerate-all") {
				t.Fatal("initrd not rebuilt")
			}
		})
	}
}

func TestAnacondaKickstartUsesVersionedOfflinePackages(t *testing.T) {
	for _, v := range []string{"7", "8.10", "9.4"} {
		s := domain.InstallSession{Version: v, Username: "tester", MAC: "001122334455", Attempt: 1}
		ks, err := AnacondaKickstart(s, "http://192.0.2.1/install/test/", "$6$fixture-hash")
		if err != nil {
			t.Fatal(err)
		}
		want := "\npython3\n"
		if v[0] == '8' {
			want = "\npython36\n"
		}
		if !strings.Contains(string(ks), want) || !strings.Contains(string(ks), "--device=00:11:22:33:44:55") {
			t.Fatalf("wrong interpreter or installer NIC: %s", ks)
		}
		if v[0] != '7' && !strings.Contains(string(ks), "file:///run/install/repo/AppStream") {
			t.Fatal("missing offline AppStream")
		}
		if dir := os.Getenv("ND_TEST_ANACONDA_OUTPUT"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "ks-"+v+".cfg"), ks, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAnacondaFailureUploadsDiagnosticsBeforeReport(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			os.MkdirAll(bin, 0700)
			os.WriteFile(filepath.Join(bin, "timeout"), []byte("#!/bin/sh\nexit 127\n"), 0700)
			for _, n := range []string{"anaconda.log", "storage.log", "program.log"} {
				if !missing {
					os.WriteFile(filepath.Join(dir, n), []byte("original "+n+"\nCHAP authentication failed\nnode.session.auth.password = hidden-chap\n"), 0600)
				}
			}
			sys := filepath.Join(dir, "sys/class/iscsi_session/session1")
			os.MkdirAll(sys, 0700)
			os.WriteFile(filepath.Join(sys, "ifacename"), []byte("ibft0"), 0600)
			os.WriteFile(filepath.Join(bin, "journalctl"), []byte("#!/bin/sh\necho iscsid-login-failed\nexit 1\n"), 0700)
			os.WriteFile(filepath.Join(bin, "iscsiadm"), []byte("#!/bin/sh\ncase \"$*\" in '-m session -P 1'|'-m node'|'-m iface') echo \"$*\"; echo 'password = hidden-chap'; exit 1;; *) exit 99;; esac\n"), 0700)
			os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\nfile=\nfor arg do case \"$arg\" in @*) file=${arg#@};; http*) url=$arg;; esac; done\necho \"${url##*/}\" >> \"$OUT/calls\"\nif [ -n \"$file\" ]; then cp \"$file\" \"$OUT/upload-${url##*/}\"; fi\ncase \"$url\" in */storage.log) exit 22;; esac\n"), 0700)
			ks, err := AnacondaKickstart(domain.InstallSession{Version: "7", Username: "tester", MAC: "001122334455", Attempt: 1}, "http://example/install/test/", "$6$fixture")
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Split(strings.Split(string(ks), "%onerror\n")[1], "%end")[0]
			script = strings.ReplaceAll(script, "/tmp/", dir+"/")
			script = strings.ReplaceAll(script, "/sys/", dir+"/sys/")
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUT="+dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			want := "anaconda.log\nstorage.log\nprogram.log\niscsi.log\nreport\n"
			if missing {
				want = "iscsi.log\nreport\n"
			}
			if string(calls) != want {
				t.Fatalf("calls %q want %q", calls, want)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "upload-iscsi.log"))
			if !strings.Contains(string(b), "iscsid-login-failed") || !strings.Contains(string(b), "ibft0") || !strings.Contains(string(b), "-m session -P 1") || !strings.Contains(string(b), "-m node") || !strings.Contains(string(b), "-m iface") || strings.Contains(string(b), "hidden-chap") {
				t.Fatalf("diagnostic %s", b)
			}
			if !missing {
				b, _ = os.ReadFile(filepath.Join(dir, "upload-storage.log"))
				if strings.Contains(string(b), "hidden-chap") || !strings.Contains(string(b), "CHAP authentication failed") {
					t.Fatalf("redaction %s", b)
				}
			}
		})
	}
}

func TestEL7ExistingSessionCompatibilityUsesActualVendorHandler(t *testing.T) {
	python := os.Getenv("ND_TEST_PYTHON2")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("Python required for vendor handler regression")
		}
	}
	if out, err := exec.Command(python, "testdata/el7-session.py").CombinedOutput(); err != nil {
		t.Fatalf("actual vendor handler: %v\n%s", err, out)
	}
}

func TestAnacondaDiagnosticWatchdogStopsOnlyItsOwnCommand(t *testing.T) {
	ks, err := AnacondaKickstart(domain.InstallSession{Version: "7", Username: "tester", MAC: "001122334455"}, "http://example/install/test/", "$6$fixture")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Split(strings.Split(string(ks), "%onerror\n")[1], "# Read-only snapshot")[0]
	script += `
sleep 30 &
other=$!
trap 'kill "$other" 2>/dev/null; wait "$other" 2>/dev/null' EXIT
nd_readonly sh -c 'echo $$ > "$OUT/child"; exec sleep 30'
status=$?
[ "$status" -ne 0 ] || exit 20
kill -0 "$other" || exit 21
if kill -0 "$(cat "$OUT/child")" 2>/dev/null; then exit 22; fi
nd_readonly sh -c 'exit 3'
[ "$?" -eq 3 ] || exit 23
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "OUT="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("watchdog %v: %s", err, out)
	}
}
