package store

import (
	"context"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func TestWinPEEnvRepoRoundTripAndLookups(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Date(2026, 10, 7, 1, 2, 3, 0, time.UTC)
	winre := domain.WinPEEnv{ID: "winpe-win11", Kind: domain.WinPEEnvImageWinRE, ImageID: "win11", Version: "10.0.22621.4037", Build: 22621,
		Dir: "/ndiskless/tank/nd/files/winpe/win11", Files: map[string]string{"boot.wim": "aa", "boot.sdi": "bb"}, CreatedAt: now}
	bcd := domain.WinPEEnv{ID: "winpe-bcd", Kind: domain.WinPEEnvBCD, Dir: "/ndiskless/tank/nd/files/winpe/bcd", Files: map[string]string{"BCD": "cc"}, CreatedAt: now}
	for _, v := range []domain.WinPEEnv{winre, bcd} {
		if err := st.WinPEEnvs().Create(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.WinPEEnvs().Get(ctx, "winpe-win11")
	if err != nil || got.Files["boot.wim"] != "aa" || got.Build != 22621 || !got.CreatedAt.Equal(now) {
		t.Fatalf("got = %+v err = %v", got, err)
	}
	byImage, err := st.WinPEEnvs().ByImage(ctx, "win11")
	if err != nil || byImage.ID != "winpe-win11" {
		t.Fatalf("ByImage = %+v err = %v", byImage, err)
	}
	if _, err := st.WinPEEnvs().ByImage(ctx, "nope"); err == nil {
		t.Fatal("没有提取记录的镜像应返回未找到")
	}
	byKind, err := st.WinPEEnvs().ByKind(ctx, domain.WinPEEnvBCD)
	if err != nil || len(byKind) != 1 || byKind[0].ID != "winpe-bcd" {
		t.Fatalf("ByKind = %+v err = %v", byKind, err)
	}
	if err := st.WinPEEnvs().Delete(ctx, "winpe-bcd"); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.WinPEEnvs().List(ctx); len(all) != 1 {
		t.Fatalf("list after delete = %+v", all)
	}
}
