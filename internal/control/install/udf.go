package install

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf16"
)

// 只读 UDF（ECMA-167）：按目录找到文件在 ISO 里的区段，用来直接读 install.wim 末尾的版本 XML。
// 只认微软 ISO 用到的子集：2048 字节块、单个普通分区、short/long/内嵌分配描述符；
// 其它结构一律报错，调用方退回 7z。

const udfSector = 2048

// extent 是文件的一段数据在 ISO 里的字节偏移与长度，按文件内顺序排列。
type extent struct{ off, n int64 }

type udfVolume struct {
	r         io.ReaderAt
	partStart int64  // 分区起始扇区
	root      uint32 // 根目录 File Entry 的逻辑块号（分区内）
}

type udfEntry struct {
	name string
	icb  uint32
}

func readUDFSector(r io.ReaderAt, sector int64) ([]byte, error) {
	b := make([]byte, udfSector)
	if _, err := r.ReadAt(b, sector*udfSector); err != nil {
		return nil, err
	}
	return b, nil
}

// udfTag 校验描述符标签：类型与标签校验和（ECMA-167 3/7.2）。
func udfTag(b []byte, want uint16) error {
	if len(b) < 16 {
		return errors.New("UDF 描述符不完整")
	}
	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += b[i]
		}
	}
	if sum != b[4] {
		return errors.New("UDF 描述符校验和不符")
	}
	if id := binary.LittleEndian.Uint16(b); id != want {
		return fmt.Errorf("UDF 描述符类型 %d，应为 %d", id, want)
	}
	return nil
}

func openUDF(r io.ReaderAt) (*udfVolume, error) {
	avdp, err := readUDFSector(r, 256)
	if err != nil {
		return nil, err
	}
	if err := udfTag(avdp, 2); err != nil {
		return nil, err
	}
	vdsLen, vdsLoc := binary.LittleEndian.Uint32(avdp[16:]), binary.LittleEndian.Uint32(avdp[20:])
	partStart, partNum, mapPart := int64(-1), -1, -2
	var fsd uint32
	for i := uint32(0); i < vdsLen/udfSector && i < 64; i++ {
		b, err := readUDFSector(r, int64(vdsLoc+i))
		if err != nil {
			return nil, err
		}
		id := binary.LittleEndian.Uint16(b)
		if err := udfTag(b, id); err != nil {
			return nil, err
		}
		if id == 8 { // 终止描述符
			break
		}
		switch id {
		case 5: // 分区描述符
			partNum = int(binary.LittleEndian.Uint16(b[22:]))
			partStart = int64(binary.LittleEndian.Uint32(b[188:]))
		case 6: // 逻辑卷描述符
			if bs := binary.LittleEndian.Uint32(b[212:]); bs != udfSector {
				return nil, fmt.Errorf("UDF 块大小 %d 不受支持", bs)
			}
			// 文件集描述符位置（long_ad）只认 0 号分区引用；分区映射只认一个 1 型。
			if binary.LittleEndian.Uint16(b[256:]) != 0 || binary.LittleEndian.Uint32(b[268:]) != 1 || b[440] != 1 {
				return nil, errors.New("UDF 分区映射不受支持")
			}
			fsd = binary.LittleEndian.Uint32(b[252:])
			mapPart = int(binary.LittleEndian.Uint16(b[444:]))
		}
	}
	if partStart < 0 || mapPart != partNum {
		return nil, errors.New("UDF 卷描述符不完整")
	}
	v := &udfVolume{r: r, partStart: partStart}
	b, err := v.block(fsd)
	if err != nil {
		return nil, err
	}
	if err := udfTag(b, 256); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint16(b[408:]) != 0 {
		return nil, errors.New("UDF 根目录不在 0 号分区")
	}
	v.root = binary.LittleEndian.Uint32(b[404:])
	return v, nil
}

func (v *udfVolume) block(lbn uint32) ([]byte, error) {
	return readUDFSector(v.r, v.partStart+int64(lbn))
}

// entry 读 File Entry / Extended File Entry：返回是否目录、数据区段、内嵌数据和文件长度。
func (v *udfVolume) entry(lbn uint32) (dir bool, ext []extent, embedded []byte, size int64, err error) {
	b, err := v.block(lbn)
	if err != nil {
		return false, nil, nil, 0, err
	}
	var lea, lad, base int
	switch id := binary.LittleEndian.Uint16(b); id {
	case 261:
		lea, lad, base = int(binary.LittleEndian.Uint32(b[168:])), int(binary.LittleEndian.Uint32(b[172:])), 176
	case 266:
		lea, lad, base = int(binary.LittleEndian.Uint32(b[208:])), int(binary.LittleEndian.Uint32(b[212:])), 216
	default:
		return false, nil, nil, 0, fmt.Errorf("UDF 文件项类型 %d 不受支持", id)
	}
	if err := udfTag(b, binary.LittleEndian.Uint16(b)); err != nil {
		return false, nil, nil, 0, err
	}
	if strategy := binary.LittleEndian.Uint16(b[20:]); strategy != 4 {
		return false, nil, nil, 0, fmt.Errorf("UDF ICB 策略 %d 不受支持", strategy)
	}
	dir = b[27] == 4
	size = int64(binary.LittleEndian.Uint64(b[56:]))
	start := base + lea
	if lea < 0 || lad < 0 || start+lad > len(b) || size < 0 {
		return false, nil, nil, 0, errors.New("UDF 文件项长度无效")
	}
	ads := b[start : start+lad]
	var step int
	switch binary.LittleEndian.Uint16(b[34:]) & 7 {
	case 0:
		step = 8
	case 1:
		step = 16
	case 3:
		if int64(lad) < size {
			return false, nil, nil, 0, errors.New("UDF 内嵌数据不完整")
		}
		return dir, nil, ads[:size], size, nil
	default:
		return false, nil, nil, 0, errors.New("UDF 分配描述符类型不受支持")
	}
	var total int64
	for p := 0; p+step <= len(ads) && total < size; p += step {
		l := binary.LittleEndian.Uint32(ads[p:])
		n := int64(l & 0x3fffffff)
		if n == 0 {
			break
		}
		// 只认已记录的区段；未记录（稀疏）和续接描述符都不认。
		if l>>30 != 0 || (step == 16 && binary.LittleEndian.Uint16(ads[p+8:]) != 0) {
			return false, nil, nil, 0, errors.New("UDF 区段类型不受支持")
		}
		lbn := binary.LittleEndian.Uint32(ads[p+4:])
		ext = append(ext, extent{off: (v.partStart + int64(lbn)) * udfSector, n: n})
		total += n
	}
	if total < size {
		return false, nil, nil, 0, errors.New("UDF 区段不足以覆盖文件长度")
	}
	if over := total - size; over > 0 {
		ext[len(ext)-1].n -= over
	}
	return dir, ext, nil, size, nil
}

func (v *udfVolume) readDir(lbn uint32) ([]udfEntry, error) {
	dir, ext, data, size, err := v.entry(lbn)
	if err != nil {
		return nil, err
	}
	if !dir {
		return nil, errors.New("UDF 路径中间不是目录")
	}
	if data == nil {
		if size > 16<<20 {
			return nil, errors.New("UDF 目录过大")
		}
		data = make([]byte, size)
		if _, err := (extentReader{r: v.r, ext: ext}).ReadAt(data, 0); err != nil {
			return nil, err
		}
	}
	var out []udfEntry
	for p := 0; p+38 <= len(data); {
		fid := data[p:]
		if err := udfTag(fid, 257); err != nil {
			return nil, err
		}
		chars, lfi, liu := fid[18], int(fid[19]), int(binary.LittleEndian.Uint16(fid[36:]))
		end := 38 + liu + lfi
		if end > len(fid) {
			return nil, errors.New("UDF 目录项越界")
		}
		// 跳过父目录项（0x08）与已删除项（0x04）。
		if chars&0x0c == 0 && lfi > 0 {
			out = append(out, udfEntry{name: udfName(fid[38+liu : end]), icb: binary.LittleEndian.Uint32(fid[24:])})
		}
		p += (end + 3) &^ 3
	}
	return out, nil
}

// udfName 解 OSTA CS0 文件名：首字节 8 为单字节字符，16 为 UTF-16 大端。
func udfName(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	switch b[0] {
	case 8:
		r := make([]rune, len(b)-1)
		for i, c := range b[1:] {
			r[i] = rune(c)
		}
		return string(r)
	case 16:
		u := make([]uint16, (len(b)-1)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[1+2*i:])
		}
		return string(utf16.Decode(u))
	}
	return ""
}

// lookup 按路径（不分大小写）找文件，返回数据区段与长度。
func (v *udfVolume) lookup(name string) ([]extent, int64, error) {
	lbn := v.root
	for _, part := range strings.Split(strings.Trim(path.Clean("/"+name), "/"), "/") {
		entries, err := v.readDir(lbn)
		if err != nil {
			return nil, 0, err
		}
		found := false
		for _, e := range entries {
			if strings.EqualFold(e.name, part) {
				lbn, found = e.icb, true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf("UDF 里没有 %s", name)
		}
	}
	dir, ext, embedded, size, err := v.entry(lbn)
	if err != nil {
		return nil, 0, err
	}
	if dir || embedded != nil {
		return nil, 0, fmt.Errorf("UDF 里的 %s 不是普通文件", name)
	}
	return ext, size, nil
}

// extentReader 把文件内偏移映射到 ISO 里的各个区段。
type extentReader struct {
	r   io.ReaderAt
	ext []extent
}

func (e extentReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for _, x := range e.ext {
		if n == len(p) {
			break
		}
		if off >= x.n {
			off -= x.n
			continue
		}
		chunk := min(int64(len(p)-n), x.n-off)
		m, err := e.r.ReadAt(p[n:n+int(chunk)], x.off+off)
		n += m
		if err != nil {
			return n, err
		}
		off = 0
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
