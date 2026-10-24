package adapt

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"github.com/tianwei/diskless/internal/storage"
	"strings"
)

//go:embed mount-disks.ps1
var mountDisksScript []byte

//go:embed nd-datadisks.sh
var linuxMountScript []byte

const mountScriptPortPlaceholder = "__ND_API_PORT__"

// RenderMountScript 把 API 端口写进盘符开机脚本（服务器 IP 由脚本从 iSCSI 会话取得，
// 只需写端口）。由镜像导入与客户机开机复用。
func RenderMountScript(apiPort string) []byte {
	return bytes.ReplaceAll(mountDisksScript, []byte(mountScriptPortPlaceholder), []byte(normalPort(apiPort)))
}

// RenderLinuxMountScript 生成 Linux 镜像的数据盘挂载脚本；服务器地址从 iBFT 读取，只需写端口。
func RenderLinuxMountScript(apiPort string) []byte {
	return bytes.ReplaceAll(linuxMountScript, []byte(mountScriptPortPlaceholder), []byte(normalPort(apiPort)))
}

// LinuxMountScriptVersion 是 Linux 烘焙版的 MountScriptVersion。
func LinuxMountScriptVersion(apiPort string) string {
	sum := sha256.Sum256(append(RenderLinuxMountScript(apiPort), storage.LinuxLayoutID()...))
	return hex.EncodeToString(sum[:])[:16]
}

func normalPort(apiPort string) string {
	if port := strings.TrimSpace(apiPort); port != "" {
		return port
	}
	return "8080"
}

// MountScriptVersion 标识一次烘焙注入。镜像记录烘焙时的版本，开机路径比对字符串即可
// 判断克隆是否已带启动器，不必挂载每台客户机的 NTFS 分区。版本为空或过期即视为未烘焙，退回逐台注入。
// 启动器开机时向服务器取最新的挂盘脚本，所以版本只看布局和端口：挂盘脚本改了不让已烘焙镜像失效；
// 端口算进来，是因为启动器从烘焙的副本里读端口。
func MountScriptVersion(apiPort string) string {
	sum := sha256.Sum256([]byte(storage.MountScriptLayoutID() + "\x00" + normalPort(apiPort)))
	return hex.EncodeToString(sum[:])[:16]
}
