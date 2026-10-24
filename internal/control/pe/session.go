package pe

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/tianwei/diskless/internal/boot/tftpassets"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/tasks"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/storage/iscsi"
	"github.com/tianwei/diskless/internal/store"
)

//go:embed scripts/*
var scripts embed.FS

// DefaultTTL 是从创建会话到客户机进 WinPE 并回报结果的最长等待；超过即判失败，客户机下次开机恢复正常路径。
var completeMu sync.Mutex

var ErrFinalizing = errors.New("PE result accepted; waiting for shutdown")

const DefaultTTL = 20 * time.Minute

// 回报日志的白名单与上限；文件名由 WinPE 侧脚本给出，不接受别的。
var logNames = map[string]bool{"console.log": true, "dism.log": true, "setupapi.dev.log": true, "identity.json": true, "result.json": true}

const maxLogBytes = 16 << 20

// SessionService 管理 PE 会话：建会话、给 /boot 出 WinPE 引导脚本、按令牌供文件、收回报、超时收尾。
type SessionService struct {
	Store    store.Store
	FilesDir func(context.Context) (string, error)
	Now      func() time.Time
	Async    bool
	// ReportBase 是 WinPE 回报的服务器地址（含端口），如 http://192.168.10.250:8080。
	ReportBase string
	TTL        time.Duration
	// OnDone 在会话回报结果后调用，067/068 据此继续各自的流程；返回错误会记到任务上。
	OnDone     func(ctx context.Context, sess domain.PESession, result PEResult) error
	OnFailure  func(ctx context.Context, sess domain.PESession) error
	CHAPSecret string
	// EnsureEnv 非空时，镜像还没有 WinRE 环境就当场提取（EnvService.EnsureForImage）。
	EnsureEnv func(ctx context.Context, imageID string) (domain.WinPEEnv, error)
	Logger    *slog.Logger
}

// ProbeRequest 是「适配环境自检」的参数：都可空，默认用该客户机所在分组镜像的 WinRE。
type ProbeRequest struct {
	EnvID string `json:"env_id"`
}

// StartResult 是会话句柄。
type StartResult struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
}

// PEResult 是 WinPE 回报的结果。
type PEResult struct {
	OK      bool            `json:"ok"`
	Stage   string          `json:"stage"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// PEFile 是按令牌下发的一个文件：有路径的用文件服务，否则直接给内容。
type PEFile struct {
	Name    string
	Path    string
	Content []byte
}

// StartProbe 让一台关机的客户机下次开机进 WinPE，回报环境信息后关机。只验证引导链路，不碰任何盘。
func (s SessionService) StartProbe(ctx context.Context, terminalID string, req ProbeRequest) (StartResult, error) {
	terminal, err := s.Store.Terminals().Get(ctx, terminalID)
	if err != nil {
		return StartResult{}, err
	}
	var profile domain.HardwareProfile
	var filesDir string
	if terminal.HardwareProfileID != "" {
		profile, err = s.Store.HardwareProfiles().Get(ctx, terminal.HardwareProfileID)
		if err != nil {
			return StartResult{}, err
		}
		filesDir, err = s.FilesDir(ctx)
		if err != nil {
			return StartResult{}, err
		}
	}
	res, err := s.start(ctx, terminal, domain.PEModeProbe, domain.TaskTypePEProbe, req.EnvID, "", 0)
	if err != nil || terminal.HardwareProfileID == "" {
		return res, err
	}
	packs, err := s.StageUploadedDriver(ctx, "PCI\\VEN_"+profile.NICVendor+"&DEV_"+profile.NICDevice, filepath.Join(filesDir, "pe-sessions", res.SessionID, "drivers"))
	if err == nil {
		var sess domain.PESession
		sess, err = s.Store.PESessions().Get(ctx, res.SessionID)
		if err == nil {
			sess.DriverPackIDs = packs
			sess.State = domain.PEStatePending
			err = s.Store.PESessions().Update(ctx, sess)
		}
	}
	if err != nil {
		_ = s.Fail(ctx, res.SessionID, "准备客户机网卡检查驱动失败："+err.Error())
		return StartResult{}, err
	}
	return res, nil
}

// start 是各模式共用的建会话：前置检查、选环境、准备驱动文件、建任务与会话。
func (s SessionService) start(ctx context.Context, terminal domain.Terminal, mode domain.PEMode, taskType domain.TaskType, envID, targetIQN string, lun int) (StartResult, error) {
	who := terminalLabel(terminal)
	if _, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(terminal.MAC)); err == nil {
		return StartResult{}, errs.Conflict("客户机「" + who + "」正在从 ISO 创建镜像，请先完成或取消安装任务")
	} else if !errs.IsNotFound(err) {
		return StartResult{}, err
	}
	if terminal.State == domain.TerminalStateOnline {
		return StartResult{}, errs.Conflict(fmt.Sprintf("请先关闭客户机「%s」，关机后再开始", who))
	}
	if active, err := s.Store.PESessions().ActiveByTerminal(ctx, terminal.ID); err == nil && !s.expired(active) {
		return StartResult{}, errs.Conflict(fmt.Sprintf("客户机「%s」已有进行中的 WinPE 任务，请等它结束或超时", who))
	}
	env, err := s.chooseEnv(ctx, terminal, envID)
	if err != nil {
		return StartResult{}, err
	}
	bcd, err := s.bcdFor(ctx, env)
	if err != nil {
		return StartResult{}, err
	}
	_, err = s.FilesDir(ctx)
	if err != nil {
		return StartResult{}, err
	}
	now := s.now()
	token, err := newToken()
	if err != nil {
		return StartResult{}, err
	}
	sess := domain.PESession{
		ID:         fmt.Sprintf("pe-%s-%d", strings.ToLower(storage.NormalizeMAC(terminal.MAC)), time.Now().UnixNano()),
		TerminalID: terminal.ID, MAC: storage.NormalizeMAC(terminal.MAC), Mode: mode, Token: token,
		EnvID: env.ID, BCDID: bcd.ID, TargetIQN: targetIQN, LUN: lun,
		State: domain.PEStatePending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(s.ttl()),
	}
	if mode == domain.PEModeAdapt || mode == domain.PEModeIdentity || (mode == domain.PEModeProbe && terminal.HardwareProfileID != "") {
		sess.State = domain.PEStatePreparing
	}
	target := terminal.MAC
	if taskType == domain.TaskTypeModelAdapt {
		target = terminal.HardwareProfileID
	}
	task, err := s.runner().Create(ctx, taskType, target)
	if err != nil {
		return StartResult{}, err
	}
	sess.TaskID = task.ID
	if err := s.Store.PESessions().Create(ctx, sess); err != nil {
		_ = s.runner().Fail(ctx, task, err)
		return StartResult{}, err
	}
	task.Status = domain.TaskStatusRunning
	task.Message = "等待客户机开机进入 WinPE"
	_ = s.Store.Tasks().Update(ctx, task)
	return StartResult{TaskID: task.ID, SessionID: sess.ID}, nil
}

// Active 返回终端进行中的会话；已过期的顺手收尾并当作没有。
func (s SessionService) Active(ctx context.Context, terminalID string) (domain.PESession, bool, error) {
	completeMu.Lock()
	defer completeMu.Unlock()
	sess, err := s.Store.PESessions().ActiveByTerminal(ctx, terminalID)
	if errs.IsNotFound(err) {
		return domain.PESession{}, false, nil
	}
	if err != nil {
		return domain.PESession{}, false, err
	}
	if sess.State == domain.PEStatePreparing {
		return domain.PESession{}, false, errs.Conflict("正在准备客户机维护环境，请等任务显示引导客户机后再开机")
	}
	if sess.State == domain.PEStateFinalizing {
		return domain.PESession{}, false, errs.Conflict("客户机正在保存机型适配还原点，请等任务完成后再开机")
	}
	if s.expired(sess) {
		_ = s.fail(ctx, sess, s.timeoutMessage())
		return domain.PESession{}, false, nil
	}
	return sess, true, nil
}

// Script 生成 WinPE 的 iPXE 引导脚本。文件都走 /boot/pe/<token>/…，相对地址落回下发脚本的那台服务器。
func (s SessionService) Script(ctx context.Context, sess domain.PESession, terminal domain.Terminal) (string, error) {
	env, err := s.Store.WinPEEnvs().Get(ctx, sess.EnvID)
	if err != nil {
		return "", err
	}
	base := "/boot/pe/" + sess.Token + "/"
	lines := []string{
		"#!ipxe",
		"set keep-san 1",
		"set initiator-iqn " + iscsi.InitiatorIQN(terminal.MAC),
	}
	if sess.Mode == domain.PEModeAdapt && sess.TargetIQN != "" {
		if iscsi.CHAPEnabled(s.CHAPSecret) {
			c := iscsi.CHAPCredentials(s.CHAPSecret, terminal.MAC)
			lines = append(lines, "set username "+c.Username, "set password "+c.Password)
		}
		lines = append(lines, fmt.Sprintf("sanhook --drive 0x80 iscsi:%s:::%d:%s || goto nd-pe-failed", sess.TargetPortal, sess.LUN, sess.TargetIQN))
	}
	lines = append(lines,
		"kernel "+base+"wimboot || goto nd-pe-failed",
		"initrd "+base+"BCD?platform=${platform} BCD || goto nd-pe-failed",
		"initrd "+base+"boot.sdi boot.sdi || goto nd-pe-failed",
		"initrd "+base+"winpeshl.ini winpeshl.ini || goto nd-pe-failed",
		"initrd "+base+"ndpe.ini ndpe.ini || goto nd-pe-failed",
		"initrd "+base+"ndpe.cmd ndpe.cmd || goto nd-pe-failed",
		"initrd "+base+"ndpe-common.cmd ndpe-common.cmd || goto nd-pe-failed",
		"initrd "+base+"ndpe-report.js ndpe-report.js || goto nd-pe-failed",
		"initrd "+base+"ndadapt-pe.cmd ndadapt-pe.cmd || goto nd-pe-failed",
		"initrd "+base+"ndnic.js ndnic.js || goto nd-pe-failed",
		"initrd "+base+"ndidentity.cmd ndidentity.cmd || goto nd-pe-failed",
	)
	if _, ok := env.Files["curl.exe"]; ok {
		lines = append(lines, "initrd "+base+"curl.exe curl.exe || goto nd-pe-failed")
	}
	if filesDir, err := s.FilesDir(ctx); err == nil {
		files, err := driverFiles(filepath.Join(filesDir, "pe-sessions", sess.ID, "drivers"))
		if err != nil {
			return "", err
		}
		for i := range files {
			// UEFI uses the image name; BIOS uses the CPIO argument. The
			// indexed URL has neither name, so set both to the manifest alias.
			lines = append(lines, fmt.Sprintf("initrd --name nd-file-%04d %sdrivers/%04d nd-file-%04d || goto nd-pe-failed", i, base, i, i))
		}
		if len(files) > 0 {
			lines = append(lines, "initrd "+base+"nd-driver-files.txt nd-driver-files.txt || goto nd-pe-failed")
		}
	}
	lines = append(lines,
		"initrd "+base+"boot.wim boot.wim || goto nd-pe-failed",
		"boot || goto nd-pe-failed",
		":nd-pe-failed",
		"set nd-pe-errno ${errno}",
		"echo WinPE boot failed (error ${nd-pe-errno}), reported to the server",
		"imgfetch "+base+"failed?err=${nd-pe-errno}&platform=${platform} || echo Could not reach the server",
		"sleep 30",
		"exit",
	)
	return strings.Join(lines, "\n") + "\n", nil
}

// File 按令牌解析一个下发文件。令牌只在会话进行中有效。platform 是 iPXE 报的固件（pcbios/efi），只影响选哪份 BCD。
func (s SessionService) File(ctx context.Context, token, name, platform string) (PEFile, error) {
	sess, err := s.activeByToken(ctx, token)
	if err != nil {
		return PEFile{}, err
	}
	name = path.Clean("/" + name)[1:]
	switch name {
	case "wimboot":
		data, err := tftpassets.Read("wimboot")
		return PEFile{Name: name, Content: data}, err
	case "BCD":
		env, err := s.Store.WinPEEnvs().Get(ctx, sess.BCDID)
		if err != nil {
			return PEFile{}, err
		}
		file := bcdFileName
		if _, ok := env.Files[bcdPCATFileName]; ok && strings.EqualFold(platform, "pcbios") {
			file = bcdPCATFileName
		}
		return s.environmentFile(ctx, env, name, file)
	case "boot.wim", "boot.sdi", "curl.exe":
		env, err := s.Store.WinPEEnvs().Get(ctx, sess.EnvID)
		if err != nil {
			return PEFile{}, err
		}
		if _, ok := env.Files[name]; !ok {
			return PEFile{}, errs.NotFound("该适配环境没有 " + name)
		}
		return s.environmentFile(ctx, env, name, name)
	case "ndpe.ini":
		terminal, err := s.Store.Terminals().Get(ctx, sess.TerminalID)
		if err != nil {
			return PEFile{}, err
		}
		return PEFile{Name: name, Content: []byte(s.renderINI(sess, terminal))}, nil
	case "winpeshl.ini", "ndpe.cmd", "ndpe-common.cmd", "ndpe-report.js", "ndadapt-pe.cmd", "ndnic.js", "ndidentity.cmd":
		data, err := scripts.ReadFile("scripts/" + name)
		if err != nil {
			return PEFile{}, err
		}
		data = crlf(data)
		if strings.HasSuffix(name, ".js") {
			code := utf16.Encode([]rune(string(data)))
			data = []byte{0xff, 0xfe}
			for _, c := range code {
				data = append(data, byte(c), byte(c>>8))
			}
		}
		return PEFile{Name: name, Content: data}, nil
	}
	if name == "nd-driver-files.txt" || strings.HasPrefix(name, "drivers/") {
		filesDir, err := s.FilesDir(ctx)
		if err != nil {
			return PEFile{}, err
		}
		dir := filepath.Join(filesDir, "pe-sessions", sess.ID, "drivers")
		files, err := driverFiles(dir)
		if err != nil {
			return PEFile{}, err
		}
		if name == "nd-driver-files.txt" {
			var lines []string
			for i, rel := range files {
				lines = append(lines, fmt.Sprintf("nd-file-%04d|%s", i, strings.ReplaceAll(rel, "/", "\\")))
			}
			return PEFile{Name: name, Content: []byte(strings.Join(lines, "\r\n") + "\r\n")}, nil
		}
		var index int
		if _, err := fmt.Sscanf(name, "drivers/%04d", &index); err != nil || index < 0 || index >= len(files) {
			return PEFile{}, errs.NotFound("驱动文件不存在")
		}
		return PEFile{Name: fmt.Sprintf("nd-file-%04d", index), Path: filepath.Join(dir, filepath.FromSlash(files[index]))}, nil
	}
	return PEFile{}, errs.NotFound("没有这个文件")
}

func (s SessionService) environmentFile(ctx context.Context, env domain.WinPEEnv, name, file string) (PEFile, error) {
	filesDir, err := s.FilesDir(ctx)
	if err != nil {
		return PEFile{}, err
	}
	dir, err := localEnvDir(filesDir, env)
	return PEFile{Name: name, Path: filepath.Join(dir, file)}, err
}

// SessionByToken 返回进行中的会话及其客户机，供端点核对来源地址。
func (s SessionService) SessionByToken(ctx context.Context, token string) (domain.PESession, domain.Terminal, error) {
	sess, err := s.activeByToken(ctx, token)
	if err != nil {
		return domain.PESession{}, domain.Terminal{}, err
	}
	terminal, err := s.Store.Terminals().Get(ctx, sess.TerminalID)
	return sess, terminal, err
}

// MarkBooted 记下客户机已经进了 WinPE：从这里起失败就不再是「没进去」。
func (s SessionService) MarkBooted(ctx context.Context, token string) error {
	completeMu.Lock()
	defer completeMu.Unlock()
	sess, err := s.activeByTokenLocked(ctx, token)
	if err != nil {
		return err
	}
	if sess.State == domain.PEStateBooted {
		return nil
	}
	sess.State = domain.PEStateBooted
	sess.UpdatedAt = s.now()
	if err := s.Store.PESessions().Update(ctx, sess); err != nil {
		return err
	}
	if task, err := s.Store.Tasks().Get(ctx, sess.TaskID); err == nil {
		task.Message = "客户机已进入 WinPE，正在执行"
		if sess.Mode == domain.PEModeAdapt {
			task.Message = "生成配置：客户机已进入WinPE，正在生成网卡模板并自检"
		}
		task.Progress = 50
		_ = s.Store.Tasks().Update(ctx, task)
	}
	return nil
}

// Complete 收下 WinPE 的结果，结束会话与任务；成功时交给 OnDone 继续。
func (s SessionService) Complete(ctx context.Context, token string, result PEResult) error {
	completeMu.Lock()
	defer completeMu.Unlock()
	sess, err := s.activeByTokenLocked(ctx, token)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(result)
	sess.Result = string(raw)
	sess.UpdatedAt = s.now()
	if !result.OK {
		msg := strings.TrimSpace(result.Message)
		if msg == "" {
			msg = "WinPE 里的步骤失败，未给出原因"
		}
		hints := map[string]string{"network": "请检查客户机网络和启动网卡驱动后重试", "boot": "请检查客户机网络启动和镜像恢复环境，关闭Secure Boot后重试", "environment": "请先查看任务详情中的环境检查日志，按具体失败原因处理后重试", "disk": "请检查客户机到存储节点的网络连接", "driver": "请到机型页上传匹配的已签名网卡驱动包后重试", "adapter": "请先查看任务详情中的网卡识别结果和DISM日志后重试", "registry": "请在任务详情查看网卡配置自检与DISM日志后重试", "identity": "请上传匹配的网卡驱动包后重新取证"}
		return s.fail(ctx, sess, fmt.Sprintf("%s：客户机在 WinPE 的%s阶段失败：%s；日志见任务详情", hints[result.Stage], result.Stage, msg))
	}
	if s.OnDone != nil {
		if err := s.OnDone(ctx, sess, result); err != nil {
			if errors.Is(err, ErrFinalizing) {
				return nil
			}
			if failed := s.fail(ctx, sess, err.Error()); failed != nil {
				return failed
			}
			if sess.Mode == domain.PEModeIdentity {
				return errs.Conflict("客户机网卡身份未能保存，请在任务详情查看失败原因后重新取证：" + err.Error())
			}
			return nil
		}
	}
	sess.State = domain.PEStateDone
	if err := s.Store.PESessions().Update(ctx, sess); err != nil {
		return err
	}
	task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
	if err != nil {
		return err
	}
	return s.runner().Finish(ctx, s.Store, task, strings.TrimSpace(result.Message))
}

// SaveLog 存 WinPE 上传的日志，文件名走白名单，大小有上限。
func (s SessionService) SaveLog(ctx context.Context, token, name string, r io.Reader) error {
	sess, err := s.activeByToken(ctx, token)
	if err != nil {
		return err
	}
	if !logNames[name] {
		return errs.Invalid("不接受这个日志文件名")
	}
	filesDir, err := s.FilesDir(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Join(filesDir, "pe-sessions", sess.ID, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxLogBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxLogBytes {
		return errs.Invalid("日志超过 16MB")
	}
	_, err = writeFileAtomic(filepath.Join(dir, name), data)
	return err
}

// TaskLog 给已登录的操作者读取任务日志；不使用已结束会话的一次性令牌。
func (s SessionService) TaskLog(ctx context.Context, taskID, name string) (PEFile, error) {
	if !logNames[name] {
		return PEFile{}, errs.Invalid("不接受这个日志文件名")
	}
	sess, err := s.Store.PESessions().ByTask(ctx, taskID)
	if err != nil {
		return PEFile{}, err
	}
	dir, err := s.FilesDir(ctx)
	if err != nil {
		return PEFile{}, err
	}
	file := filepath.Join(dir, "pe-sessions", sess.ID, "logs", name)
	if _, err := os.Stat(file); err != nil {
		if os.IsNotExist(err) {
			return PEFile{}, errs.NotFound("该任务尚无「" + name + "」日志")
		}
		return PEFile{}, err
	}
	return PEFile{Name: name, Path: file}, nil
}

// ExpireStale 把超时的会话判失败。由主程序周期调用，否则任务会一直显示执行中。
func (s SessionService) ExpireStale(ctx context.Context) (int, error) {
	active, err := s.Store.PESessions().ListActive(ctx)
	if err != nil {
		return 0, err
	}
	completeMu.Lock()
	defer completeMu.Unlock()
	n := 0
	for _, sess := range active {
		// The result may have been accepted after this sweep read its active list.
		sess, err = s.Store.PESessions().Get(ctx, sess.ID)
		if err != nil {
			return n, err
		}
		if sess.State != domain.PEStatePending && sess.State != domain.PEStateBooted && sess.State != domain.PEStatePreparing {
			continue
		}
		if s.expired(sess) {
			if err := s.fail(ctx, sess, s.timeoutMessage()); err == nil {
				n++
			}
		}
	}
	return n, nil
}

// RunExpirer 按 interval 周期判超时，直到 ctx 结束。
func (s SessionService) RunExpirer(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.ExpireStale(ctx); err != nil && s.Logger != nil {
				s.Logger.Warn("pe session expiry sweep failed", "error", err)
			} else if n > 0 && s.Logger != nil {
				s.Logger.Info("expired stale pe sessions", "count", n)
			}
		}
	}
}

// InUseDescription 说明哪个进行中的会话在用这份环境，没有则为空；供删除环境前检查。
func (s SessionService) InUseDescription(ctx context.Context, envID string) string {
	active, err := s.Store.PESessions().ListActive(ctx)
	if err != nil {
		return ""
	}
	for _, sess := range active {
		if sess.EnvID != envID && sess.BCDID != envID {
			continue
		}
		who := sess.MAC
		if t, err := s.Store.Terminals().Get(ctx, sess.TerminalID); err == nil {
			who = terminalLabel(t)
		}
		return fmt.Sprintf("客户机「%s」的 WinPE 任务", who)
	}
	return ""
}

// InUse 适配 EnvService.InUse 的签名。
func (s SessionService) InUse(ctx context.Context, envID string) (string, error) {
	return s.InUseDescription(ctx, envID), nil
}

// bcdFor 选启动配置：环境自带的（镜像 Windows\Boot\DVD 下的通用 BCD，或 ADK 包里的）优先，没有才用上传的。
func (s SessionService) bcdFor(ctx context.Context, env domain.WinPEEnv) (domain.WinPEEnv, error) {
	if _, ok := env.Files[bcdFileName]; ok {
		return env, nil
	}
	list, err := s.Store.WinPEEnvs().ByKind(ctx, domain.WinPEEnvBCD)
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	if len(list) == 0 {
		return domain.WinPEEnv{}, errs.Invalid("这份 WinPE 没有自带启动配置（BCD）：请在镜像详情上传用 New-NdPeBCD.ps1 生成的 BCD")
	}
	return list[0], nil
}

// chooseEnv 选 WinPE 来源：指定的环境 → 该客户机分组镜像自带的 WinRE → 最新的 ADK 包。
func (s SessionService) chooseEnv(ctx context.Context, terminal domain.Terminal, envID string) (domain.WinPEEnv, error) {
	if envID != "" {
		env, err := s.Store.WinPEEnvs().Get(ctx, envID)
		if err != nil {
			return domain.WinPEEnv{}, err
		}
		if env.Kind == domain.WinPEEnvBCD {
			return domain.WinPEEnv{}, errs.Invalid("BCD 不是 WinPE 环境")
		}
		return env, nil
	}
	if g, err := s.Store.Groups().Get(ctx, terminal.GroupID); err == nil {
		return s.chooseEnvForImage(ctx, g.SystemImageID)
	}
	return s.chooseEnvForImage(ctx, "")
}
func (s SessionService) chooseEnvForImage(ctx context.Context, imageID string) (domain.WinPEEnv, error) {
	imageName := ""
	var extractErr error
	if imageID != "" {
		if env, err := s.Store.WinPEEnvs().ByImage(ctx, imageID); err == nil {
			return env, nil
		}
		if img, err := s.Store.Images().Get(ctx, imageID); err == nil {
			imageName = img.Name
		}
		if s.EnsureEnv != nil {
			env, err := s.EnsureEnv(ctx, imageID)
			if err == nil {
				return env, nil
			}
			extractErr = err
		}
	}

	adk, err := s.Store.WinPEEnvs().ByKind(ctx, domain.WinPEEnvADK)
	if err != nil {
		return domain.WinPEEnv{}, err
	}
	if len(adk) > 0 {
		return adk[0], nil
	}
	hint := "镜像没有恢复环境，请在镜像详情上传 ADK 生成的 WinPE 包"
	if imageName != "" {
		hint = fmt.Sprintf("镜像「%s」没有恢复环境，请在镜像详情上传 ADK 生成的 WinPE 包", imageName)
	}
	if extractErr != nil {
		hint += "（" + extractErr.Error() + "）"
	}
	return domain.WinPEEnv{}, errs.Invalid("没有可用的 WinPE：" + hint)
}

func (s SessionService) renderINI(sess domain.PESession, terminal domain.Terminal) string {
	lines := []string{
		"mode=" + string(sess.Mode),
		"token=" + sess.Token,
		"report=" + strings.TrimRight(s.ReportBase, "/") + "/boot/pe/" + sess.Token,
		"mac=" + formatMAC(terminal.MAC),
		"terminal=" + terminal.ID,
		"target=" + sess.TargetIQN,
		"portal=" + sess.TargetPortal,
		fmt.Sprintf("lun=%d", sess.LUN),
	}
	if env, err := s.Store.WinPEEnvs().Get(context.Background(), sess.EnvID); err == nil {
		maxBuild := env.Build
		// Windows enablement releases share their predecessor's servicing branch.
		switch env.Build {
		case 18362:
			maxBuild = 18363
		case 19041:
			maxBuild = 19045
		case 22621:
			maxBuild = 22631
		case 26100:
			maxBuild = 26200
		}
		lines = append(lines, fmt.Sprintf("pe_build=%d", env.Build), fmt.Sprintf("pe_max_build=%d", maxBuild))
	}
	if terminal.HardwareProfileID != "" {
		if p, err := s.Store.HardwareProfiles().Get(context.Background(), terminal.HardwareProfileID); err == nil {
			lines = append(lines, "hwid=PCI\\VEN_"+p.NICVendor+"&DEV_"+p.NICDevice, "busloc="+p.BusLoc)
		}
	}
	if iscsi.CHAPEnabled(s.CHAPSecret) {
		c := iscsi.CHAPCredentials(s.CHAPSecret, terminal.MAC)
		lines = append(lines, "username="+c.Username, "password="+c.Password)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func (s SessionService) activeByToken(ctx context.Context, token string) (domain.PESession, error) {
	completeMu.Lock()
	defer completeMu.Unlock()
	return s.activeByTokenLocked(ctx, token)
}

func (s SessionService) activeByTokenLocked(ctx context.Context, token string) (domain.PESession, error) {
	if token == "" {
		return domain.PESession{}, errs.NotFound("会话不存在")
	}
	sess, err := s.Store.PESessions().ByToken(ctx, token)
	if err != nil {
		return domain.PESession{}, errs.NotFound("会话不存在")
	}
	if sess.State != domain.PEStatePending && sess.State != domain.PEStateBooted {
		return domain.PESession{}, errs.NotFound("会话已结束")
	}
	if s.expired(sess) {
		_ = s.fail(ctx, sess, s.timeoutMessage())
		return domain.PESession{}, errs.NotFound("会话已超时")
	}
	return sess, nil
}

func (s SessionService) fail(ctx context.Context, sess domain.PESession, msg string) error {
	if s.OnFailure != nil {
		if err := s.OnFailure(ctx, sess); err != nil {
			msg += "; 清理适配盘失败，请联系管理员：" + err.Error()
		}
	}
	sess.State = domain.PEStateFailed
	sess.UpdatedAt = s.now()
	if err := s.Store.PESessions().Update(ctx, sess); err != nil {
		return err
	}
	if sess.TaskID == "" {
		return nil
	}
	task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
	if err != nil {
		return err
	}
	return s.runner().Fail(ctx, task, fmt.Errorf("%s", msg))
}

func (s SessionService) timeoutMessage() string {
	return fmt.Sprintf("等待客户机进入 WinPE 并回报已超时（%d 分钟）；请确认客户机已开机、能从网络启动且未开启 Secure Boot，再重试", int(s.ttl().Minutes()))
}

func (s SessionService) expired(sess domain.PESession) bool {
	return !sess.ExpiresAt.IsZero() && s.now().After(sess.ExpiresAt)
}

func (s SessionService) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

func (s SessionService) runner() tasks.Runner {
	return tasks.Runner{Store: s.Store, Now: s.Now, Async: s.Async}
}

func (s SessionService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func newToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func terminalLabel(t domain.Terminal) string {
	if name := strings.TrimSpace(t.Name); name != "" {
		return name
	}
	return formatMAC(t.MAC)
}

// formatMAC 把 AABBCCDDEEFF 写成 AA:BB:CC:DD:EE:FF。
func formatMAC(mac string) string {
	mac = strings.ToUpper(storage.NormalizeMAC(mac))
	if len(mac) != 12 {
		return mac
	}
	var parts []string
	for i := 0; i < 12; i += 2 {
		parts = append(parts, mac[i:i+2])
	}
	return strings.Join(parts, ":")
}

// reportHost 从回报地址里取主机，用于 sanhook。
func reportHost(base string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}

func listDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// crlf 把脚本换行统一成 Windows 的 CRLF；cmd 对孤立的 LF 偶有解析问题。
func crlf(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
}

func (s SessionService) StartIdentity(ctx context.Context, terminalID string) (StartResult, error) {
	terminal, err := s.Store.Terminals().Get(ctx, terminalID)
	if err != nil {
		return StartResult{}, err
	}
	return s.start(ctx, terminal, domain.PEModeIdentity, domain.TaskTypeIdentityCapture, "", "", 0)
}

func (s SessionService) Fail(ctx context.Context, sessionID, message string) error {
	completeMu.Lock()
	defer completeMu.Unlock()
	session, err := s.Store.PESessions().Get(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.fail(ctx, session, message)
}
