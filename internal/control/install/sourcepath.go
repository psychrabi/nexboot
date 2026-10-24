package install

import (
	"context"
	"path/filepath"
	"strings"
)

// 安装会话记下的 ISO 路径相对于文件库（"imports/win10.iso"）：文件库在存储池上并随目录库复制，
// 各节点的池名可以不同（/ndiskless/data 与 /ndiskless/tank），存相对路径换了主机也能找到同一个文件。
// 旧会话存的是绝对路径，原样使用。

// storedSourcePath 把文件库内的绝对路径换成相对路径；不在文件库内（旧导入目录）或读不到文件库时保留绝对路径。
func storedSourcePath(ctx context.Context, filesDir func(context.Context) (string, error), abs string) string {
	if filesDir == nil {
		return abs
	}
	root, err := filesDir(ctx)
	if err != nil || root == "" {
		return abs
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return abs
	}
	return rel
}

// resolveSourcePath 把会话里的路径换成本机绝对路径。
func resolveSourcePath(ctx context.Context, filesDir func(context.Context) (string, error), stored string) (string, error) {
	if stored == "" || filepath.IsAbs(stored) || filesDir == nil {
		return stored, nil
	}
	root, err := filesDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, stored), nil
}
