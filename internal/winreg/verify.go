package winreg

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Group by subsystem/service: each hivex process opens the hive once, irrespective of the number of INF parameters.
func verifyMerged(ctx context.Context, hive, patch, cs string) error {
	want := parseReg(patch)
	roots := map[string]bool{}
	for _, k := range want {
		rel := strings.TrimPrefix(k.Path, prefix+`\`)
		parts := strings.Split(rel, `\`)
		if len(parts) < 2 || !strings.EqualFold(parts[0], cs) {
			return generationFailure(fmt.Errorf("网卡模板回读路径无效：%s", k.Path))
		}
		n := min(3, len(parts))
		if len(parts) > 3 && strings.EqualFold(parts[1], "Control") && strings.EqualFold(parts[2], "Class") {
			n = 4
		}
		roots[strings.Join(parts[:n], `\`)] = true
	}
	paths := make([]string, 0, len(roots))
	for root := range roots {
		paths = append(paths, root)
	}
	sort.Strings(paths)
	have := map[string]regKey{}
	for _, root := range paths {
		raw, err := export(ctx, hive, root)
		if err != nil {
			return fmt.Errorf("网卡模板写入后回读失败：%w", err)
		}
		for _, k := range parseReg(raw) {
			have[strings.ToLower(k.Path)] = k
		}
	}
	for _, k := range want {
		got, ok := have[strings.ToLower(k.Path)]
		if !ok {
			return generationFailure(fmt.Errorf("网卡模板写入后缺少键 %s", k.Path))
		}
		for _, v := range k.Values {
			actual := got.get(v.Name)
			if actual == "" {
				return generationFailure(fmt.Errorf("网卡模板写入后键 %s 缺少值 %s", k.Path, v.Name))
			}
			wt, wb, we := registryValue(v.Raw)
			gt, gb, ge := registryValue(actual)
			if we != nil || ge != nil || wt != gt || !bytes.Equal(wb, gb) {
				return generationFailure(fmt.Errorf("网卡模板写入后键 %s 的值 %s 回读校验失败", k.Path, v.Name))
			}
		}
	}
	return nil
}

func registryValue(raw string) (uint32, []byte, error) {
	if strings.HasPrefix(raw, `"`) {
		b, err := parseHexBytes(utf16Hex(unquoteReg(raw)))
		return 1, b, err
	}
	if strings.HasPrefix(raw, "dword:") {
		n, err := strconv.ParseUint(raw[6:], 16, 32)
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(n))
		return 4, b, err
	}
	kind, body, ok := splitHex(raw)
	if !ok {
		return 0, nil, fmt.Errorf("不支持的注册表值")
	}
	typ := uint64(3)
	if kind != "hex" {
		var err error
		typ, err = strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(kind, "hex("), ")"), 16, 32)
		if err != nil {
			return 0, nil, err
		}
	}
	b, err := parseHexBytes(body)
	return uint32(typ), b, err
}

const generationFailurePrefix = "网卡模板生成失败："

// IsGenerationFailure survives the remote storage error string; transient I/O and command failures are not cached.
func IsGenerationFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), generationFailurePrefix)
}
func generationFailure(err error) error { return fmt.Errorf("%s%w", generationFailurePrefix, err) }

// Templates arrive as reg text; keep quoted Unicode values as exact UTF-16 bytes before hivex reads the file.
func encodeTemplateStrings(patch string) (string, error) {
	keys := parseReg(patch)
	ascii := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return false
			}
		}
		return true
	}
	for i := range keys {
		if !ascii(keys[i].Path) {
			return "", fmt.Errorf("网卡模板不支持非 ASCII 注册表键名")
		}
		for j := range keys[i].Values {
			v := &keys[i].Values[j]
			if !ascii(v.Name) {
				return "", fmt.Errorf("网卡模板不支持非 ASCII 注册表值名")
			}
			if strings.HasPrefix(v.Raw, `"`) {
				v.Raw = quoteReg(unquoteReg(v.Raw))
			}
		}
	}
	return formatReg(keys), nil
}
