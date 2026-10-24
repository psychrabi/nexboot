package winpe

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// buildWIM 拼一个只含头部和 XML 元数据的 WIM 文件，版本信息按真实 Winre.wim 的 XML 结构给出。
func buildWIM(t *testing.T, xml string, images int) string {
	t.Helper()
	utf := utf16.Encode([]rune(xml))
	payload := make([]byte, 2+len(utf)*2)
	payload[0], payload[1] = 0xFF, 0xFE
	for i, u := range utf {
		binary.LittleEndian.PutUint16(payload[2+i*2:], u)
	}
	header := make([]byte, wimHeaderSize)
	copy(header, "MSWIM\x00\x00\x00")
	binary.LittleEndian.PutUint32(header[8:], wimHeaderSize)
	binary.LittleEndian.PutUint32(header[12:], 0x10d00)
	binary.LittleEndian.PutUint32(header[0x2C:], uint32(images))
	// XML 资源头：7 字节大小 + 1 字节标志，8 字节偏移，8 字节原始大小。
	size := uint64(len(payload))
	for i := 0; i < 7; i++ {
		header[0x48+i] = byte(size >> (8 * i))
	}
	header[0x48+7] = 0x02 // 元数据标志
	binary.LittleEndian.PutUint64(header[0x50:], wimHeaderSize)
	binary.LittleEndian.PutUint64(header[0x58:], size)
	path := filepath.Join(t.TempDir(), "boot.wim")
	if err := os.WriteFile(path, append(header, payload...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const winreXML = `<WIM><TOTALBYTES>815048472</TOTALBYTES><IMAGE INDEX="1"><DIRCOUNT>3</DIRCOUNT>` +
	`<NAME>Microsoft Windows Recovery Environment (amd64)</NAME>` +
	`<WINDOWS><ARCH>9</ARCH><PRODUCTNAME>Microsoft® Windows® Operating System</PRODUCTNAME>` +
	`<VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>22621</BUILD><SPBUILD>4037</SPBUILD></VERSION></WINDOWS>` +
	`</IMAGE></WIM>`

func TestReadWIMInfoParsesVersionFromXML(t *testing.T) {
	info, err := ReadWIMInfo(buildWIM(t, winreXML, 1))
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "10.0.22621.4037" || info.Build != 22621 || info.Images != 1 {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(info.Name, "Recovery Environment") || info.Arch != "amd64" {
		t.Fatalf("name/arch = %q/%q", info.Name, info.Arch)
	}
}

func TestReadWIMInfoRejectsNonWIM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wim")
	if err := os.WriteFile(path, []byte("not a wim at all, just some bytes padded to be long enough for a header....................................................................................................................................................."), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWIMInfo(path); err == nil {
		t.Fatal("非 WIM 文件应报错")
	}
}

func TestReadWIMInfoRejectsMissingVersion(t *testing.T) {
	if _, err := ReadWIMInfo(buildWIM(t, `<WIM><IMAGE INDEX="1"><NAME>x</NAME></IMAGE></WIM>`, 1)); err == nil {
		t.Fatal("没有 Windows 版本的 WIM 不能当适配环境")
	}
}

func TestValidateBCD(t *testing.T) {
	good := make([]byte, 8192)
	copy(good, "regf")
	if err := ValidateBCD(good); err != nil {
		t.Fatalf("regf 开头的配置单元应通过：%v", err)
	}
	if err := ValidateBCD([]byte("regf")); err == nil {
		t.Fatal("太短的文件应拒绝")
	}
	bad := make([]byte, 8192)
	copy(bad, "MZ")
	if err := ValidateBCD(bad); err == nil {
		t.Fatal("非注册表配置单元应拒绝")
	}
}
