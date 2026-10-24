package local

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

func TestInstallationDiskSurvivesRebootAndOrdinaryCleanup(t *testing.T) {
	ctx := context.Background()
	z := newFakePool()
	a := New("node-a", z, &fakeExporter{})
	req := storage.InstallationDiskReq{ID: "job-1", MAC: "aa:bb:cc:dd:ee:ff", SizeBytes: 64 << 30}
	first, err := a.PrepareInstallation(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// A process restart must re-export the same volume, not create or erase it.
	a = New("node-a", z, &fakeExporter{})
	second, err := a.PrepareInstallation(ctx, req)
	if err != nil || first.System.VolPath != second.System.VolPath || len(z.volumes) != 1 {
		t.Fatalf("reboot lost installation: %+v %v volumes=%v", second, err, z.volumes)
	}
	if _, err := a.CreateClientLUN(ctx, storage.ClientReq{MAC: req.MAC}); err == nil || !strings.Contains(err.Error(), "安装") {
		t.Fatalf("ordinary boot took installation disk: %v", err)
	}
	if err := a.CleanupClientClones(ctx, req.MAC, nil); err == nil {
		t.Fatal("ordinary cleanup took installation disk")
	}
	if len(z.destroyed) != 0 {
		t.Fatalf("installation was destroyed: %v", z.destroyed)
	}
}

func TestInstallationSaveCanResumeWithoutDestroyingInstalledSystem(t *testing.T) {
	for _, step := range []string{"rename tank/run/INSTALL-", "snapshot tank/nd/iso-test@0", "clone tank/nd/iso-test_default", "snapshot tank/nd/iso-test_default@0"} {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			z := newFakePool()
			a := New("node-a", z, &fakeExporter{})
			if _, err := a.PrepareInstallation(ctx, storage.InstallationDiskReq{ID: "job-1", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30}); err != nil {
				t.Fatal(err)
			}
			z.failOn = step
			req := storage.InstallationSaveReq{ID: "job-1", MAC: "AABBCCDDEEFF", ImageID: "iso-test", Name: "系统镜像", OSType: domain.OSTypeWindows, SizeBytes: 64 << 30}
			if _, err := a.SaveInstallation(ctx, req); err == nil {
				t.Fatal("expected interrupted save")
			}
			if len(z.destroyed) != 0 {
				t.Fatalf("save failure destroyed system: %v", z.destroyed)
			}
			z.failOn = ""
			a = New("node-a", z, &fakeExporter{})
			result, err := a.SaveInstallation(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Image.State == domain.ImageStateNormal || result.Config.DefaultReductionID == nil || result.Reduction.Status == domain.ReductionStatusReady {
				t.Fatalf("unverified candidate became ready: %+v", result)
			}
			if !z.snaps["tank/nd/iso-test_default@0"] {
				t.Fatal("candidate snapshot missing")
			}
		})
	}
}

func TestInstallationCleanupRefusesOnlineAndOldAttempts(t *testing.T) {
	ctx := context.Background()
	z := newFakePool()
	exp := &fakeExporter{}
	a := New("node-a", z, exp)
	req := storage.InstallationDiskReq{ID: "new", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30}
	if _, err := a.PrepareInstallation(ctx, req); err != nil {
		t.Fatal(err)
	}
	exp.sessions = []string{req.MAC}
	if err := a.CleanupInstallation(ctx, req); err == nil {
		t.Fatal("deleted an online disk")
	}
	if err := a.CleanupInstallation(ctx, storage.InstallationDiskReq{ID: "old", MAC: req.MAC}); err != nil {
		t.Fatal(err)
	}
	if len(z.destroyed) != 0 {
		t.Fatalf("wrong attempt/online disk deleted: %v", z.destroyed)
	}
}

func TestInstallationCandidateCleanupOnlyRemovesItsVerificationClone(t *testing.T) {
	for _, verification := range []bool{true, false} {
		t.Run(map[bool]string{true: "verification clone", false: "unrelated ordinary clone"}[verification], func(t *testing.T) {
			ctx := context.Background()
			z := newFakePool()
			req := storage.InstallationCleanupReq{ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "AABBCCDDEEFF"}
			image := z.Dataset(req.ImageID)
			config := z.Dataset(storage.DefaultConfigName(req.ImageID))
			client := z.Dataset(storage.ClientCloneName(req.MAC))
			data := z.Dataset(storage.ClientDataCloneName(req.MAC, 1))
			old := z.Dataset(storage.InstallationDiskName(req.MAC, "old-attempt"))
			z.datasets = []string{image, config, client, data, old}
			z.snaps[image+"@0"], z.snaps[config+"@0"] = true, true
			z.origin[config] = image + "@0"
			z.origin[client] = "tank/nd/ordinary_default@0"
			if verification {
				z.origin[client] = config + "@0"
			}
			a := New("node-a", z, &fakeExporter{})
			if err := a.CleanupInstallationCandidate(ctx, req); err != nil {
				t.Fatal(err)
			}
			if z.present(image) || z.present(config) || z.present(client) == verification || !z.present(data) || !z.present(old) {
				t.Fatalf("cleanup touched unrelated resources or retained candidate: %v", z.datasets)
			}
			if err := a.CleanupInstallationCandidate(ctx, req); err != nil {
				t.Fatalf("retry after lost response: %v", err)
			}
		})
	}
}

func TestInstallationCandidateCleanupRejectsUnsafeDeletion(t *testing.T) {
	for _, mode := range []string{"online", "foreign dependent", "wrong candidate", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			z := newFakePool()
			exp := &fakeExporter{}
			req := storage.InstallationCleanupReq{ID: "install-0123456789abcdef", ImageID: "iso_0123456789abcdef", MAC: "AABBCCDDEEFF"}
			image := z.Dataset(req.ImageID)
			config := z.Dataset(storage.DefaultConfigName(req.ImageID))
			z.datasets = []string{image, config}
			z.origin[config] = image + "@0"
			switch mode {
			case "online":
				exp.sessions = []string{req.MAC}
			case "foreign dependent":
				other := z.Dataset("unrelated_config")
				z.datasets = append(z.datasets, other)
				z.origin[other] = config + "@0"
			case "wrong candidate":
				req.ImageID = "iso_somebody_else"
			case "interrupted":
				z.failOn = "destroy " + image
			}
			a := New("node-a", z, exp)
			if err := a.CleanupInstallationCandidate(ctx, req); err == nil {
				t.Fatal("unsafe or interrupted cleanup reported success")
			}
			if mode == "interrupted" {
				z.failOn = ""
				if err := a.CleanupInstallationCandidate(ctx, req); err != nil || z.present(image) || z.present(config) {
					t.Fatalf("partial cleanup did not resume: %v %v", z.datasets, err)
				}
			} else if len(z.destroyed) != 0 {
				t.Fatalf("rejected cleanup destroyed resources: %v", z.destroyed)
			}
		})
	}
}

// Ubuntu 安装的 ISO 作为同一 target 的只读 LUN 1 挂给客户机：按需读取，不再整张下进客户机内存。
func TestInstallationExportsMediaAsReadOnlySecondLUN(t *testing.T) {
	ctx := context.Background()
	media := filepath.Join(t.TempDir(), "media.iso")
	if err := os.WriteFile(media, []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	exp := &fakeExporter{}
	a := New("node-a", newFakePool(), exp)
	run := &recordingRunner{out: map[string]string{"losetup": "/dev/loop7\n"}}
	a.runner = run
	req := storage.InstallationDiskReq{ID: "job-1", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30, MediaPath: media}
	if _, err := a.PrepareInstallation(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(exp.reqs) != 2 || exp.reqs[0].LUN != 0 || exp.reqs[0].ReadOnly || exp.reqs[1].LUN != 1 || !exp.reqs[1].ReadOnly || exp.reqs[1].VolPath != "/dev/loop7" {
		t.Fatalf("exports = %+v", exp.reqs)
	}
	// loop 交给内核：LIO 一释放就自动回收，任何拆除路径都不会留下它。
	want := [][]string{{"losetup", "--read-only", "--find", "--show", media}, {"losetup", "--detach", "/dev/loop7"}}
	if !reflect.DeepEqual(run.calls, want) {
		t.Fatalf("losetup calls = %v", run.calls)
	}
	if !reflect.DeepEqual(exp.tornLUNs, []int{0, 1}) {
		t.Fatalf("re-export must clear both LUNs first: %v", exp.tornLUNs)
	}
}

func TestInstallationMediaMustBeOnThisNode(t *testing.T) {
	a := New("node-a", newFakePool(), &fakeExporter{})
	a.runner = &recordingRunner{}
	missing := filepath.Join(t.TempDir(), "media.iso")
	_, err := a.PrepareInstallation(context.Background(), storage.InstallationDiskReq{ID: "job-1", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30, MediaPath: missing})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing media = %v", err)
	}
}

// 保存镜像前拆 target 要连安装介质一起拆，不能把只读 ISO 留在客户机 target 上。
func TestInstallationSaveDropsMediaLUN(t *testing.T) {
	ctx := context.Background()
	exp := &fakeExporter{}
	a := New("node-a", newFakePool(), exp)
	if _, err := a.PrepareInstallation(ctx, storage.InstallationDiskReq{ID: "job-1", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30}); err != nil {
		t.Fatal(err)
	}
	exp.tornLUNs = nil
	if _, err := a.SaveInstallation(ctx, storage.InstallationSaveReq{ID: "job-1", MAC: "AABBCCDDEEFF", ImageID: "iso-test", Name: "系统镜像", OSType: domain.OSTypeLinux, SizeBytes: 64 << 30}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(exp.tornLUNs, []int{0, 1}) {
		t.Fatalf("save teardown LUNs = %v", exp.tornLUNs)
	}
}

// 客户机自己重启来要盘：上一轮 iPXE 的会话还没超时也已作废，和普通开机一样直接重建，不能拒绝。
func TestInstallationRebootIgnoresItsOwnStaleSession(t *testing.T) {
	ctx := context.Background()
	exp := &fakeExporter{sessions: []string{"AABBCCDDEEFF"}}
	a := New("node-a", newFakePool(), exp)
	req := storage.InstallationDiskReq{ID: "job-1", MAC: "AABBCCDDEEFF", SizeBytes: 64 << 30}
	if _, err := a.PrepareInstallation(ctx, req); err == nil {
		t.Fatal("re-exported a disk still in use outside a boot")
	}
	req.Booting = true
	if _, err := a.PrepareInstallation(ctx, req); err != nil {
		t.Fatalf("booting client was refused its own disk: %v", err)
	}
}
