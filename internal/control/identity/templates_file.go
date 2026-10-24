package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tianwei/diskless/internal/winreg"
)

type templateFileEntry struct {
	Vendor         string `json:"vendor"`
	Device         string `json:"device"`
	Manufacturer   string `json:"manufacturer"`
	InstancePrefix string `json:"instance_prefix"`
	SuffixRule     string `json:"suffix_rule"`
	RegFile        string `json:"reg_file"`
	BuiltinDriver  bool   `json:"builtin_driver"`
	DriverHWID     string `json:"driver_hwid"`
	Generate       bool   `json:"generate"`
}

var pciID = regexp.MustCompile(`^[0-9a-f]{4}$`)

// LoadTemplates 读允许名单文件：JSON 数组，reg_file 相对该文件所在目录。
// 任何一条不合法就整体拒绝，调用方应按「名单为空」继续运行，机器照旧按适配记录开机。
func LoadTemplates(path string) ([]TemplateEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []templateFileEntry
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("网卡模板名单不是有效的 JSON：%w", err)
	}
	out := make([]TemplateEntry, 0, len(list))
	for i, f := range list {
		e := TemplateEntry{Vendor: strings.ToLower(f.Vendor), Device: strings.ToLower(f.Device), Manufacturer: f.Manufacturer, InstancePrefix: f.InstancePrefix, SuffixRule: f.SuffixRule, BuiltinDriver: f.BuiltinDriver, DriverHWID: f.DriverHWID}
		at := fmt.Sprintf("网卡模板名单第 %d 条", i+1)
		if !pciID.MatchString(e.Vendor) || !pciID.MatchString(e.Device) {
			return nil, fmt.Errorf("%s：vendor/device 必须是 4 位十六进制", at)
		}
		if e.SuffixRule != "" && e.SuffixRule != SuffixVMwareMAC {
			return nil, fmt.Errorf("%s：不认识的 suffix_rule %q", at, e.SuffixRule)
		}
		if e.SuffixRule != "" && !strings.HasSuffix(e.InstancePrefix, `\`) {
			return nil, fmt.Errorf("%s：instance_prefix 必须以反斜杠结尾", at)
		}
		if f.RegFile == "" {
			if !f.Generate {
				return nil, fmt.Errorf("%s：缺少 reg_file", at)
			}
			e.Generate = true
			if !e.BuiltinDriver && e.DriverHWID == "" {
				e.BuiltinDriver = true
			}
		} else {
			reg, err := os.ReadFile(filepath.Join(filepath.Dir(path), filepath.Clean(f.RegFile)))
			if err != nil {
				return nil, fmt.Errorf("%s：%w", at, err)
			}
			e.Reg = string(reg)
			// 用一组示例参数试渲染，模板越界或有未知占位符时在启动时就暴露。
			if _, err := winreg.RenderTemplate(e.Reg, winreg.TemplateParams{Instance: `PCI\VEN_` + strings.ToUpper(e.Vendor) + `&DEV_` + strings.ToUpper(e.Device) + `\X1`, GUID: "{00000000-0000-0000-0000-000000000000}", MAC: "001122334455", ControlSet: "ControlSet001", ClassKey: "0000"}); err != nil {
				return nil, fmt.Errorf("%s：%w", at, err)
			}
		}
		if !e.BuiltinDriver && e.DriverHWID == "" {
			return nil, fmt.Errorf("%s：驱动不随系统自带时必须写 driver_hwid", at)
		}
		out = append(out, e)
	}
	return out, nil
}
