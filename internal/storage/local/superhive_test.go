package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/winreg"
)

// 单元测试的池是替身，没有真 zvol 可挂载；默认跳过注册表检查，需要它的测试自行替换。
func TestMain(m *testing.M) {
	superHiveFn = func(context.Context, *Agent, string) error { return nil }
	os.Exit(m.Run())
}

func stubSuperHive(t *testing.T, fn func(context.Context, *Agent, string) error) {
	old := superHiveFn
	superHiveFn = fn
	t.Cleanup(func() { superHiveFn = old })
}

func poolState(z *fakePool) ([]string, []string, map[string]string) {
	ds := append([]string(nil), z.datasets...)
	sort.Strings(ds)
	var snaps []string
	for s, ok := range z.snaps {
		if ok {
			snaps = append(snaps, s)
		}
	}
	sort.Strings(snaps)
	origin := map[string]string{}
	for k, v := range z.origin {
		origin[k] = v
	}
	return ds, snaps, origin
}

// 注册表恢复在任何改名、promote、快照之前，对象是超管机克隆本身。
func TestSuperStopRecoversHiveBeforeTouchingConfig(t *testing.T) {
	z := seedSuperPool()
	var seen string
	opsAtCheck := -1
	stubSuperHive(t, func(_ context.Context, _ *Agent, dataset string) error {
		seen, opsAtCheck = dataset, len(z.ops)
		return nil
	})
	res, err := New("server-a", z).SuperStop(context.Background(), superStopReq())
	if err != nil {
		t.Fatal(err)
	}
	if seen != "tank/run/SCLIENT-AABBCCDDEEFF" || opsAtCheck != 0 {
		t.Fatalf("checked %q after %d ops", seen, opsAtCheck)
	}
	if res.System.Name != "@v1" || !z.snaps["tank/nd/cfg@v1"] {
		t.Fatalf("not saved: %+v", res.System)
	}
}

// 不能安全恢复：返回类型化错误，配置、还原点、超管机克隆都原样保留，可在正常关机后重存。
func TestSuperStopRefusesUnsafeHiveAndChangesNothing(t *testing.T) {
	for _, unsafe := range []storage.SuperHiveUnsafe{{FastStartup: true}, {Reason: "没有可用于恢复的注册表事务日志"}} {
		z := seedSuperPool()
		ds, snaps, origin := poolState(z)
		stubSuperHive(t, func(context.Context, *Agent, string) error { return unsafe })
		agent := New("server-a", z)
		_, err := agent.SuperStop(context.Background(), superStopReq())
		var got storage.SuperHiveUnsafe
		if !errors.As(err, &got) || got != unsafe {
			t.Fatalf("err = %v", err)
		}
		if len(z.ops) != 0 {
			t.Fatalf("pool modified: %v", z.ops)
		}
		ds2, snaps2, origin2 := poolState(z)
		if !reflect.DeepEqual(ds, ds2) || !reflect.DeepEqual(snaps, snaps2) || !reflect.DeepEqual(origin, origin2) {
			t.Fatalf("pool changed: %v %v %v", ds2, snaps2, origin2)
		}
		// 问题排除后再存走正常流程。
		stubSuperHive(t, func(context.Context, *Agent, string) error { return nil })
		if _, err := agent.SuperStop(context.Background(), superStopReq()); err != nil || !z.snaps["tank/nd/cfg@v1"] {
			t.Fatalf("retry: %v", err)
		}
	}
}

// 替换开始后（配置已挪到暂存名）续跑不再检查：此时拒绝会让池停在半途。放弃保存也不检查。
func TestSuperStopSkipsHiveCheckWhenResumingOrDiscarding(t *testing.T) {
	calls := 0
	z := seedSuperPool()
	z.failOn = "rename tank/run/SCLIENT-AABBCCDDEEFF"
	agent := New("server-a", z)
	stubSuperHive(t, func(context.Context, *Agent, string) error { return nil })
	if _, err := agent.SuperStop(context.Background(), superStopReq()); err == nil {
		t.Fatal("expected interruption")
	}
	z.failOn = ""
	stubSuperHive(t, func(context.Context, *Agent, string) error { calls++; return errors.New("must not run") })
	if _, err := agent.SuperStop(context.Background(), superStopReq()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := New("server-a", seedSuperPool()).SuperStop(context.Background(), storage.SuperStopReq{MAC: "AA:BB:CC:DD:EE:FF"}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if calls != 0 {
		t.Fatalf("hive check ran %d times", calls)
	}
}

func TestHibernatedReadsHiberfilHeader(t *testing.T) {
	for head, want := range map[string]bool{"HIBR": true, "wake": true, "WAKE": true, "\x00\x00\x00\x00": false, "RSTR": false, "": false} {
		root := t.TempDir()
		if head != "" {
			if err := os.WriteFile(filepath.Join(root, "hiberfil.sys"), append([]byte(head), make([]byte, 4092)...), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := hibernated(root); got != want {
			t.Fatalf("%q: hibernated = %v", head, got)
		}
	}
}

// 开机兜底恢复失败时身份层不留半成品，还原点本身不动。
func TestIdentityLayerRefusesUnrecoverableHiveWithoutLeftovers(t *testing.T) {
	z := seedSuperPool()
	a := New("node-a", z)
	a.identityWriteFn = func(context.Context, string, storage.IdentityLayerReq) error {
		return winreg.HiveRecoveryError{Reason: "没有可用于恢复的注册表事务日志"}
	}
	req := storage.IdentityLayerReq{MAC: "001122334455", ConfigID: "cfg", SnapshotName: "0", InstanceID: `PCI\VEN_15AD&DEV_07B0\FIRST`, ClassKey: "0010"}
	var hre winreg.HiveRecoveryError
	if err := a.CreateIdentityLayer(context.Background(), req); !errors.As(err, &hre) {
		t.Fatalf("err = %v", err)
	}
	if z.present("tank/run/IDENT-001122334455") || z.snaps["tank/run/IDENT-001122334455@ready"] || !z.snaps["tank/nd/cfg@0"] {
		t.Fatalf("leftovers: %v %v", z.datasets, z.snaps)
	}
}
