package winreg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
	"sort"
)

// 注册表事务日志回放，按 regf 格式规范（Suhanov, "Windows registry file format specification"）：
// Windows 8.1 起用新格式（HvLE 日志项），更早用旧格式（DIRT 脏页位图）。
// 只覆盖「基块校验和正确、两个序号不等」这一种脏状态：这是断电/强制关机留下的状态。
// 基块本身损坏时不从日志恢复基块，直接拒绝，由调用方保持原样不动。

const (
	baseBlockSize = 4096
	logSector     = 512
	hvleHeader    = 40
	hiveBinAlign  = 4096
	// 单个 hive 的数据区上限远小于此值；用来挡住日志里被破坏的尺寸，避免按它分配内存。
	maxHiveBins = 1 << 31
)

// ReplayResult 描述一次回放：Applied 是写入的日志项（旧格式为 1）数，Seq 是回放后基块的序号。
type ReplayResult struct {
	Format  string // "HvLE" 或 "DIRT"
	Applied int
	Seq     uint32
}

// HiveDirty 报告主文件基块是否需要恢复（两个序号不等）；基块无效时返回错误。
func HiveDirty(primary []byte) (bool, error) {
	if len(primary) < baseBlockSize || string(primary[:4]) != "regf" {
		return false, fmt.Errorf("Windows注册表文件头无效")
	}
	if checksum(primary) != binary.LittleEndian.Uint32(primary[508:]) {
		return false, fmt.Errorf("Windows注册表文件头校验和错误，无法从事务日志恢复")
	}
	return binary.LittleEndian.Uint32(primary[4:]) != binary.LittleEndian.Uint32(primary[8:]), nil
}

// ReplayLogs 把事务日志回放到脏的主文件上，返回新的主文件内容；输入不被修改。
// 新格式：两个日志的项按序号排成一条链，从「日志基块的主序号」开始（且不小于主文件的次序号），
// 遇到断号、哈希错、越界即停在最后一个有效项。旧格式：用基块时间戳与主文件匹配的那个日志。
// 一项都应用不了时返回错误：脏主文件可能是写了一半的，不能当作已恢复。
func ReplayLogs(primary []byte, logs ...[]byte) ([]byte, ReplayResult, error) {
	dirty, err := HiveDirty(primary)
	if err != nil {
		return nil, ReplayResult{}, err
	}
	if !dirty {
		return append([]byte(nil), primary...), ReplayResult{Seq: binary.LittleEndian.Uint32(primary[4:])}, nil
	}
	var newer, older [][]byte
	for _, l := range logs {
		switch logType(l) {
		case 6:
			newer = append(newer, l)
		case 1, 2:
			older = append(older, l)
		}
	}
	if len(newer) > 0 {
		return replayHvLE(primary, newer)
	}
	for _, l := range older {
		if out, res, err := replayDIRT(primary, l); err == nil {
			return out, res, nil
		}
	}
	return nil, ReplayResult{}, fmt.Errorf("没有可用于恢复的注册表事务日志")
}

// logType 返回有效日志基块的文件类型（1/2 旧格式，6 新格式）；无效返回 0。
// 有效：签名、校验和正确且两个序号相等（规范：否则日志本身写了一半）。
func logType(l []byte) uint32 {
	if len(l) < logSector || string(l[:4]) != "regf" || checksum(l) != binary.LittleEndian.Uint32(l[508:]) {
		return 0
	}
	if binary.LittleEndian.Uint32(l[4:]) != binary.LittleEndian.Uint32(l[8:]) {
		return 0
	}
	return binary.LittleEndian.Uint32(l[28:])
}

type hvlePage struct {
	off  uint32
	data []byte
}

type hvleEntry struct {
	flags, seq, bins uint32
	pages            []hvlePage
}

// hvleEntries 解析一个新格式日志里从第二个扇区起的有效日志项，遇到第一个无效项即停。
// 只保留序号不小于日志基块主序号的项（更早的是已应用过的旧项）。
func hvleEntries(l []byte) []hvleEntry {
	first := binary.LittleEndian.Uint32(l[4:])
	var out []hvleEntry
	for off := logSector; off+hvleHeader <= len(l); {
		e := l[off:]
		if string(e[:4]) != "HvLE" {
			break
		}
		size := int(binary.LittleEndian.Uint32(e[4:]))
		if size < hvleHeader || size%logSector != 0 || size > len(e) {
			break
		}
		e = e[:size]
		bins := binary.LittleEndian.Uint32(e[16:])
		count := int(binary.LittleEndian.Uint32(e[20:]))
		if bins == 0 || bins%hiveBinAlign != 0 || bins > maxHiveBins {
			break
		}
		if marvin32(e[hvleHeader:]) != binary.LittleEndian.Uint64(e[24:]) || marvin32(e[:32]) != binary.LittleEndian.Uint64(e[32:]) {
			break
		}
		if count < 0 || hvleHeader+8*count > size {
			break
		}
		ent := hvleEntry{flags: binary.LittleEndian.Uint32(e[8:]), seq: binary.LittleEndian.Uint32(e[12:]), bins: bins}
		data := hvleHeader + 8*count
		ok := true
		for i := 0; i < count; i++ {
			ref := e[hvleHeader+8*i:]
			pageOff, pageSize := binary.LittleEndian.Uint32(ref), binary.LittleEndian.Uint32(ref[4:])
			if uint64(pageOff)+uint64(pageSize) > uint64(bins) || uint64(data)+uint64(pageSize) > uint64(size) {
				ok = false
				break
			}
			ent.pages = append(ent.pages, hvlePage{off: pageOff, data: e[data : data+int(pageSize)]})
			data += int(pageSize)
		}
		if !ok {
			break
		}
		if ent.seq >= first {
			out = append(out, ent)
		}
		off += size
	}
	return out
}

func replayHvLE(primary []byte, logs [][]byte) ([]byte, ReplayResult, error) {
	type chain struct {
		base    uint32
		entries []hvleEntry
	}
	var chains []chain
	for _, l := range logs {
		if es := hvleEntries(l); len(es) > 0 {
			chains = append(chains, chain{base: binary.LittleEndian.Uint32(l[4:]), entries: es})
		}
	}
	// 两个日志轮换写，LOG1 不一定更早：按各自首项序号排序。
	sort.Slice(chains, func(i, j int) bool { return chains[i].entries[0].seq < chains[j].entries[0].seq })
	out := append([]byte(nil), primary...)
	secondary := binary.LittleEndian.Uint32(primary[8:])
	flags := binary.LittleEndian.Uint32(primary[144:])
	var last *hvleEntry
	applied := 0
	for ci, c := range chains {
		for i := range c.entries {
			e := &c.entries[i]
			if last == nil {
				// 首项必须等于所在日志基块的主序号，且不早于主文件的次序号。
				if ci != 0 || i != 0 || e.seq != c.base || e.seq < secondary {
					break
				}
			} else if e.seq != last.seq+1 {
				break
			}
			if need := baseBlockSize + int(e.bins); len(out) < need {
				out = append(out, make([]byte, need-len(out))...)
			}
			for _, p := range e.pages {
				copy(out[baseBlockSize+int(p.off):], p.data)
			}
			flags = flags&^1 | e.flags&1
			last = e
			applied++
		}
		if last == nil {
			break
		}
	}
	if last == nil {
		return nil, ReplayResult{}, fmt.Errorf("注册表事务日志中没有可接续的日志项")
	}
	out = out[:baseBlockSize+int(last.bins)]
	binary.LittleEndian.PutUint32(out[40:], last.bins)
	binary.LittleEndian.PutUint32(out[144:], flags)
	finishBase(out, last.seq)
	return out, ReplayResult{Format: "HvLE", Applied: applied, Seq: last.seq}, nil
}

// replayDIRT 应用一个旧格式日志：基块时间戳必须与主文件一致，脏页按位图顺序依次存放。
func replayDIRT(primary, l []byte) ([]byte, ReplayResult, error) {
	if !bytes.Equal(l[12:20], primary[12:20]) {
		return nil, ReplayResult{}, fmt.Errorf("事务日志与注册表文件不匹配")
	}
	bins := binary.LittleEndian.Uint32(l[40:])
	if bins == 0 || bins%hiveBinAlign != 0 || bins > maxHiveBins {
		return nil, ReplayResult{}, fmt.Errorf("事务日志的数据区大小无效")
	}
	pages := int(bins / logSector)
	vector := logSector
	bitmapEnd := vector + 4 + (pages+7)/8
	if len(l) < bitmapEnd || string(l[vector:vector+4]) != "DIRT" {
		return nil, ReplayResult{}, fmt.Errorf("事务日志缺少脏页位图")
	}
	bitmap := l[vector+4 : bitmapEnd]
	data := (bitmapEnd + logSector - 1) / logSector * logSector
	out := append([]byte(nil), primary...)
	if need := baseBlockSize + int(bins); len(out) < need {
		out = append(out, make([]byte, need-len(out))...)
	}
	out = out[:baseBlockSize+int(bins)]
	for bit := 0; bit < pages; bit++ {
		if bitmap[bit/8]&(1<<(bit%8)) == 0 {
			continue
		}
		if data+logSector > len(l) {
			return nil, ReplayResult{}, fmt.Errorf("事务日志的脏页不完整")
		}
		copy(out[baseBlockSize+bit*logSector:], l[data:data+logSector])
		data += logSector
	}
	// 基块取日志里的备份（即写主文件时内存中的基块），文件类型改回主文件。
	copy(out[:logSector], l[:logSector])
	binary.LittleEndian.PutUint32(out[28:], 0)
	seq := binary.LittleEndian.Uint32(l[4:])
	finishBase(out, seq)
	return out, ReplayResult{Format: "DIRT", Applied: 1, Seq: seq}, nil
}

// finishBase 把两个序号置为相同的 seq 并重算校验和，主文件由此变为干净。
func finishBase(b []byte, seq uint32) {
	binary.LittleEndian.PutUint32(b[4:], seq)
	binary.LittleEndian.PutUint32(b[8:], seq)
	binary.LittleEndian.PutUint32(b[508:], checksum(b))
}

// checksum 是基块前 508 字节的 XOR-32，0xFFFFFFFF 记作 0xFFFFFFFE，0 记作 1。
func checksum(b []byte) uint32 {
	var c uint32
	for i := 0; i < 508; i += 4 {
		c ^= binary.LittleEndian.Uint32(b[i:])
	}
	switch c {
	case 0xFFFFFFFF:
		return 0xFFFFFFFE
	case 0:
		return 1
	}
	return c
}

// marvin32 是日志项哈希所用的 Marvin32，种子按数值 0x82EF4D887A4E55C5 取（低 32 位为 p0），已用真实日志核对。
func marvin32(data []byte) uint64 {
	var seed uint64 = 0x82EF4D887A4E55C5
	p0, p1 := uint32(seed), uint32(seed>>32)
	block := func() {
		p1 ^= p0
		p0 = bits.RotateLeft32(p0, 20)
		p0 += p1
		p1 = bits.RotateLeft32(p1, 9)
		p1 ^= p0
		p0 = bits.RotateLeft32(p0, 27)
		p0 += p1
		p1 = bits.RotateLeft32(p1, 19)
	}
	for len(data) >= 4 {
		p0 += binary.LittleEndian.Uint32(data)
		block()
		data = data[4:]
	}
	final := uint32(0x80)
	switch len(data) {
	case 1:
		final = 0x8000 | uint32(data[0])
	case 2:
		final = 0x800000 | uint32(data[0]) | uint32(data[1])<<8
	case 3:
		final = 0x80000000 | uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	}
	p0 += final
	block()
	block()
	return uint64(p1)<<32 | uint64(p0)
}
