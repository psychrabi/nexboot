package install

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
)

type Inspector struct {
	ImportDir func(context.Context) (string, error)
}

func (i Inspector) Inspect(ctx context.Context, source string) (Metadata, error) {
	if i.ImportDir == nil {
		return Metadata{}, errs.Invalid("请先创建存储池：上传和导入的文件存放在存储池的 imports 目录")
	}
	dir, err := i.ImportDir(ctx)
	if err != nil {
		return Metadata{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return Metadata{}, err
	}
	source, err = filepath.EvalSymlinks(strings.TrimSpace(source))
	if err != nil {
		return Metadata{}, errs.Invalid("安装文件不存在，请重新选择 ISO")
	}
	rel, err := filepath.Rel(dir, source)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return Metadata{}, errs.Invalid("请将 ISO 放在导入目录（存储池上的 imports）内再选择")
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(source), ".iso") {
		return Metadata{}, errs.Invalid("请选择导入目录中的 ISO 安装文件")
	}
	key := inspectKey{path: source, size: info.Size(), mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		key.ino = uint64(st.Ino)
	}
	if meta, ok := inspected.get(key); ok {
		return meta, nil
	}
	meta, err := inspectFile(ctx, source, info)
	if err != nil {
		return Metadata{}, err
	}
	meta.Fingerprint = key.fingerprint()
	inspected.put(key, meta)
	return meta, nil
}

// inspectFile 只读 ISO 目录和识别要用的几个文件，不整张读；结果由 Inspect 按文件身份缓存。
// 整张的校验值在创建安装任务复制 ISO 时顺带算。
func inspectFile(ctx context.Context, source string, info os.FileInfo) (Metadata, error) {
	meta := Metadata{Path: source, SizeBytes: info.Size(), Architecture: "amd64", Editions: []Edition{}}
	entries, err := ArchiveEntries(ctx, source)
	if err != nil {
		return Metadata{}, err
	}
	lookup := map[string]string{}
	for _, entry := range entries {
		lookup[strings.ToLower(entry)] = entry
	}
	if _, ok := lookup[".treeinfo"]; ok {
		return inspectAnaconda(ctx, source, lookup, meta)
	}
	if diskInfo, ok := lookup[".disk/info"]; ok {
		data, err := ReadArchiveEntry(ctx, source, diskInfo, 16<<10)
		if err != nil {
			return Metadata{}, err
		}
		label := strings.TrimSpace(string(data))
		if !strings.HasPrefix(label, "Ubuntu") || !strings.Contains(strings.ToLower(label), "amd64") {
			return Metadata{}, errs.Invalid("首期仅支持 Ubuntu x64 官方安装 ISO")
		}
		version := regexp.MustCompile(`\b24\.04(?:\.\d+)?\b`).FindString(label)
		if version == "" {
			return Metadata{}, errs.Invalid("首期仅支持 Ubuntu 24.04 LTS 桌面版和服务器版 ISO")
		}
		if _, ok := lookup["casper/vmlinuz"]; !ok {
			return Metadata{}, errs.Invalid("Ubuntu ISO 缺少安装内核，请使用官方桌面版或服务器版安装文件")
		}
		if _, ok := lookup["casper/initrd"]; !ok {
			return Metadata{}, errs.Invalid("Ubuntu ISO 缺少安装 initrd，请重新下载官方安装文件")
		}
		meta.OSType = domain.OSTypeLinux
		meta.Version = version
		meta.Distribution = "ubuntu-desktop"
		meta.RecommendedSizeGiB = 64
		if strings.Contains(strings.ToLower(label), "server") {
			meta.Distribution = "ubuntu-server"
			meta.RecommendedSizeGiB = 32
		}
		if err := checkUbuntuOfflinePackages(ctx, source, lookup, meta.Distribution); err != nil {
			return Metadata{}, err
		}
		return meta, nil
	}
	installFile, ok := lookup["sources/install.wim"]
	if !ok {
		installFile, ok = lookup["sources/install.esd"]
	}
	if !ok {
		return Metadata{}, errs.Invalid("无法识别受支持的安装系统，请使用 Windows 10/11、Windows Server 2016 及以上、Ubuntu 24.04 LTS 或 RHEL 系 7/8/9 x86_64 完整 DVD ISO")
	}
	if _, ok := lookup["efi/boot/bootx64.efi"]; !ok {
		return Metadata{}, errs.Invalid("安装文件不支持 x64 UEFI 启动")
	}
	editions, err := readWIMEditions(ctx, source, installFile)
	if err != nil {
		return Metadata{}, err
	}
	if len(editions) == 0 {
		return Metadata{}, errs.Invalid("安装文件中没有受支持的 x64 版本（Windows 10/11 或 Windows Server 2016 及以上）")
	}
	meta.OSType = domain.OSTypeWindows
	meta.Distribution = "windows"
	meta.RecommendedSizeGiB = 64
	meta.Editions = editions
	meta.Version = windowsVersionLabel(editions)
	return meta, nil
}

// inspectKey 认定「同一个文件」：路径、大小、修改时间、inode 都不变。
// 文件身份：同一路径下大小、修改时间、文件编号都没变，就当作同一个文件。
type inspectKey struct {
	path  string
	size  int64
	mtime int64
	ino   uint64
}

func (k inspectKey) fingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%d\n%d", k.path, k.size, k.mtime, k.ino)))
	return hex.EncodeToString(sum[:16])
}

type inspectCache struct {
	mu    sync.Mutex
	items map[inspectKey]Metadata
}

var inspected = &inspectCache{items: map[inspectKey]Metadata{}}

func (c *inspectCache) get(k inspectKey) (Metadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.items[k]
	m.Editions = append([]Edition(nil), m.Editions...)
	return m, ok
}

func (c *inspectCache) put(k inspectKey, m Metadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 导入目录里的 ISO 不多；同一路径的旧结果直接丢掉，免得文件换了还越攒越多。
	for old := range c.items {
		if old.path == k.path || len(c.items) >= 32 {
			delete(c.items, old)
		}
	}
	m.Editions = append([]Edition(nil), m.Editions...)
	c.items[k] = m
}

// 安装不联网：装出来的系统必须自带无盘启动要用的包，缺了就在识别时拦下。
var ubuntuDisklessPackages = []string{"open-iscsi", "curl"}

func checkUbuntuOfflinePackages(ctx context.Context, source string, lookup map[string]string, distribution string) error {
	unknown := errs.Invalid("请改用 Ubuntu Server 24.04 官方 ISO：无法确认这张 ISO 装出来的系统带有 open-iscsi，无盘启动需要它，安装过程也不联网补装")
	entry, ok := lookup["casper/install-sources.yaml"]
	if !ok {
		return unknown
	}
	data, err := ReadArchiveEntry(ctx, source, entry, 1<<20)
	if err != nil {
		return err
	}
	// 与应答文件一致：桌面版装 ubuntu-desktop，服务器版装默认来源。
	path := ""
	for _, src := range parseInstallSources(data) {
		if (distribution == "ubuntu-desktop" && src.id == "ubuntu-desktop") || (distribution != "ubuntu-desktop" && src.def) {
			path = src.path
			break
		}
	}
	if !strings.HasSuffix(path, ".squashfs") {
		return unknown
	}
	installed := map[string]bool{}
	parts := strings.Split(strings.TrimSuffix(path, ".squashfs"), ".")
	for i := range parts {
		entry, ok := lookup["casper/"+strings.ToLower(strings.Join(parts[:i+1], "."))+".manifest"]
		if !ok {
			return unknown
		}
		data, err := ReadArchiveEntry(ctx, source, entry, 8<<20)
		if err != nil {
			return err
		}
		applyManifest(installed, data)
	}
	var missing []string
	for _, name := range ubuntuDisklessPackages {
		if !installed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return errs.Invalid("请改用 Ubuntu Server 24.04 官方 ISO：这张 ISO 装出来的系统缺少 " + strings.Join(missing, "、") + "，无盘启动需要它们，安装过程也不联网补装")
	}
	return nil
}

type installSource struct {
	id, path string
	def      bool
}

// parseInstallSources 只读 casper/install-sources.yaml 顶层条目的 id、path、default。
func parseInstallSources(data []byte) []installSource {
	var out []installSource
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		if strings.HasPrefix(line, "- ") {
			out = append(out, installSource{})
			line = "  " + line[2:]
		}
		if len(out) == 0 || !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		cur := &out[len(out)-1]
		switch key {
		case "id":
			cur.id = value
		case "path":
			cur.path = value
		case "default":
			cur.def = value == "true"
		}
	}
	return out
}

// applyManifest 叠加一层清单：分层 ISO 的清单是相对下一层的 diff（+ 加、- 减），整份清单每行一个包。
func applyManifest(installed map[string]bool, data []byte) {
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
			continue
		}
		remove := line[0] == '-'
		if line[0] == '+' || line[0] == '-' || line[0] == ' ' {
			line = line[1:]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name, _, _ := strings.Cut(fields[0], ":")
		installed[name] = !remove
	}
}

// windowsVersionLabel 是向导和镜像上显示的系统名；Server 按内部版本号对应发行年份。
func windowsVersionLabel(editions []Edition) string {
	first := editions[0]
	if strings.Contains(strings.ToLower(first.Name), "server") {
		if year, ok := map[int]string{14393: "2016", 17763: "2019", 20348: "2022", 26100: "2025"}[first.Build]; ok {
			return "Windows Server " + year
		}
		return "Windows Server"
	}
	if first.Build >= 22000 {
		return "Windows 11"
	}
	return "Windows 10"
}

// ArchiveEntries uses 7z for both ISO9660 and UDF media; every extracted member must come from this validated list.
func ArchiveEntries(ctx context.Context, source string) ([]string, error) {
	data, err := commandOutput(ctx, 8<<20, "7z", "l", "-slt", "--", source)
	if err != nil {
		return nil, fmt.Errorf("请安装 7z 并检查 ISO 文件是否完整：%w", err)
	}
	text := string(data)
	if at := strings.Index(text, "----------"); at >= 0 {
		text = text[at+10:]
	} else {
		return nil, errs.Invalid("无法读取 ISO 文件目录")
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "Path = ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "Path = "))
		name = strings.ReplaceAll(name, "\\", "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." || strings.ContainsAny(name, "\x00\r\n") {
			return nil, errs.Invalid("ISO 包含不安全的文件路径，请使用官方安装文件")
		}
		out = append(out, name)
	}
	return out, nil
}
func ReadArchiveEntry(ctx context.Context, source, entry string, maxBytes int64) ([]byte, error) {
	return commandOutput(ctx, maxBytes, "7z", "x", "-so", "--", source, entry)
}
func commandOutput(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if readErr != nil || int64(len(data)) > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("安装介质输出超过允许大小")
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return data, nil
}
func readWIMEditions(ctx context.Context, source, entry string) ([]Edition, error) {
	if raw, err := wimXMLFromUDF(source, entry); err == nil {
		return parseWIMEditions(raw)
	}
	// 兜底：7z 从头流式读到版本 XML，要读完几乎整个 install.wim，大 ISO 要几分钟。
	cmd := exec.CommandContext(ctx, "7z", "x", "-so", "--", source, entry)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	header := make([]byte, 208)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, errs.Invalid("Windows 安装映像头部损坏，请重新下载 ISO")
	}
	offset, size, err := wimXMLLocation(header)
	if err != nil {
		return nil, err
	}
	if _, err := io.CopyN(io.Discard, r, offset-208); err != nil {
		return nil, errs.Invalid("Windows 安装映像不完整")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	return parseWIMEditions(decodeWIMXML(raw))
}

// wimXMLFromUDF 按 ISO 的 UDF 目录定位 install.wim，只读头部和末尾的版本 XML（几 KB 到几百 KB）。
func wimXMLFromUDF(source, entry string) ([]byte, error) {
	f, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	v, err := openUDF(f)
	if err != nil {
		return nil, err
	}
	ext, size, err := v.lookup(entry)
	if err != nil {
		return nil, err
	}
	r := io.NewSectionReader(extentReader{r: f, ext: ext}, 0, size)
	header := make([]byte, 208)
	if _, err := r.ReadAt(header, 0); err != nil {
		return nil, err
	}
	offset, n, err := wimXMLLocation(header)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, n)
	if _, err := r.ReadAt(raw, offset); err != nil {
		return nil, err
	}
	return decodeWIMXML(raw), nil
}

// wimXMLLocation 从 WIM 头取版本 XML 资源的偏移与长度。
func wimXMLLocation(header []byte) (offset, size int64, err error) {
	if len(header) < 208 || string(header[:8]) != "MSWIM\x00\x00\x00" {
		return 0, 0, errs.Invalid("Windows 安装映像头部损坏，请重新下载 ISO")
	}
	n := binary.LittleEndian.Uint64(header[0x48:]) & 0x00ffffffffffffff
	off := binary.LittleEndian.Uint64(header[0x50:])
	if n == 0 || n > 16<<20 || off < 208 || off > 64<<30 || header[0x4f]&0x04 != 0 {
		return 0, 0, errs.Invalid("Windows 安装映像元数据无效或不受支持")
	}
	return int64(off), int64(n), nil
}

// decodeWIMXML 把带 BOM 的 UTF-16LE 转成 UTF-8。
func decodeWIMXML(raw []byte) []byte {
	if len(raw) > 1 && raw[0] == 0xff && raw[1] == 0xfe {
		u := make([]uint16, (len(raw)-2)/2)
		for n := range u {
			u[n] = binary.LittleEndian.Uint16(raw[2+n*2:])
		}
		return []byte(string(utf16.Decode(u)))
	}
	return raw
}

func parseWIMEditions(raw []byte) ([]Edition, error) {
	var doc struct {
		Images []struct {
			Index   int    `xml:"INDEX,attr"`
			Name    string `xml:"NAME"`
			Windows struct {
				Arch             string `xml:"ARCH"`
				InstallationType string `xml:"INSTALLATIONTYPE"`
				Languages        struct {
					Language []string `xml:"LANGUAGE"`
					Default  string   `xml:"DEFAULT"`
				} `xml:"LANGUAGES"`
				Version struct {
					Build    string `xml:"BUILD"`
					Revision int    `xml:"SPBUILD"`
				} `xml:"VERSION"`
			} `xml:"WINDOWS"`
		} `xml:"IMAGE"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, errs.Invalid("Windows 安装映像元数据无法解析")
	}
	var editions []Edition
	for _, img := range doc.Images {
		build, _ := strconv.Atoi(img.Windows.Version.Build)
		if img.Windows.Arch != "9" || build < 10240 {
			continue
		}
		warning := ""
		if (build == 26100 || build == 26200) && img.Windows.Version.Revision < 7705 {
			if img.Windows.Version.Revision <= 0 {
				warning = fmt.Sprintf("无法确认 Windows 11（构建 %d）的修订版本。可以继续安装，兼容性以实际无盘启动验证为准。", build)
			} else {
				warning = fmt.Sprintf("检测到 Windows 11 版本 %d.%d，可能存在 iSCSI 启动兼容性问题；建议使用 %d.7705 或更新版本。可以继续安装，兼容性以实际无盘启动验证为准。", build, img.Windows.Version.Revision, build)
			}
		}
		language := strings.TrimSpace(img.Windows.Languages.Default)
		if language == "" && len(img.Windows.Languages.Language) > 0 {
			language = strings.TrimSpace(img.Windows.Languages.Language[0])
		}
		editions = append(editions, Edition{Index: img.Index, Name: img.Name, Build: build, Revision: img.Windows.Version.Revision, Warning: warning, Language: language,
			Core: strings.EqualFold(strings.TrimSpace(img.Windows.InstallationType), "Server Core")})
	}
	return editions, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
