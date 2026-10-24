package install

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// testdata/windows-udf.iso.gz：macOS `hdiutil makehybrid -udf -udf-version 1.02` 生成（与微软 ISO 同为 UDF 1.02），
// 内含 Sources/Install.wim：208 字节 WIM 头 + 3 MiB 填充 + 末尾 UTF-16 版本 XML。
func udfFixture(t *testing.T) string {
	t.Helper()
	f, err := os.Open("testdata/windows-udf.iso.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), "win.iso")
	out, err := os.Create(iso)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, zr); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return iso
}

func wantFixtureEdition(t *testing.T, editions []Edition, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].Index != 1 || editions[0].Name != "Windows 11 专业版" || editions[0].Build != 22631 || editions[0].Revision != 2861 || editions[0].Language != "zh-CN" {
		t.Fatalf("editions = %+v", editions)
	}
}

// 版本列表在 install.wim 末尾：按 UDF 目录直接定位读那几 KB，不经 7z 把几个 G 的 install.wim 从头读一遍。
func TestReadWIMEditionsLocatesTheImageInUDF(t *testing.T) {
	iso := udfFixture(t)
	t.Setenv("PATH", t.TempDir()) // 没有 7z，只能直读
	for _, entry := range []string{"Sources/Install.wim", "sources/install.wim"} {
		editions, err := readWIMEditions(context.Background(), iso, entry)
		wantFixtureEdition(t, editions, err)
	}
	if _, err := wimXMLFromUDF(iso, "sources/missing.wim"); err == nil {
		t.Fatal("missing file must fail so the caller falls back to 7z")
	}
}

// 读不了 UDF（不是 UDF、结构不认识）时退回 7z 流式读，结果一样。
func TestReadWIMEditionsFallsBackTo7z(t *testing.T) {
	dir := t.TempDir()
	wim := filepath.Join(dir, "install.wim")
	if err := os.WriteFile(wim, syntheticWIM(fixtureXML, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	notUDF := filepath.Join(dir, "plain.iso")
	if err := os.WriteFile(notUDF, []byte("not a UDF image"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte("#!/bin/sh\nexec cat '"+wim+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	editions, err := readWIMEditions(ctx, notUDF, "sources/install.wim")
	wantFixtureEdition(t, editions, err)
}

const fixtureXML = "<WIM><IMAGE INDEX=\"1\"><NAME>Windows 11 专业版</NAME><WINDOWS><ARCH>9</ARCH><INSTALLATIONTYPE>Client</INSTALLATIONTYPE>" +
	"<LANGUAGES><LANGUAGE>zh-CN</LANGUAGE><DEFAULT>zh-CN</DEFAULT></LANGUAGES><VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>22631</BUILD><SPBUILD>2861</SPBUILD></VERSION></WINDOWS></IMAGE></WIM>"

// syntheticWIM：WIM 头 + 填充 + 末尾 UTF-16LE（带 BOM）的版本 XML，与真实 install.wim 的布局相同。
func syntheticWIM(xmlText string, body int) []byte {
	u := utf16.Encode([]rune("\ufeff" + xmlText))
	raw := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(raw[2*i:], c)
	}
	out := make([]byte, body, body+len(raw))
	copy(out, "MSWIM\x00\x00\x00")
	binary.LittleEndian.PutUint64(out[0x48:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(out[0x50:], uint64(body))
	return append(out, raw...)
}

// 真实 install.wim 有几个 G，跨好几个 UDF 区段：文件内偏移要按区段顺序映射。
func TestExtentReaderMapsAcrossExtents(t *testing.T) {
	disk := bytes.NewReader([]byte("0123456789abcdefghij"))
	r := extentReader{r: disk, ext: []extent{{off: 10, n: 5}, {off: 2, n: 3}, {off: 15, n: 5}}} // 文件内容 abcde234fghij
	buf := make([]byte, 6)
	if n, err := r.ReadAt(buf, 3); err != nil || string(buf[:n]) != "de234f" {
		t.Fatalf("ReadAt = %q, %v", buf[:n], err)
	}
	if n, err := r.ReadAt(buf, 10); err != io.EOF || string(buf[:n]) != "hij" {
		t.Fatalf("read past end = %q, %v", buf[:n], err)
	}
}

// 真机核对：ND_TEST_WINDOWS_ISO 填一个或多个（冒号分隔）微软官方 ISO，打出版本、XML 位置与摘要，
// 再与内核 UDF 挂载后读到的同一段字节比对。
func TestRealWindowsISOEditionsFromUDF(t *testing.T) {
	list := os.Getenv("ND_TEST_WINDOWS_ISO")
	if list == "" {
		t.Skip("set ND_TEST_WINDOWS_ISO to real Windows ISOs")
	}
	for _, iso := range strings.Split(list, ":") {
		started := time.Now()
		raw, err := wimXMLFromUDF(iso, "sources/install.wim")
		if err != nil {
			raw, err = wimXMLFromUDF(iso, "sources/install.esd")
		}
		if err != nil {
			t.Fatalf("%s: %v", iso, err)
		}
		took := time.Since(started)
		editions, err := parseWIMEditions(raw)
		if err != nil || len(editions) == 0 {
			t.Fatalf("%s: editions %v %v", iso, editions, err)
		}
		t.Logf("%s: %d editions in %v, xml %d bytes sha256 %x", filepath.Base(iso), len(editions), took, len(raw), sha256.Sum256(raw))
		for _, e := range editions {
			t.Logf("  %d %s build %d.%d %s core=%v", e.Index, e.Name, e.Build, e.Revision, e.Language, e.Core)
		}
	}
}
