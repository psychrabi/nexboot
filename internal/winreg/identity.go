// Package winreg performs the one bounded Linux-side write permitted by ADR-0007.
package winreg

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

func ValidateHeader(b []byte) error {
	if len(b) < 512 || string(b[:4]) != "regf" {
		return fmt.Errorf("Windows注册表文件头无效")
	}
	if binary.LittleEndian.Uint32(b[4:]) != binary.LittleEndian.Uint32(b[8:]) {
		return fmt.Errorf("Windows注册表尚未提交，请先关闭客户机")
	}
	var sum uint32
	for i := 0; i < 508; i += 4 {
		sum ^= binary.LittleEndian.Uint32(b[i:])
	}
	if sum == 0 {
		sum = 1
	} else if sum == 0xffffffff {
		sum = 0xfffffffe
	}
	if binary.LittleEndian.Uint32(b[508:]) != sum {
		return fmt.Errorf("Windows注册表校验和错误")
	}
	return nil
}

const prefix = "HKEY_LOCAL_MACHINE\\SYSTEM"

var continuation = regexp.MustCompile(`\\\r?\n\s*`)
var currentValue = regexp.MustCompile(`(?mi)^"Current"=dword:([0-9a-f]+)$`)
var currentHex = regexp.MustCompile(`(?mi)^"Current"=hex\(4\):([0-9a-f,]+)$`)
var validInstance = regexp.MustCompile(`(?i)^PCI\\VEN_[0-9A-F]{4}&DEV_[0-9A-F]{4}[^\r\n\[\]"]*\\[^\\\r\n\[\]"]+$`)
var validClass = regexp.MustCompile(`^[0-9]{4}$`)

func run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "hivexregedit", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("注册表工具失败：%w：%s", err, strings.TrimSpace(string(out)))
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n"), nil
}
func export(ctx context.Context, hive, key string) (string, error) {
	return run(ctx, "--export", "--prefix", prefix, hive, "\\"+key)
}
func controlSet(ctx context.Context, hive string) (string, error) {
	raw, err := export(ctx, hive, "Select")
	if err != nil {
		return "", err
	}
	var n uint64
	if m := currentValue.FindStringSubmatch(raw); m != nil {
		n, err = strconv.ParseUint(m[1], 16, 32)
	} else if m := currentHex.FindStringSubmatch(raw); m != nil {
		bytes := strings.Split(m[1], ",")
		if len(bytes) != 4 {
			return "", fmt.Errorf("Select.Current格式无效")
		}
		for i, x := range bytes {
			v, e := strconv.ParseUint(x, 16, 8)
			if e != nil {
				return "", e
			}
			n |= v << uint(8*i)
		}
	} else {
		return "", fmt.Errorf("注册表没有Select.Current")
	}
	if err != nil || n < 1 || n > 999 {
		return "", fmt.Errorf("Select.Current值无效")
	}
	return fmt.Sprintf("ControlSet%03d", n), nil
}
func canonical(raw, root string) string {
	raw = continuation.ReplaceAllString(raw, "")
	key := ""
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			key = strings.Replace(line, root, "ROOT", 1)
			lines = append(lines, key)
		} else if strings.HasPrefix(line, "\"") || strings.HasPrefix(line, "@") {
			lines = append(lines, key+line)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// CopyIdentity preserves every source value type and byte, updates one class pointer, and verifies before replacing SYSTEM.
func CopyIdentity(ctx context.Context, hive, template, instance, classKey string) error {
	if !validInstance.MatchString(template) || !validInstance.MatchString(instance) || !validClass.MatchString(classKey) {
		return fmt.Errorf("启动网卡身份路径无效")
	}
	if template[:strings.LastIndex(template, "\\")] != instance[:strings.LastIndex(instance, "\\")] {
		return fmt.Errorf("终端身份与机型模板硬件ID不一致")
	}
	original, err := os.ReadFile(hive)
	if err != nil {
		return err
	}
	if err := ValidateHeader(original); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(hive), "ndidentity-SYSTEM-*")
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
	cs, err := controlSet(ctx, name)
	if err != nil {
		return err
	}
	src := cs + "\\Enum\\" + template
	dst := cs + "\\Enum\\" + instance
	source, err := export(ctx, name, src)
	if err != nil {
		return err
	}
	copied := strings.ReplaceAll(source, "["+prefix+"\\"+src, "["+prefix+"\\"+dst)
	pointer := []string{}
	for _, c := range utf16.Encode([]rune(instance)) {
		pointer = append(pointer, fmt.Sprintf("%02x,%02x", byte(c), byte(c>>8)))
	}
	pointer = append(pointer, "00,00")
	patch := copied + "\n[" + prefix + "\\" + cs + "\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\" + classKey + "]\n\"DeviceInstanceID\"=hex(1):" + strings.Join(pointer, ",") + "\n"
	f, err := os.CreateTemp(filepath.Dir(hive), "ndidentity-*.reg")
	if err != nil {
		return err
	}
	regname := f.Name()
	defer os.Remove(regname)
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
	copiedBack, err := export(ctx, name, dst)
	if err != nil {
		return err
	}
	if canonical(source, prefix+"\\"+src) != canonical(copiedBack, prefix+"\\"+dst) {
		return fmt.Errorf("设备实例复制后的类型或字节回读校验失败")
	}
	class, err := export(ctx, name, cs+"\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318}\\"+classKey)
	if err != nil {
		return err
	}
	if !strings.Contains(continuation.ReplaceAllString(class, ""), "\"DeviceInstanceID\"=hex(1):"+strings.Join(pointer, ",")) {
		return fmt.Errorf("网卡类实例指向回读校验失败")
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

// DisableCrashDump 把当前控制集的 CrashControl\CrashDumpEnabled 设为 0，回读校验后替换 SYSTEM。
// iSCSI 启动写不出蓝屏转储；关掉它，开机就不必为转储准备一套 iSCSI 驱动栈（Windows 10 准备时会蓝屏）。
func DisableCrashDump(ctx context.Context, hive string) error {
	if crashDumpOff(ctx, hive) {
		return nil
	}
	original, err := os.ReadFile(hive)
	if err != nil {
		return err
	}
	if err := ValidateHeader(original); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(hive), "ndcrashdump-SYSTEM-*")
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
	cs, err := controlSet(ctx, name)
	if err != nil {
		return err
	}
	key := cs + "\\Control\\CrashControl"
	f, err := os.CreateTemp(filepath.Dir(hive), "ndcrashdump-*.reg")
	if err != nil {
		return err
	}
	regname := f.Name()
	defer os.Remove(regname)
	if _, err = f.WriteString("[" + prefix + "\\" + key + "]\n\"CrashDumpEnabled\"=dword:00000000\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err := run(ctx, "--merge", "--prefix", prefix, name, regname); err != nil {
		return err
	}
	back, err := export(ctx, name, key)
	if err != nil {
		return err
	}
	if !strings.Contains(continuation.ReplaceAllString(back, ""), "\"CrashDumpEnabled\"=dword:00000000") {
		return fmt.Errorf("关闭崩溃转储后回读校验失败")
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if err := ValidateHeader(b); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, hive)
}

// crashDumpOff 用 hivexget（C 实现，几十毫秒）读当前值：已经关了就不必改写整个注册表文件，
// 开机逐台注入时每台客户机都要过这一步。读不出来按没关处理，交给后面完整的改写与校验。
func crashDumpOff(ctx context.Context, hive string) bool {
	get := func(key, value string) (string, error) {
		out, err := exec.CommandContext(ctx, "hivexget", hive, key, value).Output()
		return strings.TrimSpace(string(out)), err
	}
	current, err := get(`\Select`, "Current")
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(current)
	if err != nil || n < 1 || n > 999 {
		return false
	}
	v, err := get(fmt.Sprintf(`\ControlSet%03d\Control\CrashControl`, n), "CrashDumpEnabled")
	return err == nil && v == "0"
}

// HiveRecoveryError 表示脏的 SYSTEM 无法用事务日志安全恢复；出错时 hive 文件保持原样。
type HiveRecoveryError struct{ Reason string }

func (e HiveRecoveryError) Error() string { return "注册表事务日志回放失败：" + e.Reason }

// checkRecovered 用 hivexregedit 读出 Select 与整个当前控制集，确认回放结果能被完整解析。
var checkRecovered = func(ctx context.Context, hive string) error {
	cs, err := controlSet(ctx, hive)
	if err != nil {
		return err
	}
	_, err = export(ctx, hive, cs)
	return err
}

// RecoverHive 在 SYSTEM 基块两个序号不等（断电、强制关机）时回放同目录的 .LOG1/.LOG2（旧格式含 .LOG），
// 写临时文件、经 ValidateHeader 与 hivexregedit 校验后原子替换；干净的 hive 不动，Applied 为 0。
// 残留日志不截断：主文件干净时 Windows 不回放日志（regf 规范 "Dirty state of a hive"：
// hive 不脏时新格式日志里的后续项被忽略；旧格式只在 hive 脏时才应用）。保留它们也便于事后取证。
func RecoverHive(ctx context.Context, hive string) (ReplayResult, error) {
	fail := func(err error) (ReplayResult, error) { return ReplayResult{}, HiveRecoveryError{Reason: err.Error()} }
	primary, err := os.ReadFile(hive)
	if err != nil {
		return ReplayResult{}, err
	}
	if dirty, err := HiveDirty(primary); err != nil {
		return fail(err)
	} else if !dirty {
		return ReplayResult{}, ValidateHeader(primary)
	}
	dir, base := filepath.Dir(hive), filepath.Base(hive)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ReplayResult{}, err
	}
	var logs [][]byte
	for _, suffix := range []string{".LOG1", ".LOG2", ".LOG"} {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), base+suffix) {
				b, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					return ReplayResult{}, err
				}
				logs = append(logs, b)
			}
		}
	}
	out, res, err := ReplayLogs(primary, logs...)
	if err != nil {
		return fail(err)
	}
	if err := ValidateHeader(out); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(dir, "ndreplay-SYSTEM-*")
	if err != nil {
		return ReplayResult{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(out); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return ReplayResult{}, err
	}
	if err := checkRecovered(ctx, name); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, 0600); err != nil {
		return ReplayResult{}, err
	}
	return res, os.Rename(name, hive)
}
