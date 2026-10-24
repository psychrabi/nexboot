package hwprofile

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

// 让 t 在 red-1 上做一次套模板开机：身份层记下模板开机，终端记下开机时间。
func templateBoot(t *testing.T, f *fixture, id string, hints Hints) domain.Terminal {
	t.Helper()
	ctx := context.Background()
	term, err := f.svc.Observe(ctx, f.terminal(t, id), hints)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.TerminalIdentities().Create(ctx, domain.TerminalIdentity{ID: id, TerminalID: id, ProfileID: term.HardwareProfileID, InstanceID: "i", Source: "derived", TemplateBootRef: "red-1", TemplateBootAt: f.now, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	img, _ := f.st.Images().Get(ctx, "img-1")
	term, err = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	if err != nil {
		t.Fatal(err)
	}
	return term
}

func TestTemplateBootCallbackDoesNotMakeTheModelAdapted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	f.now = f.now.Add(90 * time.Second)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ModelAdaptations().ForProfileReduction(ctx, term.HardwareProfileID, "red-1"); err == nil {
		t.Fatal("template boot wrote an adaptation row; the model would look adapted and 开始适配 would vanish")
	}
	id, _ := f.st.TerminalIdentities().Get(ctx, "t1")
	if id.TemplateOKRef != "red-1" || id.TemplateStrikes != 0 {
		t.Fatalf("identity %+v", id)
	}
	if got := f.terminal(t, "t1"); got.LastBootOKAt == nil {
		t.Fatal("boot success time still has to be recorded")
	}
}

// 模板开机成功不写适配表（否则下次开机会跳过模板），但它就是开机成功回报，机型显示正常。
func TestTemplateBootsMakeTheModelOKWithoutAnAdaptationRecord(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	// t2 同机型同分组：另一台，也套模板成功。
	term2 := templateBoot(t, f, "t2", e1000e)
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term2); err != nil {
		t.Fatal(err)
	}
	views, err := f.svc.List(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("views %+v %v", views, err)
	}
	v := views[0]
	if v.Status != StatusOK || v.BootOK != 2 || len(v.Points) != 1 || !v.Points[0].Verified {
		t.Fatalf("view %+v", v)
	}
	ts, err := f.svc.ForTerminal(ctx, f.terminal(t, "t1"))
	if err != nil || ts.Status != StatusOK {
		t.Fatalf("terminal status %+v %v", ts, err)
	}
	by, err := f.svc.FailingByGroup(ctx)
	if err != nil || len(by["g1"]) != 0 {
		t.Fatalf("a model that boots must not raise the group hint: %+v %v", by, err)
	}
}

// 同机型一台在这个点上开机成功，就证明了这个点；另一台只是还没开过机。
func TestOneMachineBootingVerifiesThePointForTheModel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Observe(ctx, f.terminal(t, "t2"), e1000e); err != nil { // 同机型，还没开过机
		t.Fatal(err)
	}
	views, _ := f.svc.List(ctx)
	if v := views[0]; v.Status != StatusOK || v.BootOK != 1 || v.BootWaiting != 1 {
		t.Fatalf("view %+v", v)
	}
}

func TestAdaptationRecordKeepsTemplateBootEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	f.svcAdapt(t, term.HardwareProfileID, "red-1")
	views, _ := f.svc.List(ctx)
	if views[0].Status != StatusOK {
		t.Fatalf("view %+v", views[0])
	}
}

func (f *fixture) svcAdapt(t *testing.T, profile, red string) {
	t.Helper()
	if err := f.st.ModelAdaptations().Create(context.Background(), domain.ModelAdaptation{ID: "ad-" + profile + red, ProfileID: profile, ReductionID: red, ConfigID: "cfg-1", Source: domain.AdaptationAdapted, TemplateInstanceID: "x", ClassKey: "0001", NetCfgInstanceID: "{g}", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
}

func TestCallbackAfterTemplateFailedOrLongAgoStillRecordsObserved(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*domain.TerminalIdentity, time.Time){
		"failed": func(v *domain.TerminalIdentity, _ time.Time) { v.TemplateFailedRef = "red-1" },
		"stale":  func(v *domain.TerminalIdentity, now time.Time) { v.TemplateBootAt = now.Add(-30 * time.Minute) },
		"other":  func(v *domain.TerminalIdentity, _ time.Time) { v.TemplateBootRef = "red-2" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			term := templateBoot(t, f, "t1", e1000e)
			id, _ := f.st.TerminalIdentities().Get(ctx, "t1")
			mutate(&id, f.now)
			if err := f.st.TerminalIdentities().Update(ctx, id); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Minute)
			if err := f.svc.NoteBooted(ctx, term); err != nil {
				t.Fatal(err)
			}
			ad, err := f.st.ModelAdaptations().ForProfileReduction(ctx, term.HardwareProfileID, "red-1")
			if err != nil || ad.Source != domain.AdaptationObserved {
				t.Fatalf("a boot that did not use the template is ordinary evidence: %+v %v", ad, err)
			}
		})
	}
}
