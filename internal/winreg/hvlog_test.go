package winreg

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 合成的脏 hive：基块按规范构造，数据区填充可识别的字节，日志项带真实的 Marvin32 哈希。

func baseBlock(size int, seq1, seq2, bins, fileType uint32) []byte {
	b := make([]byte, size)
	copy(b, "regf")
	binary.LittleEndian.PutUint32(b[4:], seq1)
	binary.LittleEndian.PutUint32(b[8:], seq2)
	binary.LittleEndian.PutUint64(b[12:], 0x01D0000012345678)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint32(b[24:], 6)
	binary.LittleEndian.PutUint32(b[28:], fileType)
	binary.LittleEndian.PutUint32(b[32:], 1)
	binary.LittleEndian.PutUint32(b[40:], bins)
	binary.LittleEndian.PutUint32(b[44:], 1)
	binary.LittleEndian.PutUint32(b[508:], checksum(b))
	return b
}

func dirtyPrimary(seq1, seq2, bins uint32) []byte {
	return append(baseBlock(baseBlockSize, seq1, seq2, bins, 0), bytes.Repeat([]byte{'A'}, int(bins))...)
}

type testPage struct {
	off  uint32
	fill byte
}

type testEntry struct {
	seq, bins uint32
	pages     []testPage
}

func hvle(e testEntry) []byte {
	b := make([]byte, hvleHeader+8*len(e.pages))
	copy(b, "HvLE")
	binary.LittleEndian.PutUint32(b[12:], e.seq)
	binary.LittleEndian.PutUint32(b[16:], e.bins)
	binary.LittleEndian.PutUint32(b[20:], uint32(len(e.pages)))
	for i, p := range e.pages {
		binary.LittleEndian.PutUint32(b[hvleHeader+8*i:], p.off)
		binary.LittleEndian.PutUint32(b[hvleHeader+8*i+4:], hiveBinAlign)
	}
	for _, p := range e.pages {
		b = append(b, bytes.Repeat([]byte{p.fill}, hiveBinAlign)...)
	}
	if pad := len(b) % logSector; pad != 0 {
		b = append(b, make([]byte, logSector-pad)...)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
	binary.LittleEndian.PutUint64(b[24:], marvin32(b[hvleHeader:]))
	binary.LittleEndian.PutUint64(b[32:], marvin32(b[:32]))
	return b
}

func hvleLog(baseSeq uint32, entries ...testEntry) []byte {
	l := baseBlock(logSector, baseSeq, baseSeq, 8192, 6)
	for _, e := range entries {
		l = append(l, hvle(e)...)
	}
	return l
}

func page(b []byte, off int) byte { return b[baseBlockSize+off] }

func assertClean(t *testing.T, b []byte, seq uint32) {
	t.Helper()
	if err := ValidateHeader(b); err != nil {
		t.Fatalf("recovered header invalid: %v", err)
	}
	if got := binary.LittleEndian.Uint32(b[4:]); got != seq {
		t.Fatalf("seq = %d, want %d", got, seq)
	}
}

// 两个日志轮换写：传入顺序与序号顺序相反也要按序号接续，并按最后一项的数据区大小扩展文件。
func TestReplayHvLEChainsBothLogsBySequence(t *testing.T) {
	primary := dirtyPrimary(5, 4, 8192)
	orig := append([]byte(nil), primary...)
	log1 := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}}, testEntry{5, 8192, []testPage{{4096, 'C'}}})
	log2 := hvleLog(6, testEntry{6, 12288, []testPage{{8192, 'D'}}})
	out, res, err := ReplayLogs(primary, log2, log1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "HvLE" || res.Applied != 3 || res.Seq != 6 {
		t.Fatalf("result = %+v", res)
	}
	assertClean(t, out, 6)
	if len(out) != baseBlockSize+12288 || binary.LittleEndian.Uint32(out[40:]) != 12288 {
		t.Fatalf("size = %d bins = %d", len(out), binary.LittleEndian.Uint32(out[40:]))
	}
	if page(out, 0) != 'B' || page(out, 4096) != 'C' || page(out, 8192) != 'D' {
		t.Fatal("pages not applied in place")
	}
	if !bytes.Equal(primary, orig) {
		t.Fatal("input modified")
	}
}

// 哈希错的项及其后全部不应用；另一个日志接不上序号也不再继续。
func TestReplayHvLEStopsAtBadHash(t *testing.T) {
	log1 := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}}, testEntry{5, 8192, []testPage{{4096, 'C'}}})
	log1[logSector+len(hvle(testEntry{4, 8192, []testPage{{0, 'B'}}}))+hvleHeader+8] ^= 0xFF // 第二项的页数据
	log2 := hvleLog(6, testEntry{6, 8192, []testPage{{0, 'D'}}})
	out, res, err := ReplayLogs(dirtyPrimary(5, 4, 8192), log1, log2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || res.Seq != 4 || page(out, 0) != 'B' || page(out, 4096) != 'A' {
		t.Fatalf("result = %+v", res)
	}
	assertClean(t, out, 4)
}

func TestReplayHvLEStopsAtSequenceGap(t *testing.T) {
	log1 := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}}, testEntry{6, 8192, []testPage{{4096, 'C'}}})
	out, res, err := ReplayLogs(dirtyPrimary(5, 4, 8192), log1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || page(out, 4096) != 'A' {
		t.Fatalf("applied past a gap: %+v", res)
	}
}

// 页引用超出该项的数据区：整项无效，无项可用即拒绝。
func TestReplayHvLERejectsOutOfBoundsPage(t *testing.T) {
	log1 := hvleLog(4, testEntry{4, 8192, []testPage{{8192, 'B'}}})
	if _, _, err := ReplayLogs(dirtyPrimary(5, 4, 8192), log1); err == nil {
		t.Fatal("out-of-bounds page accepted")
	}
}

// 首项既要等于日志基块的主序号，也不能早于主文件的次序号。
func TestReplayHvLERejectsUnexpectedFirstEntry(t *testing.T) {
	if _, _, err := ReplayLogs(dirtyPrimary(9, 8, 8192), hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}})); err == nil {
		t.Fatal("stale log applied")
	}
	if _, _, err := ReplayLogs(dirtyPrimary(5, 4, 8192), hvleLog(3, testEntry{4, 8192, []testPage{{0, 'B'}}})); err == nil {
		t.Fatal("entry not matching log base sequence applied")
	}
}

func TestReplayRejectsEmptyOrInvalidLogs(t *testing.T) {
	broken := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}})
	broken[100] ^= 1 // 日志基块校验和错
	for name, logs := range map[string][][]byte{
		"none":     nil,
		"empty":    {{}, {}},
		"zeroed":   {make([]byte, 8192)},
		"bad base": {broken},
	} {
		if _, _, err := ReplayLogs(dirtyPrimary(5, 4, 8192), logs...); err == nil {
			t.Fatalf("%s: dirty hive accepted without usable log", name)
		}
	}
	bad := dirtyPrimary(5, 4, 8192)
	bad[200] ^= 1
	if _, _, err := ReplayLogs(bad, hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}})); err == nil {
		t.Fatal("primary with a wrong base block checksum accepted")
	}
}

func TestReplayLeavesCleanHiveAlone(t *testing.T) {
	clean := dirtyPrimary(7, 7, 8192)
	out, res, err := ReplayLogs(clean, hvleLog(7, testEntry{7, 8192, []testPage{{0, 'B'}}}))
	if err != nil || res.Applied != 0 || !bytes.Equal(out, clean) {
		t.Fatalf("clean hive changed: %+v %v", res, err)
	}
}

func dirtLog(seq uint32, bits []int, fill byte) []byte {
	l := baseBlock(logSector, seq, seq, 8192, 1)
	vector := make([]byte, logSector)
	copy(vector, "DIRT")
	for _, bit := range bits {
		vector[4+bit/8] |= 1 << (bit % 8)
	}
	l = append(l, vector...)
	for range bits {
		l = append(l, bytes.Repeat([]byte{fill}, logSector)...)
	}
	return l
}

func TestReplayDIRTAppliesBitmapPages(t *testing.T) {
	out, res, err := ReplayLogs(dirtyPrimary(5, 4, 8192), dirtLog(5, []int{1, 3}, 'Z'))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "DIRT" || res.Seq != 5 {
		t.Fatalf("result = %+v", res)
	}
	assertClean(t, out, 5)
	if page(out, 0) != 'A' || page(out, 512) != 'Z' || page(out, 1024) != 'A' || page(out, 1536) != 'Z' {
		t.Fatal("dirty pages misplaced")
	}
	if binary.LittleEndian.Uint32(out[28:]) != 0 {
		t.Fatal("file type not reset to primary")
	}
}

func TestReplayDIRTRejectsMismatchedOrTruncatedLog(t *testing.T) {
	other := dirtLog(5, []int{1}, 'Z')
	other[12] ^= 1
	binary.LittleEndian.PutUint32(other[508:], checksum(other))
	truncated := dirtLog(5, []int{1, 3}, 'Z')
	truncated = truncated[:len(truncated)-logSector]
	for name, l := range map[string][]byte{"timestamp": other, "truncated": truncated} {
		if _, _, err := ReplayLogs(dirtyPrimary(5, 4, 8192), l); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func writeHive(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func stubCheck(t *testing.T, err error) {
	old := checkRecovered
	checkRecovered = func(context.Context, string) error { return err }
	t.Cleanup(func() { checkRecovered = old })
}

// 日志名大小写不限；替换后残留日志原样保留。
func TestRecoverHiveReplacesDirtyHiveAndKeepsLogs(t *testing.T) {
	stubCheck(t, nil)
	log1 := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}})
	dir := writeHive(t, map[string][]byte{"SYSTEM": dirtyPrimary(5, 4, 8192), "system.log1": log1})
	res, err := RecoverHive(context.Background(), filepath.Join(dir, "SYSTEM"))
	if err != nil || res.Applied != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "SYSTEM"))
	assertClean(t, b, 4)
	if l, _ := os.ReadFile(filepath.Join(dir, "system.log1")); !bytes.Equal(l, log1) {
		t.Fatal("log changed")
	}
	if names, _ := os.ReadDir(dir); len(names) != 2 {
		t.Fatalf("temp file left: %v", names)
	}
	if res, err := RecoverHive(context.Background(), filepath.Join(dir, "SYSTEM")); err != nil || res.Applied != 0 {
		t.Fatalf("second pass: %+v %v", res, err)
	}
}

func TestRecoverHiveRefusesAndLeavesFilesUnchanged(t *testing.T) {
	for name, check := range map[string]error{"no usable log": nil, "hivex rejects": errors.New("hivex")} {
		t.Run(name, func(t *testing.T) {
			stubCheck(t, check)
			primary := dirtyPrimary(5, 4, 8192)
			log := hvleLog(4, testEntry{4, 8192, []testPage{{0, 'B'}}})
			if check == nil {
				log = hvleLog(4, testEntry{4, 8192, []testPage{{8192, 'B'}}}) // 越界，无可用项
			}
			dir := writeHive(t, map[string][]byte{"SYSTEM": primary, "SYSTEM.LOG1": log})
			_, err := RecoverHive(context.Background(), filepath.Join(dir, "SYSTEM"))
			var hre HiveRecoveryError
			if !errors.As(err, &hre) {
				t.Fatalf("err = %v", err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "SYSTEM")); !bytes.Equal(b, primary) {
				t.Fatal("hive changed on refusal")
			}
			if names, _ := os.ReadDir(dir); len(names) != 2 {
				t.Fatalf("temp file left: %v", names)
			}
		})
	}
}

// 真实样本：NEXBOOT_TEST_DIRTY_SYSTEM_DIR 指向含 SYSTEM、SYSTEM.LOG1、SYSTEM.LOG2 的目录（只读使用，复制到临时目录）。
// 样本是客户机注册表，不进仓库。
func TestRecoverHiveRealDirtySample(t *testing.T) {
	src := os.Getenv("NEXBOOT_TEST_DIRTY_SYSTEM_DIR")
	if src == "" {
		t.Skip("set NEXBOOT_TEST_DIRTY_SYSTEM_DIR to a directory with a dirty SYSTEM and its LOG1/LOG2")
	}
	if _, err := exec.LookPath("hivexregedit"); err != nil {
		t.Skip("hivexregedit not installed")
	}
	files := map[string][]byte{}
	for _, name := range []string{"SYSTEM", "SYSTEM.LOG1", "SYSTEM.LOG2"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = b
	}
	if dirty, err := HiveDirty(files["SYSTEM"]); err != nil || !dirty {
		t.Fatalf("sample is not dirty: %v %v", dirty, err)
	}
	dir := writeHive(t, files)
	res, err := RecoverHive(context.Background(), filepath.Join(dir, "SYSTEM"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("replayed %+v", res)
	b, _ := os.ReadFile(filepath.Join(dir, "SYSTEM"))
	assertClean(t, b, res.Seq)
	if _, err := controlSet(context.Background(), filepath.Join(dir, "SYSTEM")); err != nil {
		t.Fatal(err)
	}
}
