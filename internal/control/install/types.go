// Package install creates a diskless system image from installation media on an isolated work disk.
package install

import (
	"context"
	"sync"
	"time"

	"github.com/tianwei/diskless/internal/boot"
	"github.com/tianwei/diskless/internal/control/assets"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

type Edition struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Build    int    `json:"build"`
	Revision int    `json:"revision"`
	Warning  string `json:"warning,omitempty"`
	// Language 是该版本的默认语言（如 zh-CN），首次初始化据此跳过地区、键盘页。
	Language string `json:"language,omitempty"`
	// Core 是 Windows Server Core（无图形界面），向导里标出来避免误选。
	Core bool `json:"core,omitempty"`
}
type Metadata struct {
	Path string `json:"path"`
	// Fingerprint 由大小、修改时间和文件编号算出，识别到开始安装之间据此判断文件没被换掉。
	Fingerprint        string        `json:"fingerprint"`
	SizeBytes          int64         `json:"size_bytes"`
	OSType             domain.OSType `json:"os_type"`
	Distribution       string        `json:"distribution"`
	Version            string        `json:"version"`
	Architecture       string        `json:"architecture"`
	Editions           []Edition     `json:"editions"`
	RecommendedSizeGiB int64         `json:"recommended_size_gib"`
}
type InspectRequest struct {
	Path string `json:"path"`
}
type MediaFiles struct {
	Dir     string
	Files   map[string]string
	Content map[string][]byte
	// SHA256 是复制 ISO 时算出的整张校验值。
	SHA256 string `json:",omitempty"`
}
type StartRequest struct {
	SourcePath        string `json:"source_path"`
	SourceFingerprint string `json:"source_fingerprint"`
	// SourceSHA256 只在重新开始安装时带上原会话的校验值，复制 ISO 时核对。
	SourceSHA256 string `json:"source_sha256"`
	EditionIndex int    `json:"edition_index"`
	Name         string `json:"name"`
	// PoolID 只为兼容旧页面而接收，安装总是用当前节点的数据池。
	PoolID   string `json:"pool_id"`
	SizeGiB  int64  `json:"size_gib"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Netmask  string `json:"netmask"`
	Gateway  string `json:"gateway"`
	DNS1     string `json:"dns1"`
	Username string `json:"username"`
	Password string `json:"password"`
}
type StartResult struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	ImageID   string `json:"image_id"`
}
type Report struct {
	Attempt  int    `json:"attempt"`
	Phase    string `json:"phase"`
	BootID   string `json:"boot_id"`
	Progress int    `json:"progress"`
	Message  string `json:"message"`
}
type View struct {
	ID           string        `json:"id"`
	TaskID       string        `json:"task_id"`
	ImageID      string        `json:"image_id"`
	Name         string        `json:"name"`
	MAC          string        `json:"mac"`
	IP           string        `json:"ip"`
	SourcePath   string        `json:"source_path"`
	SourceSHA256 string        `json:"source_sha256"`
	EditionIndex int           `json:"edition_index"`
	PoolID       string        `json:"pool_id"`
	SizeGiB      int64         `json:"size_gib"`
	Netmask      string        `json:"netmask"`
	Gateway      string        `json:"gateway"`
	DNS1         string        `json:"dns1"`
	Username     string        `json:"username"`
	OSType       domain.OSType `json:"os_type"`
	Distribution string        `json:"distribution"`
	Version      string        `json:"version"`
	Phase        string        `json:"phase"`
	Progress     int           `json:"progress"`
	Message      string        `json:"message"`
	Error        string        `json:"error"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}
type ListResult struct {
	Items []View `json:"items"`
	Total int    `json:"total"`
}
type Service struct {
	ImportDir     func(context.Context) (string, error)
	Store         store.Store
	Storage       storage.StorageAgent
	Host          storage.InstallationHostAgent
	Builder       boot.BootBuilder
	ReportBase    string
	CanWrite      func() error
	RedactSecrets func(domain.InstallSession, []byte) []byte
	NodeID        string
	Network       assets.NetworkProvider
	SyncDHCP      func(context.Context) error
	InspectSource func(context.Context, string) (Metadata, error)
	Prepare       func(context.Context, domain.InstallSession) (MediaFiles, error)
	Render        func(context.Context, domain.InstallSession, MediaFiles) (string, error)
	MountScript   func(domain.InstallSession) []byte
	// BakeMountScript 给出保存 Windows 镜像时烘焙的标准挂盘脚本及其烘焙版本，与导入相同。
	BakeMountScript func() ([]byte, string)
	FilesDir        func(context.Context) (string, error)
	CleanupRemote   func(context.Context, storage.InstallationCleanupReq) error
	Now             func() time.Time
	SaveGrace       time.Duration
	Async           bool
	mu              sync.Mutex
	mediaMu         sync.Mutex
}

func view(s domain.InstallSession) View {
	return View{
		ID: s.ID, TaskID: s.TaskID, ImageID: s.ImageID, Name: s.Name, MAC: s.MAC, IP: s.IP,
		SourcePath: s.SourcePath, SourceSHA256: s.SourceSHA256, EditionIndex: s.EditionIndex, PoolID: s.PoolID, SizeGiB: s.SizeBytes >> 30,
		Netmask: s.Netmask, Gateway: s.Gateway, DNS1: s.DNS1, Username: s.Username,
		OSType: s.OSType, Distribution: s.Distribution, Version: s.Version, Phase: s.Phase, Progress: s.Progress,
		Message: s.Message, Error: s.Error, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
