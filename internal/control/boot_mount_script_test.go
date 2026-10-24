package control

import (
	"context"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/storage"
)

type verificationInstallFake struct {
	script []byte
	ok     bool
}

func (verificationInstallFake) BootScript(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f verificationInstallFake) VerificationMountScript(context.Context, string) ([]byte, bool, error) {
	return f.script, f.ok, nil
}

// 启动器取到的总是当前版本；ISO 安装验证阶段的客户机另带验证回报。
func TestMountScriptServesTheCurrentScriptOrTheVerificationVariant(t *testing.T) {
	s := BootService{APIPort: "18080", Install: verificationInstallFake{}}
	got, err := s.MountScript(context.Background(), "aa:bb")
	if err != nil || !strings.HasPrefix(got, storage.MountScriptMarker) || !strings.Contains(got, "$port = '18080'") {
		t.Fatalf("script = %.60q, %v", got, err)
	}
	s.Install = verificationInstallFake{script: []byte(storage.MountScriptMarker + "\r\n& iso-startup"), ok: true}
	if got, _ := s.MountScript(context.Background(), "aa:bb"); !strings.HasSuffix(got, "& iso-startup") {
		t.Fatalf("验证阶段 = %.60q", got)
	}
}
