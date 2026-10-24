package assets

import (
	"context"
	"errors"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/hwprofile"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

func TestConfigServiceCreateFromImage(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	image := domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}
	if err := st.Images().Create(ctx, image); err != nil {
		t.Fatal(err)
	}

	storage := &fakeConfigStorage{}
	service := ConfigService{Store: st, Storage: storage}

	result, err := service.CreateFromImage(ctx, image.ID, CreateConfigRequest{Name: "Work 01"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := result.Config
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	if cfg.Name != "Work 01" || cfg.ImageID != image.ID {
		t.Fatalf("cfg = %#v", cfg)
	}
	if cfg.DefaultReductionID == nil || *cfg.DefaultReductionID != cfg.ID+"_0" {
		t.Fatalf("default reduction = %#v", cfg.DefaultReductionID)
	}
	if storage.created != image.ID || storage.name != "Work 01" {
		t.Fatalf("storage call = %#v", storage)
	}
	got, err := st.Configs().Get(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageID != image.ID || got.Name != cfg.Name {
		t.Fatalf("stored cfg = %#v", got)
	}
	task, err := st.Tasks().Get(ctx, result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Type != domain.TaskTypeCreateConfig || task.Status != domain.TaskStatusSuccess || task.Progress != 100 {
		t.Fatalf("task = %#v", task)
	}
}

// 分组经还原点表解析 DefaultReductionID，基线还原点没有行时配置无法绑定也无法启动。
func TestConfigServiceCreateFromImageStoresBaselineReduction(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	result, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).CreateFromImage(ctx, "img-1", CreateConfigRequest{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.Configs().Get(ctx, result.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultReductionID == nil {
		t.Fatal("default reduction is nil")
	}
	red, err := st.Reductions().Get(ctx, *cfg.DefaultReductionID)
	if err != nil {
		t.Fatalf("default reduction %q must resolve: %v", *cfg.DefaultReductionID, err)
	}
	if red.ConfigID != cfg.ID || red.Status != domain.ReductionStatusReady {
		t.Fatalf("reduction = %#v", red)
	}
	list, err := st.Reductions().ListByConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("reductions = %#v", list)
	}
}

func TestConfigServiceCreateFromImageAsync(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	image := domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}
	if err := st.Images().Create(ctx, image); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	started := make(chan struct{})
	storage := &fakeConfigStorage{blockCreate: block, startedCreate: started}
	service := ConfigService{Store: st, Storage: storage, Async: true}

	result, err := service.CreateFromImage(ctx, image.ID, CreateConfigRequest{Name: "Work 01"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async storage did not start")
	}
	if _, err := st.Configs().Get(ctx, "img-1_clone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("config should not be stored before task completes: %v", err)
	}
	close(block)
	waitForTaskStatus(t, st, result.TaskID, domain.TaskStatusSuccess)
	if _, err := st.Configs().Get(ctx, "img-1_clone"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigServiceRejectsDuplicateName(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "work", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	_, err := ConfigService{Store: st, Storage: &fakeConfigStorage{}}.CreateFromImage(ctx, "img-1", CreateConfigRequest{Name: "work"})
	if !errors.Is(err, ErrConfigExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigServiceCreateFromConfigAndVerifyReduction(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	img := domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}
	cfg := domain.Config{ID: "cfg-1", ImageID: img.ID, Name: "default", CreatedAt: time.Now().UTC()}
	red := domain.Reduction{ID: "red-1", ConfigID: cfg.ID, Name: "@0", CreatedAt: time.Now().UTC(), Status: domain.ReductionStatusReady}
	otherCfg := domain.Config{ID: "cfg-2", ImageID: img.ID, Name: "other", CreatedAt: time.Now().UTC()}
	otherRed := domain.Reduction{ID: "red-2", ConfigID: otherCfg.ID, Name: "@0", CreatedAt: time.Now().UTC(), Status: domain.ReductionStatusReady}
	if err := st.Images().Create(ctx, img); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, red); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, otherCfg); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, otherRed); err != nil {
		t.Fatal(err)
	}

	storage := &fakeConfigStorage{}
	service := ConfigService{Store: st, Storage: storage}
	result, err := service.CreateFromConfig(ctx, cfg.ID, CreateConfigFromConfigRequest{Name: "clone", ReductionID: "red-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	if storage.created != cfg.ID || storage.fromReduction == nil || *storage.fromReduction != "0" {
		t.Fatalf("storage call = %#v", storage)
	}

	_, err = service.CreateFromConfig(ctx, cfg.ID, CreateConfigFromConfigRequest{Name: "bad", ReductionID: otherRed.ID})
	if !errors.Is(err, ErrReductionMismatch) {
		t.Fatalf("err = %v", err)
	}
}

// fork 出的配置的基线还原点属于新配置，而不是被克隆的源配置。
func TestConfigServiceForkStoresBaselineReduction(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-1", ConfigID: "cfg-1", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}

	result, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).CreateFromConfig(ctx, "cfg-1", CreateConfigFromConfigRequest{Name: "fork", ReductionID: "red-1"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.Configs().Get(ctx, result.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultReductionID == nil {
		t.Fatal("default reduction is nil")
	}
	red, err := st.Reductions().Get(ctx, *cfg.DefaultReductionID)
	if err != nil {
		t.Fatalf("default reduction %q must resolve: %v", *cfg.DefaultReductionID, err)
	}
	if red.ConfigID != cfg.ID {
		t.Fatalf("baseline reduction belongs to %s, want %s", red.ConfigID, cfg.ID)
	}
}

type failingCatalogueTxStore struct {
	store.Store
	fail bool
}

func (s *failingCatalogueTxStore) Tx(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.Tx(ctx, func(tx store.Store) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.fail {
			s.fail = false
			return errors.New("catalogue transaction interrupted")
		}
		return nil
	})
}

func TestConfigServiceDerivationInheritsOnlyItsSourceTemplate(t *testing.T) {
	for _, tc := range []struct {
		name, sourceReductionID string
		fromImage               bool
		wantInherited, failTx   bool
	}{
		{name: "selected-config-point", sourceReductionID: "red-selected", wantInherited: true},
		{name: "image-does-not-use-config-latest", sourceReductionID: "red-selected", fromImage: true},
		{name: "failed-template-commit-leaves-no-partial-config", sourceReductionID: "red-selected", failTx: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newImageTestStore(t)
			now := time.Now().UTC()
			image := domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}
			selected := domain.Reduction{ID: "red-selected", ConfigID: "cfg-1", Name: "@adapted", Status: domain.ReductionStatusReady, CreatedAt: now}
			latest := domain.Reduction{ID: "red-latest", ConfigID: "cfg-1", Name: "@latest", Status: domain.ReductionStatusReady, CreatedAt: now.Add(time.Hour)}
			cfg := domain.Config{ID: "cfg-1", ImageID: image.ID, Name: "default", DefaultReductionID: &latest.ID, CreatedAt: now}
			profile := domain.HardwareProfile{ID: "e1000", Name: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100F", CreatedAt: now, UpdatedAt: now}
			source := domain.ModelAdaptation{ID: "adapt-source", ProfileID: profile.ID, ReductionID: tc.sourceReductionID, ConfigID: cfg.ID, Source: domain.AdaptationAdapted,
				TemplateInstanceID: `PCI\VEN_8086&DEV_100F\4&SOURCE&0&0088`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", DriverPackIDs: []string{"pack-source"}, CreatedAt: now}
			other := source
			other.ID, other.ReductionID, other.TemplateInstanceID, other.ClassKey, other.DriverPackIDs = "adapt-latest", latest.ID, `PCI\VEN_8086&DEV_100F\4&LATEST&0&0088`, "0034", []string{"pack-latest"}
			for _, err := range []error{st.Images().Create(ctx, image), st.Configs().Create(ctx, cfg), st.Reductions().Create(ctx, selected), st.Reductions().Create(ctx, latest), st.HardwareProfiles().Create(ctx, profile), st.ModelAdaptations().Create(ctx, source), st.ModelAdaptations().Create(ctx, other)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			var serviceStore store.Store = st
			if tc.failTx {
				serviceStore = &failingCatalogueTxStore{Store: st, fail: true}
			}
			disk := &fakeConfigStorage{}
			service := ConfigService{Store: serviceStore, Storage: disk}
			var result ConfigOperationResult
			var err error
			if tc.fromImage {
				result, err = service.CreateFromImage(ctx, image.ID, CreateConfigRequest{Name: "fork"})
			} else {
				result, err = service.CreateFromConfig(ctx, cfg.ID, CreateConfigFromConfigRequest{Name: "fork", ReductionID: selected.ID})
			}
			if tc.failTx {
				if err == nil || disk.deleted != "cfg-1_clone" {
					t.Fatalf("failed commit must return an error and clean the new dataset: error=%v deleted=%s", err, disk.deleted)
				}
				if _, err := st.Configs().Get(ctx, "cfg-1_clone"); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("partial config survived: %v", err)
				}
				if _, err := st.Reductions().Get(ctx, "cfg-1_clone_0"); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("partial baseline survived: %v", err)
				}
				if rows, err := st.ModelAdaptations().ByReduction(ctx, "cfg-1_clone_0"); err != nil || len(rows) != 0 {
					t.Fatalf("partial template survived: %+v error=%v", rows, err)
				}
				unchanged, err := st.ModelAdaptations().Get(ctx, source.ID)
				if err != nil || !reflect.DeepEqual(unchanged, source) {
					t.Fatalf("failed derivation changed its source template: %+v error=%v", unchanged, err)
				}
				stored, _ := st.Configs().Get(ctx, cfg.ID)
				if stored.DefaultReductionID == nil || *stored.DefaultReductionID != latest.ID {
					t.Fatalf("failed derivation changed source current point: %+v", stored)
				}
				if tasks, total, err := st.Tasks().ListPaged(ctx, string(domain.TaskStatusFailed), 10, 0); err != nil || total != 1 || len(tasks) != 1 {
					t.Fatalf("failed derivation must have one failed task: %+v total=%d error=%v", tasks, total, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := (hwprofile.Service{Store: st}).Get(ctx, profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			var inherited []domain.ModelAdaptation
			for _, row := range view.Adaptations {
				if row.ReductionID == *result.Config.DefaultReductionID {
					inherited = append(inherited, row)
				}
			}
			if !tc.wantInherited {
				if len(inherited) != 0 {
					t.Fatalf("image baseline must not inherit unrelated config content: %+v", inherited)
				}
				return
			}
			if len(inherited) != 1 {
				t.Fatalf("derived baseline must expose its source model template: %+v", inherited)
			}
			got := inherited[0]
			if got.Source != domain.AdaptationInherited || got.ConfigID != result.Config.ID || got.TemplateInstanceID != source.TemplateInstanceID || got.ClassKey != source.ClassKey || got.NetCfgInstanceID != source.NetCfgInstanceID || !reflect.DeepEqual(got.DriverPackIDs, source.DriverPackIDs) {
				t.Fatalf("derived model must preserve selected source identity, not the latest: %+v", got)
			}
			unchanged, err := st.ModelAdaptations().Get(ctx, source.ID)
			if err != nil || !reflect.DeepEqual(unchanged, source) {
				t.Fatalf("source template changed: %+v, error=%v", unchanged, err)
			}
			stored, err := st.Configs().Get(ctx, cfg.ID)
			if err != nil || stored.DefaultReductionID == nil || *stored.DefaultReductionID != latest.ID {
				t.Fatalf("source config current point changed: %+v, error=%v", stored, err)
			}
		})
	}
}

func TestConfigServiceRebuiltImagePreservesOnlyItsChosenTemplate(t *testing.T) {
	for _, tc := range []struct {
		name, oldBaselineID         string
		newPoint, overwrite, failTx bool
	}{
		{name: "legacy-config-baseline", oldBaselineID: "cfg-keep_0"},
		{name: "same-image-baseline", oldBaselineID: "img-1_0"},
		{name: "replaces-image-baseline", oldBaselineID: "img-1_0", newPoint: true},
		{name: "overwrite-selected-earlier-point", oldBaselineID: "img-1_0", newPoint: true, overwrite: true},
		{name: "failed-merge-commit-is-atomic-and-retryable", oldBaselineID: "cfg-keep_0", newPoint: true, failTx: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newImageTestStore(t)
			now := time.Now().UTC()
			baseline := domain.Reduction{ID: tc.oldBaselineID, ConfigID: "cfg-keep", Name: "@0", Status: domain.ReductionStatusReady, CreatedAt: now}
			selected := baseline
			if tc.newPoint {
				selected.ID, selected.Name, selected.CreatedAt = "red-selected", "@selected", now.Add(time.Minute)
			}
			currentID := selected.ID
			later := domain.Reduction{ID: "red-later", ConfigID: "cfg-keep", Name: "@later", Status: domain.ReductionStatusReady, CreatedAt: now.Add(2 * time.Minute)}
			if tc.overwrite {
				currentID = later.ID
			}
			image := domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}
			cfg := domain.Config{ID: "cfg-keep", ImageID: image.ID, Name: "keep", DefaultReductionID: &currentID, CreatedAt: now}
			profile := domain.HardwareProfile{ID: "e1000", Name: "e1000", Fingerprint: "e1000", NICVendor: "8086", NICDevice: "100F", CreatedAt: now, UpdatedAt: now}
			for _, err := range []error{st.Images().Create(ctx, image), st.Configs().Create(ctx, cfg), st.Reductions().Create(ctx, baseline), st.HardwareProfiles().Create(ctx, profile)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.newPoint {
				if err := st.Reductions().Create(ctx, selected); err != nil {
					t.Fatal(err)
				}
			}
			if tc.overwrite {
				if err := st.Reductions().Create(ctx, later); err != nil {
					t.Fatal(err)
				}
			}
			chosen := domain.ModelAdaptation{ID: "adapt-selected", ProfileID: profile.ID, ReductionID: selected.ID, ConfigID: cfg.ID, Source: domain.AdaptationAdapted,
				TemplateInstanceID: `PCI\VEN_8086&DEV_100F\4&CHOSEN&0&0088`, ClassKey: "0012", NetCfgInstanceID: "{11111111-2222-3333-4444-555555555555}", CreatedAt: now}
			if err := st.ModelAdaptations().Create(ctx, chosen); err != nil {
				t.Fatal(err)
			}
			for _, red := range []domain.Reduction{baseline, later} {
				if red.ID == selected.ID || red.ID == later.ID && !tc.overwrite {
					continue
				}
				other := chosen
				other.ID, other.ReductionID, other.TemplateInstanceID = "adapt-"+red.ID, red.ID, `PCI\VEN_8086&DEV_100F\4&OTHER&0&0088`
				if err := st.ModelAdaptations().Create(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			group := domain.Group{ID: "g-1", Name: "教室", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: image.ID, SystemConfigID: cfg.ID, SystemReductionID: currentID}
			if err := st.Groups().Create(ctx, group); err != nil {
				t.Fatal(err)
			}
			var serviceStore store.Store = st
			if tc.failTx {
				serviceStore = &failingCatalogueTxStore{Store: st, fail: true}
			}
			service := ConfigService{Store: serviceStore, Storage: &fakeConfigStorage{}}
			var err error
			if tc.overwrite {
				_, err = service.OverwriteImage(ctx, selected.ID)
			} else {
				_, err = service.Merge(ctx, cfg.ID)
			}
			if tc.failTx {
				if err == nil {
					t.Fatal("interrupted merge commit must fail")
				}
				stored, _ := st.Configs().Get(ctx, cfg.ID)
				storedGroup, _ := st.Groups().Get(ctx, group.ID)
				if stored.DefaultReductionID == nil || *stored.DefaultReductionID != currentID || storedGroup.SystemReductionID != currentID {
					t.Fatalf("failed merge changed current references: config=%+v group=%+v", stored, storedGroup)
				}
				for _, red := range []domain.Reduction{baseline, selected} {
					unchanged, err := st.Reductions().Get(ctx, red.ID)
					if err != nil || !reflect.DeepEqual(unchanged, red) {
						t.Fatalf("failed merge changed source point: %+v error=%v", unchanged, err)
					}
				}
				unchanged, sourceErr := st.ModelAdaptations().Get(ctx, chosen.ID)
				if sourceErr != nil || !reflect.DeepEqual(unchanged, chosen) {
					t.Fatalf("failed merge changed source template: %+v error=%v", unchanged, sourceErr)
				}
				if _, err := st.Reductions().Get(ctx, "img-1_0"); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("failed merge left a new baseline: %v", err)
				}
				if rows, err := st.ModelAdaptations().ByReduction(ctx, "img-1_0"); err != nil || len(rows) != 0 {
					t.Fatalf("failed merge left partial inherited templates: %+v error=%v", rows, err)
				}
				if tasks, total, err := st.Tasks().ListPaged(ctx, string(domain.TaskStatusFailed), 10, 0); err != nil || total != 1 || len(tasks) != 1 {
					t.Fatalf("failed merge must have one failed task before retry: %+v total=%d error=%v", tasks, total, err)
				}
				_, err = service.Merge(ctx, cfg.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, _ = st.Configs().Get(ctx, cfg.ID)
			group, _ = st.Groups().Get(ctx, group.ID)
			if cfg.DefaultReductionID == nil || *cfg.DefaultReductionID != "img-1_0" || group.SystemReductionID != "img-1_0" {
				t.Fatalf("rebuilt image must expose and apply its real canonical baseline: config=%+v group=%+v", cfg, group)
			}
			result, err := service.CreateFromImage(ctx, image.ID, CreateConfigRequest{Name: "new-config"})
			if err != nil {
				t.Fatal(err)
			}
			view, err := (hwprofile.Service{Store: st}).Get(ctx, profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range view.Adaptations {
				if row.ReductionID == *result.Config.DefaultReductionID {
					found = true
					if row.Source != domain.AdaptationInherited || row.TemplateInstanceID != chosen.TemplateInstanceID || row.ClassKey != chosen.ClassKey || row.NetCfgInstanceID != chosen.NetCfgInstanceID {
						t.Fatalf("new config inherited the wrong physical source template: %+v", row)
					}
				}
				if _, err := st.Reductions().Get(ctx, row.ReductionID); err != nil {
					t.Fatalf("model history must only refer to real restore points: %+v, error=%v", row, err)
				}
			}
			if !found {
				t.Fatalf("new config baseline %s lost the rebuilt image template", *result.Config.DefaultReductionID)
			}
		})
	}
}

func TestConfigServiceDelete(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cfg := domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: now}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	seedSiblingConfig(t, ctx, st, cfg.ImageID)

	service := ConfigService{Store: st, Storage: &fakeConfigStorage{}}
	result, err := service.Delete(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("delete should succeed: %v", err)
	}
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	if _, err := st.Configs().Get(ctx, cfg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("config should be removed, err=%v", err)
	}
	if _, err := st.Reductions().Get(ctx, "red-own"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reduction should be removed, err=%v", err)
	}

	inUse := &fakeConfigStorage{}
	service = ConfigService{Store: st, Storage: inUse}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-x", ConfigID: cfg.ID, Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "group", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: cfg.ID, SystemReductionID: "red-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, cfg.ID); !errors.Is(err, ErrConfigInUse) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigServiceDeleteAllowsOwnReductions(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cfg := domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "work", CreatedAt: now}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	seedSiblingConfig(t, ctx, st, cfg.ImageID)
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-own", ConfigID: cfg.ID, Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}

	result, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).Delete(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	if _, err := st.Configs().Get(ctx, cfg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("config err = %v", err)
	}
	if _, err := st.Reductions().Get(ctx, "red-own"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reduction err = %v", err)
	}
}

func TestConfigServiceDeleteRejectsGroupDiskAndClientCloneUse(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := seedConfigInUseBase(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := st.GroupDisks().Create(ctx, domain.GroupDisk{ID: "gd-1", GroupID: "g-1", MountTarget: "D", ImageID: "img-1", ConfigID: "cfg-1"}); err != nil {
		t.Fatal(err)
	}
	_, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).Delete(ctx, "cfg-1")
	if !errors.Is(err, ErrConfigInUse) {
		t.Fatalf("group disk err = %v", err)
	}

	st = newImageTestStore(t)
	if err := seedConfigInUseBase(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Servers().Create(ctx, domain.Server{ID: "srv-1", Name: "srv", IP: "10.0.0.2", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.ClientClones().Create(ctx, domain.ClientClone{ID: "clone-1", TerminalMAC: "AA", Kind: domain.CloneKindEphemeral, ConfigID: "cfg-1", ReductionID: "red-1", ServerID: "srv-1", Target: "target", VolPath: "/dev/zvol/tank/c"}); err != nil {
		t.Fatal(err)
	}
	_, err = (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).Delete(ctx, "cfg-1")
	if !errors.Is(err, ErrConfigInUse) {
		t.Fatalf("clone err = %v", err)
	}
}

func TestConfigServiceMergeDeletesSiblingConfigs(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	keep := domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", CreatedAt: now}
	drop := domain.Config{ID: "cfg-drop", ImageID: "img-1", Name: "drop", CreatedAt: now}
	for _, cfg := range []domain.Config{keep, drop} {
		if err := st.Configs().Create(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-" + cfg.ID, ConfigID: cfg.ID, Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
			t.Fatal(err)
		}
	}
	storage := &fakeConfigStorage{}

	result, err := (ConfigService{Store: st, Storage: storage}).Merge(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" {
		t.Fatal("task id is empty")
	}
	if storage.mergeReq.ImageID != "img-1" || storage.mergeReq.ConfigID != keep.ID || len(storage.mergeReq.DeleteConfigIDs) != 1 || storage.mergeReq.DeleteConfigIDs[0] != drop.ID {
		t.Fatalf("merge req = %#v", storage.mergeReq)
	}
	if _, err := st.Configs().Get(ctx, drop.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("drop config err = %v", err)
	}
	if _, err := st.Reductions().Get(ctx, "red-"+drop.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("drop reduction err = %v", err)
	}
	if _, err := st.Configs().Get(ctx, keep.ID); err != nil {
		t.Fatalf("keep config err = %v", err)
	}
}

// 合并后保留配置的历史并成一个基线；旧还原点行指向已不存在的快照，必须删掉，否则会提供无法克隆的还原点。
func TestConfigServiceMergeFoldsKeptReductionsIntoBaseline(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	oldDefault := "red-keep-1"
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", DefaultReductionID: &oldDefault, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-drop", ImageID: "img-1", Name: "drop", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, red := range []domain.Reduction{
		{ID: "red-keep-1", ConfigID: "cfg-keep", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady},
		{ID: "red-keep-2", ConfigID: "cfg-keep", Name: "@r1", CreatedAt: now, Status: domain.ReductionStatusReady},
		{ID: "red-drop", ConfigID: "cfg-drop", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady},
	} {
		if err := st.Reductions().Create(ctx, red); err != nil {
			t.Fatal(err)
		}
	}

	storage := &fakeConfigStorage{}
	if _, err := (ConfigService{Store: st, Storage: storage}).Merge(ctx, "cfg-keep"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storage.mergeReq.ReductionNames, []string{"@0", "@r1"}) {
		t.Fatalf("reduction names = %#v", storage.mergeReq.ReductionNames)
	}
	list, err := st.Reductions().ListByConfig(ctx, "cfg-keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "img-1_0" || list[0].Name != "@0" {
		t.Fatalf("reductions = %#v", list)
	}
	cfg, err := st.Configs().Get(ctx, "cfg-keep")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultReductionID == nil || *cfg.DefaultReductionID != "img-1_0" {
		t.Fatalf("default reduction = %#v", cfg.DefaultReductionID)
	}
	if _, err := st.Reductions().Get(ctx, "red-keep-2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale reduction err = %v", err)
	}
}

// 保留配置通常绑着分组，合并不能因此拒绝；分组的还原点应随之移到新基线。
func TestConfigServiceMergeRepointsGroupsAtTheNewBaseline(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, red := range []domain.Reduction{
		{ID: "cfg-keep_0", ConfigID: "cfg-keep", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady},
		{ID: "red-later", ConfigID: "cfg-keep", Name: "@r1", CreatedAt: now.Add(time.Second), Status: domain.ReductionStatusReady},
	} {
		if err := st.Reductions().Create(ctx, red); err != nil {
			t.Fatal(err)
		}
	}
	// 分组启动的正是合并要并掉的还原点。
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "教室一", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-keep", SystemReductionID: "red-later"}); err != nil {
		t.Fatal(err)
	}
	// 数据盘只绑配置、跟随其默认还原点，既不用改指向，也不应阻塞合并。
	if err := st.GroupDisks().Create(ctx, domain.GroupDisk{ID: "gd-1", GroupID: "g-1", MountTarget: "D", ImageID: "img-1", ConfigID: "cfg-keep"}); err != nil {
		t.Fatal(err)
	}

	if _, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).Merge(ctx, "cfg-keep"); err != nil {
		t.Fatalf("a bound config must still be mergeable: %v", err)
	}
	group, err := st.Groups().Get(ctx, "g-1")
	if err != nil {
		t.Fatal(err)
	}
	if group.SystemReductionID != "img-1_0" {
		t.Fatalf("group reduction = %q, want the new baseline", group.SystemReductionID)
	}
	list, err := st.Reductions().ListByConfig(ctx, "cfg-keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "img-1_0" {
		t.Fatalf("reductions = %#v", list)
	}
}

// 被合并掉的配置仍有绑定时必须拒绝，由操作者手动改指向，不能静默把分组换到别的内容。
func TestConfigServiceMergeRejectsWhenADoomedConfigIsBound(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cfg-keep", "cfg-drop"} {
		if err := st.Configs().Create(ctx, domain.Config{ID: id, ImageID: "img-1", Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-" + id, ConfigID: id, Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "教室二", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-drop", SystemReductionID: "red-cfg-drop"}); err != nil {
		t.Fatal(err)
	}

	storage := &fakeConfigStorage{}
	_, err := (ConfigService{Store: st, Storage: storage}).Merge(ctx, "cfg-keep")
	if !errors.Is(err, ErrConfigInUse) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "cfg-drop") || !strings.Contains(err.Error(), "教室二") {
		t.Fatalf("error should name the config and its user: %v", err)
	}
	if storage.mergeReq.ConfigID != "" {
		t.Fatalf("storage was called: %#v", storage.mergeReq)
	}
}

// 合并会销毁保留配置的快照，而运行中的客户机正克隆自其中之一，必须拒绝。
func TestConfigServiceMergeRejectsWhenAClientIsRunningOnTheKeptConfig(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-keep", ConfigID: "cfg-keep", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "教室三", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-keep", SystemReductionID: "red-keep"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: "AABBCCDDEEFF", IP: "10.0.0.5", GroupID: "g-1", State: domain.TerminalStateUnknown}); err != nil {
		t.Fatal(err)
	}
	if err := st.Servers().Create(ctx, domain.Server{ID: "srv-1", Name: "srv", IP: "10.0.0.2", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := st.ClientClones().Create(ctx, domain.ClientClone{ID: "clone-1", TerminalMAC: "AABBCCDDEEFF", Kind: domain.CloneKindEphemeral, ConfigID: "cfg-keep", ReductionID: "red-keep", ServerID: "srv-1", Target: "t", VolPath: "/dev/zvol/tank/c"}); err != nil {
		t.Fatal(err)
	}

	storage := &fakeConfigStorage{}
	if _, err := (ConfigService{Store: st, Storage: storage}).Merge(ctx, "cfg-keep"); !errors.Is(err, ErrConfigInUse) {
		t.Fatalf("err = %v", err)
	}
	if storage.mergeReq.ConfigID != "" {
		t.Fatalf("storage was called: %#v", storage.mergeReq)
	}
}

// fork 克隆了父配置的快照而数据库不记这层关系；不在提交时拦住，删除会在 ZFS 报
// "volume has dependent clones ... use '-R'"。
func TestConfigServiceDeleteRejectsWhenAForkDependsOnIt(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-fork", ImageID: "img-1", Name: "派生配置", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	storage := &fakeConfigStorage{dependents: map[string][]string{"cfg-1": {"tank/cfg-fork"}}}

	_, err := (ConfigService{Store: st, Storage: storage}).Delete(ctx, "cfg-1")
	if !errors.Is(err, ErrConfigHasDependents) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "派生配置") {
		t.Fatalf("error should name the dependent: %v", err)
	}
	if storage.deleted != "" {
		t.Fatalf("storage was asked to destroy the dataset: %#v", storage)
	}
}

// 外部克隆会阻塞合并，但会先被销毁的兄弟配置不算。
func TestConfigServiceMergeIgnoresSiblingsAsDependents(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cfg-keep", "cfg-drop"} {
		if err := st.Configs().Create(ctx, domain.Config{ID: id, ImageID: "img-1", Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-" + id, ConfigID: id, Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
			t.Fatal(err)
		}
	}

	// cfg-drop 克隆自 cfg-keep，但它会被合并掉，合并照常进行。
	sibling := &fakeConfigStorage{dependents: map[string][]string{"cfg-keep": {"tank/cfg-drop"}}}
	if _, err := (ConfigService{Store: st, Storage: sibling}).Merge(ctx, "cfg-keep"); err != nil {
		t.Fatalf("sibling must not block the merge: %v", err)
	}

	// 已 promote 的合并重试时镜像数据集挂在保留配置下；它会被销毁，不能阻塞重试，否则半途的合并永远完不成。
	stIm := newImageTestStore(t)
	if err := stIm.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := stIm.Configs().Create(ctx, domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := stIm.Reductions().Create(ctx, domain.Reduction{ID: "red-keep", ConfigID: "cfg-keep", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	promoted := &fakeConfigStorage{dependents: map[string][]string{"cfg-keep": {"tank/img-1"}}}
	if _, err := (ConfigService{Store: stIm, Storage: promoted}).Merge(ctx, "cfg-keep"); err != nil {
		t.Fatalf("the image dataset must not block a merge retry: %v", err)
	}

	// 孤儿客户机克隆不属于合并范围，必须阻塞。
	st2 := newImageTestStore(t)
	if err := st2.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st2.Configs().Create(ctx, domain.Config{ID: "cfg-keep", ImageID: "img-1", Name: "keep", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st2.Reductions().Create(ctx, domain.Reduction{ID: "red-keep", ConfigID: "cfg-keep", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		t.Fatal(err)
	}
	orphan := &fakeConfigStorage{dependents: map[string][]string{"cfg-keep": {"tank/CLIENT-AABBCCDDEEFF"}}}
	_, err := (ConfigService{Store: st2, Storage: orphan}).Merge(ctx, "cfg-keep")
	if !errors.Is(err, ErrConfigHasDependents) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "AA:BB:CC:DD:EE:FF") {
		t.Fatalf("error should name the machine holding it: %v", err)
	}
	if orphan.mergeReq.ConfigID != "" {
		t.Fatalf("storage was called: %#v", orphan.mergeReq)
	}
}

func TestConfigServiceList(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	result, err := (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).List(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 || result.Items == nil {
		t.Fatalf("result = %#v", result)
	}

	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	result, err = (ConfigService{Store: st, Storage: &fakeConfigStorage{}}).List(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

type fakeConfigStorage struct {
	storage.StorageAgent
	created       string
	name          string
	fromReduction *string
	deleted       string
	mergeReq      storage.MergeConfigReq
	dependents    map[string][]string
	err           error
	blockCreate   chan struct{}
	startedCreate chan struct{}
}

func (s *fakeConfigStorage) CreateConfig(_ context.Context, sourceID string, name string, reduction *string) (storage.CreateConfigResult, error) {
	if s.startedCreate != nil {
		close(s.startedCreate)
		s.startedCreate = nil
	}
	if s.blockCreate != nil {
		<-s.blockCreate
	}
	if s.err != nil {
		return storage.CreateConfigResult{}, s.err
	}
	s.created = sourceID
	s.name = name
	s.fromReduction = reduction
	cfgID := sourceID + "_clone"
	baseline := domain.Reduction{ID: cfgID + "_0", ConfigID: cfgID, Name: "@0", CreatedAt: time.Now().UTC(), Status: domain.ReductionStatusReady}
	return storage.CreateConfigResult{
		Config:    domain.Config{ID: cfgID, DefaultReductionID: &baseline.ID},
		Reduction: baseline,
	}, nil
}

func (s *fakeConfigStorage) DeleteConfig(_ context.Context, id string) error {
	s.deleted = id
	return s.err
}

func (s *fakeConfigStorage) ConfigDependents(_ context.Context, configID string) ([]string, error) {
	return s.dependents[configID], nil
}

func (s *fakeConfigStorage) MergeConfig(_ context.Context, req storage.MergeConfigReq) (storage.MergeConfigResult, error) {
	s.mergeReq = req
	if s.err != nil {
		return storage.MergeConfigResult{}, s.err
	}
	return storage.MergeConfigResult{Reduction: domain.Reduction{
		ID: req.ImageID + "_0", ConfigID: req.ConfigID, Name: "@0",
		CreatedAt: time.Now().UTC(), Status: domain.ReductionStatusReady,
	}}, nil
}

func seedConfigInUseBase(ctx context.Context, st store.Store, now time.Time) error {
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		return err
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "default", CreatedAt: now}); err != nil {
		return err
	}
	if err := st.Configs().Create(ctx, domain.Config{ID: "cfg-other", ImageID: "img-1", Name: "other", CreatedAt: now}); err != nil {
		return err
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-1", ConfigID: "cfg-1", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		return err
	}
	if err := st.Reductions().Create(ctx, domain.Reduction{ID: "red-other", ConfigID: "cfg-other", Name: "@0", CreatedAt: now, Status: domain.ReductionStatusReady}); err != nil {
		return err
	}
	if err := st.Groups().Create(ctx, domain.Group{ID: "g-1", Name: "group", StartIP: "10.0.0.1", ClientMax: 10, Gateway: "10.0.0.254", Netmask: "255.255.255.0", SystemImageID: "img-1", SystemConfigID: "cfg-other", SystemReductionID: "red-other"}); err != nil {
		return err
	}
	return st.Terminals().Create(ctx, domain.Terminal{ID: "term-1", MAC: "AA", IP: "10.0.0.1", GroupID: "g-1", State: domain.TerminalStateUnknown})
}

func waitForTaskStatus(t *testing.T, st store.Store, taskID string, want domain.TaskStatus) domain.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, err := st.Tasks().Get(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == want {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := st.Tasks().Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task %s status = %s, want %s", taskID, task.Status, want)
	return domain.Task{}
}

// 备机接收该配置时删除必然失败，提交时就拒绝并点名备机；追平按数据集逐个发，只拦正在发的配置。
func TestConfigServiceDeleteRefusedWhileAStandbyReceivesThatConfig(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cfg-1", "cfg-2"} {
		if err := st.Configs().Create(ctx, domain.Config{ID: id, ImageID: "img-1", Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	sending := func(ids ...string) []string {
		for _, id := range ids {
			if id == "cfg-1" {
				return []string{"192.168.10.5"}
			}
		}
		return nil
	}
	service := ConfigService{Store: st, Storage: &fakeConfigStorage{}, Replicating: sending}
	if _, err := service.Delete(ctx, "cfg-1"); !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "192.168.10.5") {
		t.Fatalf("err = %v", err)
	}
	if _, err := service.Delete(ctx, "cfg-2"); err != nil {
		t.Fatalf("an unrelated config was refused: %v", err)
	}
}

func TestConfigServiceDeleteRefusedWhileAStandbyCopiesTheWholeCatalogue(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cfg := domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "work", CreatedAt: now}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	seedSiblingConfig(t, ctx, st, cfg.ImageID)
	fake := &fakeConfigStorage{}
	service := ConfigService{Store: st, Storage: fake, Replicating: func(...string) []string { return []string{"192.168.10.4"} }}

	_, err := service.Delete(ctx, cfg.ID)
	if !errors.Is(err, errs.ErrConflict) || !strings.Contains(err.Error(), "192.168.10.4") {
		t.Fatalf("err = %v，应为点名备机的冲突", err)
	}
	if fake.deleted != "" {
		t.Fatal("被拒时不该动存储")
	}
	if _, err := st.Configs().Get(ctx, cfg.ID); err != nil {
		t.Fatalf("配置应原样保留: %v", err)
	}
}

// 提交后执行中才撞上同步的，任务失败信息也要点名备机。
func TestConfigDeleteTaskExplainsABusyCatalogueHonestly(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	now := time.Now().UTC()
	if err := st.Images().Create(ctx, domain.Image{ID: "img-1", Name: "win", OSType: domain.OSTypeWindows, State: domain.ImageStateNormal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cfg := domain.Config{ID: "cfg-1", ImageID: "img-1", Name: "work", CreatedAt: now}
	if err := st.Configs().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	seedSiblingConfig(t, ctx, st, cfg.ImageID)
	fake := &fakeConfigStorage{err: storage.CommandError{Name: "zfs", Output: "cannot destroy 'tank/nd/cfg-1': dataset is busy"}}
	calls := 0
	service := ConfigService{Store: st, Storage: fake, Replicating: func(...string) []string {
		calls++
		if calls == 1 {
			return nil // 提交那一刻还没开始
		}
		return []string{"192.168.10.4"}
	}}

	if _, err := service.Delete(ctx, cfg.ID); err == nil {
		t.Fatal("存储报占用时任务应失败")
	}
	tasks, _, err := st.Tasks().ListPaged(ctx, "", 10, 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %v, err = %v", tasks, err)
	}
	task := tasks[0]
	if !strings.Contains(task.Error, "192.168.10.4") || strings.Contains(task.Error, "客户机") {
		t.Fatalf("任务失败原因 = %q，应点名备机、不提客户机", task.Error)
	}
}

// seedSiblingConfig 让被删的配置不是镜像的最后一个。
func seedSiblingConfig(t *testing.T, ctx context.Context, st store.Store, imageID string) {
	t.Helper()
	if err := st.Configs().Create(ctx, domain.Config{ID: imageID + "-sibling", ImageID: imageID, Name: "sibling", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
