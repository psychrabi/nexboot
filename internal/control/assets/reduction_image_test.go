package assets

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/hwprofile"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

type copyingImageStorage struct {
	fakeImageStorage
	copyReq storage.CopyReductionReq
	copyErr error
	onCopy  func(storage.CopyReductionReq)
}

func (s *copyingImageStorage) CopyReductionToImage(_ context.Context, req storage.CopyReductionReq) (storage.ImportImageResult, error) {
	s.copyReq = req
	if s.onCopy != nil {
		s.onCopy(req)
	}
	if s.copyErr != nil {
		return storage.ImportImageResult{}, s.copyErr
	}
	now := time.Now().UTC()
	red := "vmdk2_0"
	return storage.ImportImageResult{
		Image:     domain.Image{ID: "vmdk2", Name: req.Name, OSType: req.OSType, Purpose: req.Purpose, State: domain.ImageStateNormal, Origin: domain.ImageOriginImported, CreatedAt: now},
		Config:    domain.Config{ID: "vmdk2_default", ImageID: "vmdk2", Name: "default", DefaultReductionID: &red, CreatedAt: now},
		Reduction: domain.Reduction{ID: red, ConfigID: "vmdk2_default", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now},
	}, nil
}

func seedRestorePoint(t *testing.T) *store.SQLStore {
	t.Helper()
	return seedRestorePointInto(t, newImageTestStore(t))
}

func seedRestorePointInto(t *testing.T, st *store.SQLStore) *store.SQLStore {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	red := "vmdk_default_r1"
	for _, err := range []error{
		st.Images().Create(ctx, domain.Image{ID: "vmdk", Name: "vmdk", OSType: domain.OSTypeWindows, Purpose: domain.ImagePurposeSystem, State: domain.ImageStateNormal, MountScriptVersion: "v-win", CreatedAt: now}),
		st.Configs().Create(ctx, domain.Config{ID: "vmdk_default", ImageID: "vmdk", Name: "default", DefaultReductionID: &red, CreatedAt: now}),
		st.Reductions().Create(ctx, domain.Reduction{ID: red, ConfigID: "vmdk_default", Name: "@r1", DisplayName: "还原点1", Status: domain.ReductionStatusReady, CreatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestStreamReductionExportsThatRestorePoint(t *testing.T) {
	agent := &copyingImageStorage{}
	svc := ImageService{ImportDir: testImportDir, Store: seedRestorePoint(t), Storage: agent}
	name, err := svc.ReductionExportName(context.Background(), "vmdk_default_r1", false)
	if err != nil || name != "vmdk-还原点1.zfs" {
		t.Fatalf("name = %q err = %v", name, err)
	}
	if err := svc.StreamReduction(context.Background(), "vmdk_default_r1", &bytes.Buffer{}, false); err != nil {
		t.Fatal(err)
	}
	if agent.exportReq.ConfigID != "vmdk_default" || agent.exportReq.Snapshot != "r1" {
		t.Fatalf("export req = %#v", agent.exportReq)
	}
}

func TestSaveReductionAsImageKeepsTheSourcesKind(t *testing.T) {
	ctx := context.Background()
	st := seedRestorePoint(t)
	agent := &copyingImageStorage{}
	svc := ImageService{ImportDir: testImportDir, Store: st, Storage: agent}
	res, err := svc.SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: " 教学机-办公版 "})
	if err != nil {
		t.Fatal(err)
	}
	if agent.copyReq.ConfigID != "vmdk_default" || agent.copyReq.Snapshot != "r1" || agent.copyReq.Name != "教学机-办公版" ||
		agent.copyReq.OSType != domain.OSTypeWindows || agent.copyReq.Purpose != domain.ImagePurposeSystem {
		t.Fatalf("copy req = %#v", agent.copyReq)
	}
	img, err := st.Images().Get(ctx, "vmdk2")
	if err != nil || img.Name != "教学机-办公版" || img.MountScriptVersion != "v-win" {
		t.Fatalf("image = %#v err = %v", img, err)
	}
	task, err := st.Tasks().Get(ctx, res.TaskID)
	if err != nil || task.Type != domain.TaskTypeCopyImage || task.Status != domain.TaskStatusSuccess {
		t.Fatalf("task = %#v err = %v", task, err)
	}
}

func TestSavedAdaptedImagePreservesItsTemplateInANewConfig(t *testing.T) {
	ctx := context.Background()
	st := seedRestorePoint(t)
	now := time.Now().UTC()
	profile := domain.HardwareProfile{ID: "e1000", Name: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100F", CreatedAt: now, UpdatedAt: now}
	source := domain.ModelAdaptation{ID: "adapt-source", ProfileID: profile.ID, ReductionID: "vmdk_default_r1", ConfigID: "vmdk_default", Source: domain.AdaptationAdapted,
		TemplateInstanceID: `PCI\VEN_8086&DEV_100F\4&SOURCE&0&0088`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", DriverPackIDs: []string{"pack-source"}, CreatedAt: now}
	if err := st.HardwareProfiles().Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if err := st.ModelAdaptations().Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: &copyingImageStorage{}}).SaveReductionAsImage(ctx, source.ReductionID, SaveAsImageRequest{Name: "adapted-image"}); err != nil {
		t.Fatal(err)
	}
	// finishImage returns imageID_0 as a real reduction in its default config.
	if _, err := st.Reductions().Get(ctx, "vmdk2_0"); err != nil {
		t.Fatalf("copied image baseline must be a real restore point: %v", err)
	}
	result, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).CreateFromImage(ctx, "vmdk2", CreateConfigRequest{Name: "new-config"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := (hwprofile.Service{Store: st}).Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range view.Adaptations {
		if row.ReductionID == *result.Config.DefaultReductionID {
			if row.Source != domain.AdaptationInherited || row.TemplateInstanceID != source.TemplateInstanceID || row.ClassKey != source.ClassKey || row.NetCfgInstanceID != source.NetCfgInstanceID {
				t.Fatalf("saved image must preserve the full template through config creation: %+v", row)
			}
			return
		}
	}
	t.Fatalf("new config baseline %s has no template after saving the adapted image", *result.Config.DefaultReductionID)
}

// 另存为期间新镜像数据集先于库行出现，任务目标须是新镜像，一致性检查才不会把它当残留。
func TestSaveReductionAsImageRecordsTheNewImageWhileCopying(t *testing.T) {
	ctx := context.Background()
	st := seedRestorePoint(t)
	var midRunRef string
	agent := &copyingImageStorage{onCopy: func(req storage.CopyReductionReq) {
		req.OnTarget("vmdk2")
		req.OnProgress(50, "复制 50%")
		active, err := st.Tasks().ListActive(ctx)
		if err != nil || len(active) != 1 {
			t.Fatalf("active = %#v err = %v", active, err)
		}
		midRunRef = active[0].TargetRef
	}}
	if _, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: agent}).SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "教学机-办公版"}); err != nil {
		t.Fatal(err)
	}
	if midRunRef != "vmdk2" {
		t.Fatalf("TargetRef while copying = %q", midRunRef)
	}
}

func TestSaveReductionAsImageRefusesUpFront(t *testing.T) {
	ctx := context.Background()
	st := seedRestorePoint(t)
	svc := ImageService{ImportDir: testImportDir, Store: st, Storage: &copyingImageStorage{}}
	if _, err := svc.SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "  "}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("blank name err = %v", err)
	}
	if _, err := svc.SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "vmdk"}); !errors.Is(err, ErrImageExists) {
		t.Fatalf("duplicate name err = %v", err)
	}
	tight := &copyingImageStorage{fakeImageStorage: fakeImageStorage{exportSize: 10 << 30, space: &storage.SpaceUsage{PoolAvailable: 1 << 30}}}
	if _, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: tight}).SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "x"}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("no room err = %v", err)
	}
	if _, err := svc.SaveReductionAsImage(ctx, "missing", SaveAsImageRequest{Name: "x"}); !errs.IsNotFound(err) {
		t.Fatalf("unknown restore point err = %v", err)
	}
}

// 另存为在拷贝期间同样没有库行：同名的导入要当场拒绝，并在另存为结束后释放名字。
func TestImportIsRefusedWhileASaveAsOfTheSameNameCopies(t *testing.T) {
	ctx := context.Background()
	st := seedRestorePoint(t)
	entered, release := make(chan struct{}), make(chan struct{})
	agent := &copyingImageStorage{onCopy: func(storage.CopyReductionReq) {
		close(entered)
		<-release
	}}
	res, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: agent, Async: true}).SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "办公版"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	_, err = (ImageService{ImportDir: testImportDir, Store: st, Storage: &fakeImageStorage{}}).ImportImage(ctx, ImportImageRequest{Name: "办公版", SourcePath: "/var/lib/ndiskless/imports/a.zfs", OSType: domain.OSTypeWindows})
	close(release)
	waitTaskDone(t, st, res.TaskID)
	if !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "办公版") || !strings.Contains(err.Error(), "另存为") {
		t.Fatalf("另存为进行中时同名导入应点名拒绝，得到 %v", err)
	}
}

func TestSaveReductionAsImageHoldsSourceUntilAsyncTaskEnds(t *testing.T) {
	for _, operation := range []string{"delete", "overwrite"} {
		for _, copyFails := range []bool{false, true} {
			name := operation + "-success"
			if copyFails {
				name = operation + "-copy-failure"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				st := seedRestorePoint(t)
				now := time.Now().UTC()
				later := domain.Reduction{ID: "vmdk_default_r2", ConfigID: "vmdk_default", Name: "@r2", Status: domain.ReductionStatusReady, CreatedAt: now.Add(time.Minute)}
				if err := st.Reductions().Create(ctx, later); err != nil {
					t.Fatal(err)
				}
				cfg, _ := st.Configs().Get(ctx, later.ConfigID)
				cfg.DefaultReductionID = &later.ID
				if err := st.Configs().Update(ctx, cfg); err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				agent := &copyingImageStorage{onCopy: func(req storage.CopyReductionReq) {
					req.OnTarget("vmdk2") // Task.TargetRef already points at the new image.
					close(entered)
					<-release
				}}
				if copyFails {
					agent.copyErr = errors.New("copy interrupted")
				}
				released := false
				var taskID string
				t.Cleanup(func() {
					if !released {
						close(release)
					}
					if taskID != "" {
						waitTaskDone(t, st, taskID)
					}
				})
				result, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: agent, Async: true}).SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: "copied"})
				if err != nil {
					t.Fatal(err)
				}
				taskID = result.TaskID
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("async copy did not start")
				}
				redDisk, configDisk := &fakeReductionStorage{}, &fakeConfigStorage{}
				changeSource := func() error {
					if operation == "delete" {
						_, err := (ReductionService{Store: st, Storage: redDisk}).Delete(ctx, "vmdk_default_r1")
						return err
					}
					_, err := (ConfigService{Store: st, Storage: configDisk}).OverwriteImage(ctx, "vmdk_default_r1")
					return err
				}
				if err := changeSource(); !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "vmdk") || !strings.Contains(err.Error(), "另存为镜像") || strings.Contains(err.Error(), "copy_image") {
					t.Fatalf("async source change must name the image and active copy in Chinese: %v", err)
				}
				if redDisk.deletedConfig != "" || configDisk.mergeReq.ConfigID != "" {
					t.Fatal("rejected source changes must not touch its disk")
				}
				if _, err := st.Reductions().Get(ctx, "vmdk_default_r1"); err != nil {
					t.Fatalf("copy source disappeared: %v", err)
				}
				close(release)
				released = true
				waitTaskDone(t, st, taskID)
				task, _ := st.Tasks().Get(ctx, taskID)
				wantStatus := domain.TaskStatusSuccess
				if copyFails {
					wantStatus = domain.TaskStatusFailed
				}
				if task.Status != wantStatus {
					t.Fatalf("copy task ended as %s, want %s", task.Status, wantStatus)
				}
				for deadline := time.Now().Add(time.Second); ; {
					err := changeSource()
					if err == nil {
						break
					}
					if !strings.Contains(err.Error(), "另存为镜像") || time.Now().After(deadline) {
						t.Fatalf("completed copy must release its source: %v", err)
					}
					time.Sleep(10 * time.Millisecond)
				}
			})
		}
	}
}

type failedCopyTaskStore struct{ store.Store }

func (s failedCopyTaskStore) Tasks() store.TaskRepo {
	return failedCopyTasks{s.Store.Tasks()}
}

type failedCopyTasks struct{ store.TaskRepo }

func (failedCopyTasks) Create(context.Context, domain.Task) error {
	return errors.New("copy task create interrupted")
}

func TestSaveReductionAsImagePreparationFailuresReleaseBothClaims(t *testing.T) {
	for _, failure := range []string{"duplicate-target", "no-space", "task-create"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			st := seedRestorePoint(t)
			now := time.Now().UTC()
			later := domain.Reduction{ID: "vmdk_default_r2", ConfigID: "vmdk_default", Name: "@r2", Status: domain.ReductionStatusReady, CreatedAt: now.Add(time.Minute)}
			if err := st.Reductions().Create(ctx, later); err != nil {
				t.Fatal(err)
			}
			cfg, _ := st.Configs().Get(ctx, later.ConfigID)
			cfg.DefaultReductionID = &later.ID
			if err := st.Configs().Update(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			agent := &copyingImageStorage{}
			name := "new-target"
			var serviceStore store.Store = st
			switch failure {
			case "duplicate-target":
				name = "vmdk"
			case "no-space":
				agent.exportSize, agent.space = 10<<30, &storage.SpaceUsage{PoolAvailable: 1 << 30}
			case "task-create":
				serviceStore = failedCopyTaskStore{Store: st}
			}
			svc := ImageService{ImportDir: testImportDir, Store: serviceStore, Storage: agent, Async: true}
			if _, err := svc.SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: name}); err == nil {
				t.Fatal("preparation must fail")
			}
			if agent.copyReq.ConfigID != "" {
				t.Fatal("preparation failure must not start the copy")
			}
			if failure == "duplicate-target" {
				if _, err := svc.SaveReductionAsImage(ctx, "vmdk_default_r1", SaveAsImageRequest{Name: name}); !errors.Is(err, ErrImageExists) {
					t.Fatalf("duplicate precheck must release target claim: %v", err)
				}
			} else if _, err := (ImageService{ImportDir: testImportDir, Store: st, Storage: &fakeImageStorage{}}).ImportImage(ctx, ImportImageRequest{Name: name, SourcePath: "/var/lib/ndiskless/imports/a.zfs", OSType: domain.OSTypeWindows}); err != nil {
				t.Fatalf("failed copy preparation must release target name for import: %v", err)
			}
			if _, err := (ReductionService{Store: st, Storage: &fakeReductionStorage{}}).Delete(ctx, "vmdk_default_r1"); err != nil {
				t.Fatalf("failed copy preparation must release its source: %v", err)
			}
		})
	}
}
