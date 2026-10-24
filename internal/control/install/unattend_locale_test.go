package install

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// 版本的默认语言取自 WIM 元数据，首次初始化要用它跳过地区、键盘页。
func TestWIMEditionsCarryDefaultLanguage(t *testing.T) {
	raw := `<WIM>
<IMAGE INDEX="1"><NAME>Windows 11 家庭版</NAME><WINDOWS><ARCH>9</ARCH><LANGUAGES><LANGUAGE>zh-CN</LANGUAGE><LANGUAGE>en-US</LANGUAGE><DEFAULT>zh-CN</DEFAULT></LANGUAGES><VERSION><BUILD>26200</BUILD><SPBUILD>7705</SPBUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="2"><NAME>Windows 11 Pro</NAME><WINDOWS><ARCH>9</ARCH><LANGUAGES><LANGUAGE>en-US</LANGUAGE></LANGUAGES><VERSION><BUILD>26200</BUILD><SPBUILD>7705</SPBUILD></VERSION></WINDOWS></IMAGE>
<IMAGE INDEX="3"><NAME>Windows 11 Edu</NAME><WINDOWS><ARCH>9</ARCH><VERSION><BUILD>26200</BUILD><SPBUILD>7705</SPBUILD></VERSION></WINDOWS></IMAGE>
</WIM>`
	editions, err := parseWIMEditions([]byte(raw))
	if err != nil || len(editions) != 3 {
		t.Fatalf("editions = %+v %v", editions, err)
	}
	if editions[0].Language != "zh-CN" || editions[1].Language != "en-US" || editions[2].Language != "" {
		t.Fatalf("languages = %q %q %q", editions[0].Language, editions[1].Language, editions[2].Language)
	}
}

// 没有地区和语言设置时，Windows 11 首次设置会停在选地区、选键盘，安装卡在首次启动。
func TestWindowsUnattendSkipsRegionAndKeyboardPages(t *testing.T) {
	data := string(WindowsUnattend(domain.InstallSession{Username: "tester", Password: "pw-locale-0001", Language: "zh-CN"}))
	if err := xml.Unmarshal([]byte(data), new(struct{})); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	oobe := data[strings.Index(data, `<settings pass="oobeSystem">`):]
	for _, want := range []string{`name="Microsoft-Windows-International-Core"`, "<InputLocale>zh-CN</InputLocale>", "<SystemLocale>zh-CN</SystemLocale>",
		"<UILanguage>zh-CN</UILanguage>", "<UserLocale>zh-CN</UserLocale>", "<HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>", "<HideLocalAccountScreen>true</HideLocalAccountScreen>"} {
		if !strings.Contains(oobe, want) {
			t.Errorf("oobeSystem lacks %s", want)
		}
	}
	// 读不到语言就不写，免得填错让初始化出错；格式不对的值同样不写。
	for _, lang := range []string{"", "zh-CN</UILanguage><x>"} {
		data := string(WindowsUnattend(domain.InstallSession{Username: "tester", Password: "pw-locale-0001", Language: lang}))
		if strings.Contains(data, "International-Core") {
			t.Errorf("language %q must not produce locale settings", lang)
		}
	}
}

func TestStartRecordsSelectedEditionLanguage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:"+filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Servers().Create(ctx, domain.Server{ID: "local", Name: "local", IP: "192.168.50.1", Role: domain.ServerRoleAll, Status: domain.ServerStatusUp})
	_ = st.Pools().Create(ctx, domain.Pool{ID: "pool", Name: "tank", ServerID: "local", Capacity: 1 << 40})
	disk := &diskFake{}
	svc := Service{NodeID: "local", SaveGrace: -1, Store: st, Storage: disk, Host: disk, ReportBase: "http://192.168.50.1:8080", InspectSource: func(context.Context, string) (Metadata, error) {
		return Metadata{Path: "/iso/win.iso", Fingerprint: "fp", SizeBytes: 7 << 30, OSType: domain.OSTypeWindows, Architecture: "amd64",
			Editions: []Edition{{Index: 1, Name: "家庭版", Language: "zh-CN"}, {Index: 6, Name: "Pro", Language: "en-US"}}}, nil
	}, Prepare: func(context.Context, domain.InstallSession) (MediaFiles, error) {
		return MediaFiles{Dir: t.TempDir()}, nil
	}, Render: func(context.Context, domain.InstallSession, MediaFiles) (string, error) { return "#!ipxe\nboot\n", nil }}
	res, err := svc.Start(ctx, StartRequest{SourcePath: "/iso/win.iso", SourceFingerprint: "fp", EditionIndex: 6, Name: "Win11", SizeGiB: 64,
		MAC: "001122334455", IP: "192.168.50.10", Netmask: "255.255.255.0", Username: "user", Password: "pw-locale-0001"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := st.InstallSessions().Get(ctx, res.SessionID)
	if s.Language != "en-US" {
		t.Fatalf("language = %q", s.Language)
	}
}

// Windows Server 首次启动会停在「设置管理员密码」；客户端版内置管理员默认禁用，设了也无害。
func TestWindowsUnattendSetsAdministratorPassword(t *testing.T) {
	data := string(WindowsUnattend(domain.InstallSession{Username: "tester", Password: `A<&\"pw-admin`}))
	if err := xml.Unmarshal([]byte(data), new(struct{})); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if !strings.Contains(data, "<AdministratorPassword><Value>A&lt;&amp;") {
		t.Fatalf("administrator password missing or unescaped")
	}
}
