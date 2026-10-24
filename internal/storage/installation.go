package storage

import (
	"context"
	"github.com/tianwei/diskless/internal/domain"
)

// InstallationDiskReq identifies a persistent work disk owned by one install attempt.
type InstallationDiskReq struct {
	ID        string
	MAC       string
	SizeBytes int64
	// MediaPath 是本节点上的安装介质（ISO），非空时作为只读 LUN 1 一起导出；为空则不挂介质。
	MediaPath string
	// Booting 表示正是这台客户机在开机要盘：它上一轮的会话已作废，不必等连接超时。
	Booting bool
}

type InstallationSaveReq struct {
	ID          string
	MAC         string
	ImageID     string
	Name        string
	OSType      domain.OSType
	SizeBytes   int64
	MountScript []byte
}

// InstallationCleanupReq names one attempt; media paths are resolved by the receiving node.
type InstallationCleanupReq struct {
	NodeID   string
	ID       string
	MAC      string
	ImageID  string
	Password string
	Token    string
}

// InstallationHostAgent keeps installation operations on the storage node boundary.
type InstallationHostAgent interface {
	PrepareInstallation(context.Context, InstallationDiskReq) (LUNInfo, error)
	SaveInstallation(context.Context, InstallationSaveReq) (ImportImageResult, error)
	CleanupInstallation(context.Context, InstallationDiskReq) error
	CleanupInstallationCandidate(context.Context, InstallationCleanupReq) error
}
