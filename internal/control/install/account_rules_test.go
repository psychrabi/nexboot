package install

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 账户名和密码各说各的：点名是哪一项、差在哪，不把两条规则塞进一句让人猜。
func TestStartNamesWhichAccountFieldIsWrong(t *testing.T) {
	cases := []struct{ user, password, want, notWant string }{
		{"nexboot", "123456", "初始密码至少 8 位，现在 6 位", "账户名"},
		{"Admin1", "Password123!", "「Admin1」", "密码"},
		{"root", "Password123!", "系统保留名", "密码"},
	}
	for _, tc := range cases {
		t.Run(tc.user+"/"+tc.password, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp}); err != nil {
				t.Fatal(err)
			}
			if err := st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40}); err != nil {
				t.Fatal(err)
			}
			disk := &diskFake{}
			svc := Service{NodeID: "local", Store: st, Storage: disk, Host: disk, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) { return MediaFiles{}, nil }, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "", nil }}
			_, err = svc.Start(ctx, StartRequest{SourcePath: "/iso/x.iso", SourceFingerprint: "fp", Name: "X", SizeGiB: 64, MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: tc.user, Password: tc.password})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("error = %v, want %q without %q", err, tc.want, tc.notWant)
			}
		})
	}
}
