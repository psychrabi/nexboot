package winreg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// TemplateParams 是套用网卡模板时每台机器各自填的部分。ControlSet 与 ClassKey 由写入时从注册表里读出。
type TemplateParams struct {
	Instance   string // PCI\VEN_xxxx&DEV_xxxx...\序列号
	GUID       string // 含花括号的接口 GUID
	MAC        string // 12 位十六进制
	ControlSet string
	ClassKey   string
}

const classRoot = "Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}"

var (
	placeholder = regexp.MustCompile(`\{\{([A-Z_]+)\}\}`)
	validGUID   = regexp.MustCompile(`^\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}$`)
	validMAC    = regexp.MustCompile(`^[0-9a-fA-F]{12}$`)
	validCS     = regexp.MustCompile(`^ControlSet[0-9]{3}$`)
	regHeader   = regexp.MustCompile(`(?m)^\[(.*)\]\s*$`)
	classChild  = regexp.MustCompile(`\\([0-9]{4})\]\s*$`)
)

// RenderTemplate 把模板里的占位符换成这台机器的值，并拒绝越界：每个键必须落在当前控制集之下。
// 占位符：{{INSTANCE}}（键名里的路径）、{{INSTANCE_ESC}}（值里的路径，反斜杠加倍）、{{GUID}}、{{MAC}}、{{CONTROLSET}}、{{CLASSKEY}}。
func RenderTemplate(tpl string, p TemplateParams) (string, error) {
	if !validInstance.MatchString(p.Instance) || !validGUID.MatchString(p.GUID) || !validMAC.MatchString(p.MAC) || !validCS.MatchString(p.ControlSet) || !validClass.MatchString(p.ClassKey) {
		return "", fmt.Errorf("网卡模板参数无效")
	}
	var bad string
	out := placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		switch m {
		case "{{INSTANCE}}":
			return p.Instance
		case "{{INSTANCE_ESC}}":
			return strings.ReplaceAll(p.Instance, `\`, `\\`)
		case "{{GUID}}":
			return strings.ToUpper(p.GUID)
		case "{{MAC}}":
			return strings.ToUpper(p.MAC)
		case "{{CONTROLSET}}":
			return p.ControlSet
		case "{{CLASSKEY}}":
			return p.ClassKey
		}
		bad = m
		return m
	})
	if bad != "" {
		return "", fmt.Errorf("网卡模板含未知占位符 %s", bad)
	}
	headers := regHeader.FindAllStringSubmatch(out, -1)
	if len(headers) == 0 {
		return "", fmt.Errorf("网卡模板里没有注册表键")
	}
	allowed := prefix + "\\" + p.ControlSet + "\\"
	for _, h := range headers {
		if !strings.HasPrefix(h[1], allowed) {
			return "", fmt.Errorf("网卡模板只能写当前控制集，发现越界的键：%s", h[1])
		}
	}
	return out, nil
}

// freeClassKey 返回网卡类下第一个比现有最大编号更大的四位编号。
func freeClassKey(exported string) (string, error) {
	max := -1
	for _, line := range strings.Split(exported, "\n") {
		if m := classChild.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > max {
				max = n
			}
		}
	}
	if max >= 9998 {
		return "", fmt.Errorf("网卡类实例编号已用尽")
	}
	return fmt.Sprintf("%04d", max+1), nil
}

// ApplyTemplate 把渲染后的网卡模板合并进 SYSTEM，回读确认每个键和值名都在、类实例指向这台机器的设备实例，再整体替换。
func ApplyTemplate(ctx context.Context, hive, tpl string, p TemplateParams) error {
	original, err := os.ReadFile(hive)
	if err != nil {
		return err
	}
	if err := ValidateHeader(original); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(hive), "ndtemplate-SYSTEM-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(original); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if p.ControlSet, err = controlSet(ctx, name); err != nil {
		return err
	}
	classes, err := export(ctx, name, p.ControlSet+"\\"+classRoot)
	if err != nil {
		return err
	}
	if p.ClassKey, err = freeClassKey(classes); err != nil {
		return err
	}
	rendered, err := RenderTemplate(tpl, p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(hive), "ndtemplate-*.reg")
	if err != nil {
		return err
	}
	regname := f.Name()
	defer os.Remove(regname)
	pointer := []string{}
	for _, c := range utf16.Encode([]rune(p.Instance)) {
		pointer = append(pointer, fmt.Sprintf("%02x,%02x", byte(c), byte(c>>8)))
	}
	pointer = append(pointer, "00,00")
	patch := rendered + "\n[" + prefix + "\\" + p.ControlSet + "\\" + classRoot + "\\" + p.ClassKey + "]\n\"DeviceInstanceID\"=hex(1):" + strings.Join(pointer, ",") + "\n"
	patch, err = encodeTemplateStrings(patch)
	if err != nil {
		f.Close()
		return err
	}
	if _, err = f.WriteString(patch); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err := run(ctx, "--merge", "--prefix", prefix, name, regname); err != nil {
		return err
	}
	if err := verifyMerged(ctx, name, patch, p.ControlSet); err != nil {
		return err
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if err := ValidateHeader(b); err != nil {
		return err
	}
	if err := os.Chmod(name, 0600); err != nil {
		return err
	}
	return os.Rename(name, hive)
}
