// Package winpe 读取适配环境文件的元数据：WIM 的 Windows 版本、BCD 的基本形态。
// 只解析头部与 XML，不解压也不校验内容；版本用于 C-16 的「WinPE 不得旧于镜像」闸门。
package winpe

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf16"
)

// WIMInfo 是一个 WIM 文件里第一个映像的版本信息。
type WIMInfo struct {
	Version string // 10.0.22621.4037
	Build   int
	Images  int
	Name    string
	Arch    string // amd64 / x86 / arm64
}

const (
	wimHeaderSize = 208
	wimMagic      = "MSWIM\x00\x00\x00"
	// XML 元数据最大允许 16MB，超过说明不是正常 WIM。
	maxWIMXML = 16 << 20
)

// ReadWIMInfo 读 WIM 头部里的 XML 元数据，返回第一个映像的 Windows 版本。
func ReadWIMInfo(path string) (WIMInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return WIMInfo{}, err
	}
	defer f.Close()
	header := make([]byte, wimHeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return WIMInfo{}, fmt.Errorf("不是 WIM 文件：读不到头部")
	}
	if string(header[:8]) != wimMagic {
		return WIMInfo{}, fmt.Errorf("不是 WIM 文件：魔数不符")
	}
	images := int(binary.LittleEndian.Uint32(header[0x2C:]))
	// XML 资源头在 0x48：前 7 字节是大小，第 8 字节是标志，随后偏移与原始大小。
	var size uint64
	for i := 0; i < 7; i++ {
		size |= uint64(header[0x48+i]) << (8 * i)
	}
	offset := binary.LittleEndian.Uint64(header[0x50:])
	if size == 0 || size > maxWIMXML {
		return WIMInfo{}, fmt.Errorf("WIM 的 XML 元数据大小异常（%d 字节）", size)
	}
	raw := make([]byte, size)
	if _, err := f.ReadAt(raw, int64(offset)); err != nil {
		return WIMInfo{}, fmt.Errorf("读不到 WIM 的 XML 元数据：%w", err)
	}
	info, err := parseWIMXML(decodeUTF16(raw))
	if err != nil {
		return WIMInfo{}, err
	}
	info.Images = images
	return info, nil
}

// decodeUTF16 把带 BOM 的 UTF-16LE 元数据转成 UTF-8；没有 BOM 时按原样返回。
func decodeUTF16(raw []byte) []byte {
	if len(raw) < 2 || !(raw[0] == 0xFF && raw[1] == 0xFE) {
		return raw
	}
	raw = raw[2:]
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return []byte(string(utf16.Decode(u)))
}

type wimXML struct {
	Images []struct {
		Name    string `xml:"NAME"`
		Windows struct {
			Arch    string `xml:"ARCH"`
			Version struct {
				Major   string `xml:"MAJOR"`
				Minor   string `xml:"MINOR"`
				Build   string `xml:"BUILD"`
				SPBuild string `xml:"SPBUILD"`
			} `xml:"VERSION"`
		} `xml:"WINDOWS"`
	} `xml:"IMAGE"`
}

var errNoWindowsVersion = errors.New("WIM 里没有 Windows 版本信息，不是可用的 WinPE")

func parseWIMXML(data []byte) (WIMInfo, error) {
	var doc wimXML
	if err := xml.Unmarshal(bytes.TrimSpace(data), &doc); err != nil {
		return WIMInfo{}, fmt.Errorf("WIM 的 XML 元数据无法解析：%w", err)
	}
	if len(doc.Images) == 0 {
		return WIMInfo{}, errNoWindowsVersion
	}
	img := doc.Images[0]
	v := img.Windows.Version
	if v.Major == "" || v.Build == "" {
		return WIMInfo{}, errNoWindowsVersion
	}
	build, err := strconv.Atoi(v.Build)
	if err != nil {
		return WIMInfo{}, errNoWindowsVersion
	}
	minor := v.Minor
	if minor == "" {
		minor = "0"
	}
	version := v.Major + "." + minor + "." + v.Build
	if v.SPBuild != "" {
		version += "." + v.SPBuild
	}
	return WIMInfo{Version: version, Build: build, Name: img.Name, Arch: archName(img.Windows.Arch)}, nil
}

// archName 把 WIM 的处理器编号换成常用名字；未知编号原样返回。
func archName(code string) string {
	switch code {
	case "0":
		return "x86"
	case "9":
		return "amd64"
	case "12":
		return "arm64"
	}
	return code
}

// ValidateBCD 判断上传的 BCD 像一个注册表配置单元：以 regf 开头、至少一个基块。
// 条目是否正确只有真机引导能证明，这里只拦明显传错的文件。
func ValidateBCD(data []byte) error {
	if len(data) < 4096 {
		return fmt.Errorf("BCD 文件太小（%d 字节），不是启动配置数据", len(data))
	}
	if string(data[:4]) != "regf" {
		return fmt.Errorf("不是 BCD 文件：启动配置数据是注册表配置单元，应以 regf 开头")
	}
	return nil
}
