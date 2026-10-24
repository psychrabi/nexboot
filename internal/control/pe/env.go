// Package pe 管理客户机进 WinPE 做适配所需的东西：适配环境文件（WinRE、ADK 包、BCD）与 PE 会话。
package pe

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/tasks"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
	"github.com/tianwei/diskless/internal/winpe"
)

// WIMInfo 是 WinPE 映像的版本信息（见 winpe.ReadWIMInfo）。
type WIMInfo = winpe.WIMInfo

// Storage 是适配环境需要的存储能力：文件库目录与从镜像提取 WinRE。
type Storage interface {
	EnsureFilesDir(context.Context) (string, error)
	ExtractWinRE(context.Context, storage.ExtractWinREReq) (storage.WinREExtract, error)
}

// EnvService 维护适配环境：每个 Windows 镜像一份提取的 WinRE、全局一份 BCD、若干上传的 ADK 包。
type EnvService struct {
	Store   store.Store
	Storage Storage
	Now     func() time.Time
	Async   bool
	// ReadWIM 可替换以便测试；默认读 WIM 头部。
	ReadWIM func(path string) (WIMInfo, error)
	// InUse 返回正在使用该环境的任务描述（如「客户机 前台02 的适配任务」），非空即拒绝删除；为 nil 不检查。
	InUse func(ctx context.Context, envID string) (string, error)
}

const (
	bcdEnvID    = "winpe-bcd"
	bcdFileName = "BCD"
	// bcdPCATFileName 是 BIOS 固件用的版本；两份都来自镜像的 Windows\Boot\DVD。
	bcdPCATFileName = "BCD.pcat"
	wimFileName     = "boot.wim"
	sdiFileName     = "boot.sdi"
	manifestName    = "manifest.json"
)

// ExtractResult 是提取任务的句柄。
type ExtractResult struct {
	TaskID string `json:"task_id"`
}

// ExtractForImage 为镜像发起 winpe_extract 任务：从默认配置的当前还原点拷出 WinRE 到文件库。
func (s EnvService) ExtractForImage(ctx context.Context, imageID string) (ExtractResult, error) {
	img, err := s.Store.Images().Get(ctx, imageID)
	if err != nil {
		return ExtractResult{}, err
	}
	if img.Purpose == domain.ImagePurposeData {
		return ExtractResult{}, errs.Invalid(fmt.Sprintf("「%s」是数据盘，里面没有恢复环境", img.Name))
	}
	if img.OSType != domain.OSTypeWindows {
		return ExtractResult{}, errs.Invalid(fmt.Sprintf("「%s」不是 Windows 镜像，没有恢复环境可提取", img.Name))
	}
	configID, snapshot, err := s.extractSource(ctx, img.ID)
	if err != nil {
		return ExtractResult{}, err
	}
	task, err := s.runner().Run(ctx, domain.TaskTypeWinPEExtract, img.ID, func(ctx context.Context, task domain.Task) error {
		return s.executeExtract(ctx, task, img, configID, snapshot)
	})
	return ExtractResult{TaskID: task.ID}, err
}

// EnsureForImage 返回镜像的 WinRE 环境，没有就当场提取（同步，任务照常可见）。失败即镜像里没有恢复环境。
func (s EnvService) EnsureForImage(ctx context.Context, imageID string) (domain.WinPEEnv, error) {
	if env, err := s.Store.WinPEEnvs().ByImage(ctx, imageID); err == nil {
		return env, nil
	} else if !errs.IsNotFound(err) {
		return domain.WinPEEnv{}, err
	}
	sync := s
	sync.Async = false
	if _, err := sync.ExtractForImage(ctx, imageID); err != nil {
		return domain.WinPEEnv{}, err
	}
	return s.Store.WinPEEnvs().ByImage(ctx, imageID)
}

func (s EnvService) executeExtract(ctx context.Context, task domain.Task, img domain.Image, configID, snapshot string) error {
	filesDir, err := s.Storage.EnsureFilesDir(ctx)
	if err != nil {
		return err
	}
	dest := filepath.Join(filesDir, "winpe", img.ID)
	res, err := s.Storage.ExtractWinRE(ctx, storage.ExtractWinREReq{ImageID: img.ID, ConfigID: configID, SnapshotName: snapshot, DestDir: dest})
	if err != nil {
		return err
	}
	info, err := s.readWIM(res.WIMPath)
	if err != nil {
		return err
	}
	env := domain.WinPEEnv{
		ID: "winpe-" + img.ID, Kind: domain.WinPEEnvImageWinRE, ImageID: img.ID,
		Version: info.Version, Build: info.Build, Dir: dest,
		Files:     map[string]string{wimFileName: res.WIMSHA256, sdiFileName: res.SDISHA256},
		CreatedAt: s.now(),
	}
	if res.CurlPath != "" {
		env.Files["curl.exe"] = res.CurlSHA256
	}
	if res.BCDEFIPath != "" {
		env.Files[bcdFileName] = res.BCDEFISHA256
	}
	if res.BCDPCATPath != "" {
		env.Files[bcdPCATFileName] = res.BCDPCATSHA256
	}
	return s.Store.Tx(ctx, func(tx store.Store) error {
		if err := upsertEnv(ctx, tx, env); err != nil {
			return err
		}
		return s.runner().Finish(ctx, tx, task, info.Version)
	})
}

// extractSource 选镜像默认配置的当前还原点；没有当前点时取最新就绪的。
func (s EnvService) extractSource(ctx context.Context, imageID string) (string, string, error) {
	configs, err := s.Store.Configs().ListByImage(ctx, imageID)
	if err != nil {
		return "", "", err
	}
	if len(configs) == 0 {
		return "", "", errs.Invalid("镜像还没有配置，无法提取恢复环境")
	}
	cfg := configs[0]
	for _, c := range configs {
		if c.Name == "default" {
			cfg = c
			break
		}
	}
	if cfg.DefaultReductionID != nil && *cfg.DefaultReductionID != "" {
		if red, err := s.Store.Reductions().Get(ctx, *cfg.DefaultReductionID); err == nil && red.Status == domain.ReductionStatusReady {
			return cfg.ID, strings.TrimPrefix(red.Name, "@"), nil
		}
	}
	reductions, err := s.Store.Reductions().ListByConfig(ctx, cfg.ID)
	if err != nil {
		return "", "", err
	}
	var latest *domain.Reduction
	for i := range reductions {
		r := &reductions[i]
		if r.Status != domain.ReductionStatusReady {
			continue
		}
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) {
			latest = r
		}
	}
	if latest == nil {
		return "", "", errs.Invalid("镜像还没有就绪的还原点，无法提取恢复环境")
	}
	return cfg.ID, strings.TrimPrefix(latest.Name, "@"), nil
}

// UploadBCD 保存全局唯一的启动配置数据，重复上传即替换。
func (s EnvService) UploadBCD(ctx context.Context, data []byte) (domain.WinPEEnv, error) {
	if err := winpe.ValidateBCD(data); err != nil {
		return domain.WinPEEnv{}, errs.Invalid(err.Error())
	}
	filesDir, err := s.Storage.EnsureFilesDir(ctx)
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	dir := filepath.Join(filesDir, "winpe", "bcd")
	sum, err := writeFileAtomic(filepath.Join(dir, bcdFileName), data)
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	env := domain.WinPEEnv{ID: bcdEnvID, Kind: domain.WinPEEnvBCD, Dir: dir, Files: map[string]string{bcdFileName: sum}, CreatedAt: s.now()}
	if err := upsertEnv(ctx, s.Store, env); err != nil {
		return domain.WinPEEnv{}, err
	}
	return env, nil
}

type adkManifest struct {
	Kind    string            `json:"kind"`
	Version string            `json:"version"`
	Build   int               `json:"build"`
	Files   map[string]string `json:"files"`
}

// UploadADK 接收 Build-NdWinPE.ps1 产出的 zip：按 manifest 校验每个文件的哈希，读 boot.wim 版本，落到文件库。
// 上传流先落临时文件再解包，包有几百 MB，不进内存。
func (s EnvService) UploadADK(ctx context.Context, r io.Reader) (domain.WinPEEnv, error) {
	filesDir, err := s.Storage.EnsureFilesDir(ctx)
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	base := filepath.Join(filesDir, "winpe")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return domain.WinPEEnv{}, err
	}
	tmp, err := os.CreateTemp(base, ".upload-*.zip")
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, r)
	tmp.Close()
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return domain.WinPEEnv{}, errs.Invalid("上传的不是 zip 包")
	}
	defer zr.Close()

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[filepath.Base(f.Name)] = f // 只认文件名，目录层级与 zip 路径穿越都不接受
	}
	mf, ok := entries[manifestName]
	if !ok {
		return domain.WinPEEnv{}, errs.Invalid("包里没有 manifest.json，不是 Build-NdWinPE.ps1 生成的适配环境包")
	}
	var manifest adkManifest
	if err := readZipJSON(mf, &manifest); err != nil {
		return domain.WinPEEnv{}, errs.Invalid("manifest.json 无法解析：" + err.Error())
	}
	if manifest.Kind != "ndwinpe" {
		return domain.WinPEEnv{}, errs.Invalid("manifest.json 不是适配环境包的清单")
	}
	for _, name := range []string{wimFileName, sdiFileName, bcdFileName} {
		if _, ok := entries[name]; !ok {
			return domain.WinPEEnv{}, errs.Invalid(fmt.Sprintf("包里缺少 %s", name))
		}
		if _, ok := manifest.Files[name]; !ok {
			return domain.WinPEEnv{}, errs.Invalid(fmt.Sprintf("manifest.json 里没有 %s 的哈希", name))
		}
	}

	dest := filepath.Join(base, fmt.Sprintf("adk-%d-%d", manifest.Build, time.Now().UTC().UnixNano()))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return domain.WinPEEnv{}, err
	}
	cleanup := func() { _ = os.RemoveAll(dest) }
	files := map[string]string{}
	for _, name := range []string{wimFileName, sdiFileName, bcdFileName} {
		sum, err := extractZipFile(entries[name], filepath.Join(dest, name))
		if err != nil {
			cleanup()
			return domain.WinPEEnv{}, err
		}
		if !strings.EqualFold(sum, manifest.Files[name]) {
			cleanup()
			return domain.WinPEEnv{}, errs.Invalid(fmt.Sprintf("%s 的哈希与 manifest.json 不符，包可能损坏或被改动", name))
		}
		files[name] = sum
	}
	bcd, err := os.ReadFile(filepath.Join(dest, bcdFileName))
	if err != nil {
		cleanup()
		return domain.WinPEEnv{}, err
	}
	if err := winpe.ValidateBCD(bcd); err != nil {
		cleanup()
		return domain.WinPEEnv{}, errs.Invalid(err.Error())
	}
	info, err := s.readWIM(filepath.Join(dest, wimFileName))
	if err != nil {
		cleanup()
		return domain.WinPEEnv{}, errs.Invalid("boot.wim 无法读取：" + err.Error())
	}
	if manifest.Build != 0 && info.Build != manifest.Build {
		cleanup()
		return domain.WinPEEnv{}, errs.Invalid(fmt.Sprintf("manifest.json 写的版本 %d 与 boot.wim 实际版本 %d 不符", manifest.Build, info.Build))
	}
	env := domain.WinPEEnv{
		ID: "winpe-" + filepath.Base(dest), Kind: domain.WinPEEnvADK,
		Version: info.Version, Build: info.Build, Dir: dest, Files: files, CreatedAt: s.now(),
	}
	if err := s.Store.WinPEEnvs().Create(ctx, env); err != nil {
		cleanup()
		return domain.WinPEEnv{}, err
	}
	return env, nil
}

// ImageWinRE 是一个 Windows 系统镜像的 WinRE 提取状态。
type ImageWinRE struct {
	ImageID string           `json:"image_id"`
	Name    string           `json:"name"`
	WinRE   *domain.WinPEEnv `json:"winre"`
}

// EnvView 是「适配环境」卡片的数据。
type EnvView struct {
	BCD    *domain.WinPEEnv  `json:"bcd"`
	ADK    []domain.WinPEEnv `json:"adk"`
	Images []ImageWinRE      `json:"images"`
}

// List 汇总 BCD、ADK 包和每个 Windows 系统镜像的 WinRE 状态。
func (s EnvService) List(ctx context.Context) (EnvView, error) {
	envs, err := s.Store.WinPEEnvs().List(ctx)
	if err != nil {
		return EnvView{}, err
	}
	view := EnvView{ADK: []domain.WinPEEnv{}, Images: []ImageWinRE{}}
	byImage := map[string]domain.WinPEEnv{}
	for i := range envs {
		e := envs[i]
		switch e.Kind {
		case domain.WinPEEnvBCD:
			view.BCD = &e
		case domain.WinPEEnvADK:
			view.ADK = append(view.ADK, e)
		case domain.WinPEEnvImageWinRE:
			byImage[e.ImageID] = e
		}
	}
	sort.Slice(view.ADK, func(i, j int) bool { return view.ADK[i].CreatedAt.After(view.ADK[j].CreatedAt) })
	images, err := s.Store.Images().List(ctx)
	if err != nil {
		return EnvView{}, err
	}
	for _, img := range images {
		if img.OSType != domain.OSTypeWindows || img.Purpose == domain.ImagePurposeData {
			continue
		}
		item := ImageWinRE{ImageID: img.ID, Name: img.Name}
		if e, ok := byImage[img.ID]; ok {
			item.WinRE = &e
		}
		view.Images = append(view.Images, item)
	}
	sort.Slice(view.Images, func(i, j int) bool { return view.Images[i].Name < view.Images[j].Name })
	return view, nil
}

// Delete 删除一份环境及其文件；正被任务使用时点名拒绝。
func (s EnvService) Delete(ctx context.Context, id string) error {
	env, err := s.Store.WinPEEnvs().Get(ctx, id)
	if err != nil {
		return err
	}
	if s.InUse != nil {
		who, err := s.InUse(ctx, id)
		if err != nil {
			return err
		}
		if who != "" {
			return errs.Conflict(fmt.Sprintf("这份适配环境正被%s使用，请等它结束后再删除", who))
		}
	}
	filesDir, err := s.Storage.EnsureFilesDir(ctx)
	if err != nil {
		return err
	}
	dir, err := localEnvDir(filesDir, env)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return s.Store.WinPEEnvs().Delete(ctx, id)
}

// 文件库随目录复制；各节点的池名可以不同，记录里的绝对路径不能跨节点使用。
func localEnvDir(filesDir string, env domain.WinPEEnv) (string, error) {
	dir := filepath.Clean(env.Dir)
	if filepath.Base(filepath.Dir(dir)) != "winpe" {
		return "", fmt.Errorf("适配环境目录 %s 不在文件库 winpe 目录里", env.Dir)
	}
	return filepath.Join(filesDir, "winpe", filepath.Base(dir)), nil
}

func upsertEnv(ctx context.Context, st store.Store, env domain.WinPEEnv) error {
	if _, err := st.WinPEEnvs().Get(ctx, env.ID); err == nil {
		return st.WinPEEnvs().Update(ctx, env)
	} else if !errs.IsNotFound(err) {
		return err
	}
	return st.WinPEEnvs().Create(ctx, env)
}

func (s EnvService) readWIM(path string) (WIMInfo, error) {
	if s.ReadWIM != nil {
		return s.ReadWIM(path)
	}
	return winpe.ReadWIMInfo(path)
}

func (s EnvService) runner() tasks.Runner {
	return tasks.Runner{Store: s.Store, Now: s.Now, Async: s.Async}
}

func (s EnvService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// writeFileAtomic 经临时名写入再改名，返回内容的 sha256。
func writeFileAtomic(dst string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func readZipJSON(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(v)
}

// extractZipFile 把一个条目写到 dst，返回其 sha256。
func extractZipFile(f *zip.File, dst string) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), rc); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
