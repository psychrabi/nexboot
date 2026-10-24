package hwprofile

import (
	"context"
	"testing"
	"time"
)

// 完整适配写过的记录不证明能开机；收到 Windows 开机成功回报才算正常。
func TestModelIsOKOnlyAfterAWindowsBootCallback(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term, err := f.svc.Observe(ctx, f.terminal(t, "t1"), e1000e)
	if err != nil {
		t.Fatal(err)
	}
	f.svcAdapt(t, term.HardwareProfileID, "red-1")
	v, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || v.Status != StatusWaiting || v.BootWaiting != 1 || v.BootOK != 0 {
		t.Fatalf("an adaptation record without a boot callback is still waiting: %+v err=%v", v, err)
	}
	img, _ := f.st.Images().Get(ctx, "img-1")
	term, err = f.svc.NoteBootStarted(ctx, term, img, "red-1")
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || v.Status != StatusOK || v.BootOK != 1 || v.BootWaiting != 0 {
		t.Fatalf("a Windows callback on the current point makes the model ok: %+v err=%v", v, err)
	}
}

// 网卡模板连续两次没进系统、而 boot_failures 里没有记录（两次开机隔得久）时，也算开机失败。
func TestTemplateFailureMarksTheMachineFailed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	term := templateBoot(t, f, "t1", e1000e)
	id, err := f.st.TerminalIdentities().Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	id.TemplateFailedRef = "red-1"
	if err := f.st.TerminalIdentities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	ts, err := f.svc.ForTerminal(ctx, f.terminal(t, "t1"))
	if err != nil || ts.Status != StatusFailed || ts.Failure == nil || ts.Failure.Stage != BootStageTemplate {
		t.Fatalf("terminal status %+v err=%v", ts, err)
	}
	v, err := f.svc.Get(ctx, term.HardwareProfileID)
	if err != nil || v.Status != StatusFailed || v.BootFailed != 1 || len(v.TerminalList) != 1 || v.TerminalList[0].Boot != StatusFailed {
		t.Fatalf("model view %+v err=%v", v, err)
	}
	// 之后本机在这个点上开机成功，失败标志不再算数。
	img, _ := f.st.Images().Get(ctx, "img-1")
	f.now = f.now.Add(time.Hour)
	term, _ = f.svc.NoteBootStarted(ctx, f.terminal(t, "t1"), img, "red-1")
	f.now = f.now.Add(time.Minute)
	if err := f.svc.NoteBooted(ctx, term); err != nil {
		t.Fatal(err)
	}
	v, _ = f.svc.Get(ctx, term.HardwareProfileID)
	if v.Status != StatusOK || v.TerminalList[0].Boot != StatusOK || v.TerminalList[0].BootAt == nil {
		t.Fatalf("model view after success %+v", v)
	}
}

// 同一块网卡在 BIOS 和 UEFI 下是两个机型；默认名字一样时补上固件，运维改过的名字不动。
func TestSameDefaultNameIsToldApartByFirmware(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	bios := e1000e
	bios.Platform = "pcbios"
	efiTerm, err := f.svc.Observe(ctx, f.terminal(t, "t1"), e1000e)
	if err != nil {
		t.Fatal(err)
	}
	biosTerm, err := f.svc.Observe(ctx, f.terminal(t, "t2"), bios)
	if err != nil || biosTerm.HardwareProfileID == efiTerm.HardwareProfileID {
		t.Fatalf("firmware is part of the model: %+v err=%v", biosTerm, err)
	}
	if _, err := f.svc.Observe(ctx, f.terminal(t, "t3"), vmxnet3); err != nil {
		t.Fatal(err)
	}
	names := func() map[string]string {
		t.Helper()
		views, err := f.svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, v := range views {
			out[v.ID] = v.Name
		}
		return out
	}
	got := names()
	if got[efiTerm.HardwareProfileID] != "VMware 虚拟机 · Intel E1000E · UEFI" || got[biosTerm.HardwareProfileID] != "VMware 虚拟机 · Intel E1000E · BIOS" {
		t.Fatalf("names = %+v", got)
	}
	for id, name := range got {
		if id != efiTerm.HardwareProfileID && id != biosTerm.HardwareProfileID && name != "VMware 虚拟机 · VMware vmxnet3" {
			t.Fatalf("a unique name keeps no suffix: %q", name)
		}
	}
	if ts, err := f.svc.ForTerminal(ctx, f.terminal(t, "t2")); err != nil || ts.ProfileName != "VMware 虚拟机 · Intel E1000E · BIOS" {
		t.Fatalf("terminal detail uses the same name: %+v err=%v", ts, err)
	}
	if err := f.svc.Rename(ctx, biosTerm.HardwareProfileID, "老机房"); err != nil {
		t.Fatal(err)
	}
	got = names()
	if got[biosTerm.HardwareProfileID] != "老机房" || got[efiTerm.HardwareProfileID] != "VMware 虚拟机 · Intel E1000E" {
		t.Fatalf("after rename names = %+v", got)
	}
}
