package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tianwei/diskless/internal/boot"
	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/hwprofile"
	"github.com/tianwei/diskless/internal/control/tasks"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

func (s *Service) Inspect(ctx context.Context, r InspectRequest) (Metadata, error) {
	if s.InspectSource == nil {
		return Metadata{}, errs.Invalid("安装文件识别服务尚未配置")
	}
	return s.InspectSource(ctx, r.Path)
}
func (s *Service) List(ctx context.Context) (ListResult, error) {
	all, err := s.Store.InstallSessions().List(ctx)
	out := ListResult{Items: []View{}}
	for _, v := range all {
		out.Items = append(out.Items, s.resolvedView(ctx, v))
	}
	out.Total = len(out.Items)
	return out, err
}
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	v, err := s.Store.InstallSessions().Get(ctx, id)
	return s.resolvedView(ctx, v), err
}

// resolvedView 给界面本机绝对路径，与 ISO 列表里的路径一致（重新开始安装靠它对上文件）。
func (s *Service) resolvedView(ctx context.Context, v domain.InstallSession) View {
	out := view(v)
	if p, err := resolveSourcePath(ctx, s.FilesDir, v.SourcePath); err == nil {
		out.SourcePath = p
	}
	return out
}

func (s *Service) Start(ctx context.Context, r StartRequest) (StartResult, error) {
	if s.CanWrite != nil {
		if err := s.CanWrite(); err != nil {
			return StartResult{}, err
		}
	}
	s.mu.Lock()
	res, err := s.start(ctx, r)
	s.mu.Unlock()
	if err != nil {
		return res, err
	}
	if s.Async {
		go func() { _ = s.prepare(context.Background(), res.SessionID) }()
		return res, nil
	}
	return res, s.prepare(ctx, res.SessionID)
}
func (s *Service) start(ctx context.Context, r StartRequest) (StartResult, error) {
	if s.Host == nil || s.Storage == nil || s.Prepare == nil || s.Render == nil {
		return StartResult{}, errs.Invalid("当前节点尚未配置 ISO 安装能力")
	}
	r.MAC = storage.NormalizeMAC(r.MAC)
	r.Name = strings.TrimSpace(r.Name)
	if !regexp.MustCompile(`^[0-9A-F]{12}$`).MatchString(r.MAC) {
		return StartResult{}, errs.Invalid("请填写正确的客户机 MAC 地址")
	}
	if r.Name == "" || len(r.Name) > 120 {
		return StartResult{}, errs.Invalid("请填写 1 至 120 字节的镜像名称")
	}
	if _, err := s.Store.Images().GetByName(ctx, r.Name); err == nil {
		return StartResult{}, errs.Conflict("已有同名镜像「" + r.Name + "」，请换一个名称")
	} else if !errs.IsNotFound(err) {
		return StartResult{}, err
	}
	existing, err := s.Store.InstallSessions().List(ctx)
	if err != nil {
		return StartResult{}, err
	}
	for _, v := range existing {
		if v.Active() && (v.MAC == r.MAC || v.Name == r.Name) {
			state := "进行中"
			retained := ""
			if v.Phase == "failed" {
				state = "失败"
				retained = "本次安装资源仍保留。"
			}
			return StartResult{}, errs.Conflict(fmt.Sprintf("请先处理已有安装：镜像「%s」、客户机「%s」的 ISO 安装%s，在「镜像」页它的安装卡片上重试或放弃。%s", v.Name, v.MAC, state, retained))
		}
	}
	if err := s.validateNetwork(ctx, r); err != nil {
		return StartResult{}, err
	}
	pool, err := s.localDataPool(ctx)
	if err != nil {
		return StartResult{}, err
	}
	if r.SizeGiB < 32 || r.SizeGiB > 2048 {
		return StartResult{}, errs.Invalid("系统安装盘容量须为 32 至 2048 GiB")
	}
	size := r.SizeGiB << 30
	if err := checkInitialAccount(r.Username, r.Password); err != nil {
		return StartResult{}, err
	}
	if t, err := s.Store.Terminals().GetByMAC(ctx, r.MAC); err == nil {
		if t.State == domain.TerminalStateOnline || t.IsSuper {
			return StartResult{}, errs.Conflict("请先关闭客户机并退出超管，再开始安装：" + r.MAC)
		}
		if _, err := s.Store.PESessions().ActiveByTerminal(ctx, t.ID); err == nil {
			return StartResult{}, errs.Conflict("请先完成客户机正在进行的维护任务：" + r.MAC)
		} else if !errs.IsNotFound(err) {
			return StartResult{}, err
		}
	} else if !errs.IsNotFound(err) {
		return StartResult{}, err
	}
	active, err := s.Storage.ActiveClientMACs(ctx)
	if err != nil {
		return StartResult{}, err
	}
	for _, mac := range active {
		if storage.NormalizeMAC(mac) == r.MAC {
			return StartResult{}, errs.Conflict("请先关闭仍在使用磁盘的客户机：" + r.MAC)
		}
	}
	meta, err := s.Inspect(ctx, InspectRequest{Path: r.SourcePath})
	if err != nil {
		return StartResult{}, err
	}
	// 重新开始安装只带原会话的校验值，复制 ISO 时核对（pinISO）。
	if r.SourceSHA256 == "" && r.SourceFingerprint == "" {
		return StartResult{}, errs.Conflict("请先选择并识别 ISO")
	}
	if r.SourceFingerprint != "" && r.SourceFingerprint != meta.Fingerprint {
		return StartResult{}, errs.Conflict("安装文件已变化，请重新选择并识别 ISO")
	}
	capacity, used := s.poolUsage(ctx, pool)
	if need := expectedInstallWrites(meta.SizeBytes, size); capacity > 0 && capacity-used < need {
		return StartResult{}, errs.Invalid(fmt.Sprintf("存储池「%s」剩余 %.1f GiB，这次安装预计需要约 %.1f GiB（安装资源加系统盘实际写入）：请清理存储池空间后再试",
			pool.Name, float64(capacity-used)/(1<<30), float64(need)/(1<<30)))
	}
	if meta.Architecture != "amd64" {
		return StartResult{}, errs.Invalid("首期仅支持 x64 安装文件")
	}
	language := ""
	if meta.OSType == domain.OSTypeWindows {
		edition := false
		for _, e := range meta.Editions {
			if e.Index == r.EditionIndex {
				edition, language = true, e.Language
			}
		}
		if !edition {
			return StartResult{}, errs.Invalid("请选择安装文件中实际存在的 Windows 版本")
		}
		if r.SizeGiB < 64 {
			return StartResult{}, errs.Invalid("Windows 安装盘至少需要 64 GiB")
		}
	}
	token, err := randomID()
	if err != nil {
		return StartResult{}, err
	}
	id := "install-" + token[:16]
	now := s.now()
	sess := domain.InstallSession{ID: id, MAC: r.MAC, IP: r.IP, Netmask: r.Netmask, Gateway: r.Gateway, DNS1: r.DNS1, Username: r.Username, Password: r.Password, Token: token, Attempt: 1, OSType: meta.OSType, EditionIndex: r.EditionIndex, Language: language, SizeBytes: size, ImageID: "iso_" + token[:16], Name: r.Name, PoolID: pool.ID, ServerID: pool.ServerID, SourcePath: storedSourcePath(ctx, s.FilesDir, meta.Path), SourceSHA256: r.SourceSHA256, Distribution: meta.Distribution, Version: meta.Version, Architecture: meta.Architecture, Phase: "preparing", Message: "正在准备安装资源，请稍后启动客户机", CreatedAt: now, UpdatedAt: now}
	var task domain.Task
	if err := s.Store.Tx(ctx, func(tx store.Store) error {
		var err error
		task, err = (tasks.Runner{Store: tx, Now: s.Now}).Create(ctx, domain.TaskTypeInstallImage, sess.ImageID)
		if err != nil {
			return err
		}
		task.Message = sess.Message
		if err := tx.Tasks().Update(ctx, task); err != nil {
			return err
		}
		sess.TaskID = task.ID
		return tx.InstallSessions().Create(ctx, sess)
	}); err != nil {
		return StartResult{}, err
	}
	result := StartResult{SessionID: id, TaskID: task.ID, ImageID: sess.ImageID}
	return result, nil
}

// expectedInstallWrites 估算一次安装在池里实际写入的量：安装盘是稀疏卷，按满额判断会拦掉放得下的安装。
// 安装资源（ISO 副本与安装文件）约 2 倍 ISO，系统盘写入（下载的安装文件加展开的系统）约 4 倍 ISO。
func expectedInstallWrites(isoBytes, diskBytes int64) int64 {
	if isoBytes <= 0 {
		return diskBytes
	}
	return min(diskBytes, 6*isoBytes+4<<30)
}

// poolUsage 优先读存储池实时用量：刚清理完上一次安装时，库里的记录要等心跳才刷新。
func (s *Service) poolUsage(ctx context.Context, pool domain.Pool) (int64, int64) {
	if live, ok := s.Storage.(interface {
		PoolStatus(context.Context, string) (domain.PoolStatus, error)
	}); ok {
		if st, err := live.PoolStatus(ctx, pool.Name); err == nil && st.Capacity > 0 {
			return st.Capacity, st.Used
		}
	}
	return pool.Capacity, pool.Used
}

// localDataPool 是安装落盘的池：安装文件与安装盘都在应答的这台上，每台恰好一个数据池，不让运维选。
// 请求里的 pool_id 不再参考，主机切换后沿用旧设置重试也能装到新主机上。
func (s *Service) localDataPool(ctx context.Context) (domain.Pool, error) {
	named, ok := s.Storage.(interface{ DataPoolName() string })
	name := ""
	if ok {
		name = named.DataPoolName()
	}
	if name == "" {
		return domain.Pool{}, errs.Invalid("请先在「存储池管理」创建数据池，再开始安装")
	}
	pools, err := s.Store.Pools().List(ctx)
	if err != nil {
		return domain.Pool{}, err
	}
	for _, p := range pools {
		if p.Name == name && (s.NodeID == "" || p.ServerID == s.NodeID) {
			return p, nil
		}
	}
	return domain.Pool{}, errs.Invalid("当前节点还没有数据池「" + name + "」：请先在「存储池管理」创建，再开始安装")
}

func (s *Service) prepare(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ensureOwner(sess); err != nil {
		return err
	}
	if sess.Phase != "preparing" {
		return nil
	}
	files, err := s.media(ctx, sess)
	// Preparation may have pinned private files before a later extraction or download fails.
	if files.Dir != "" {
		sess.MediaDir = files.Dir
	}
	// 复制 ISO 时算出的校验值；后面的步骤失败也留着，重新开始安装时据此核对文件没被换掉。
	if sess.SourceSHA256 == "" {
		sess.SourceSHA256 = files.SHA256
	}
	if err != nil {
		return s.abort(ctx, sess, "准备安装环境失败："+err.Error())
	}
	disk := storage.InstallationDiskReq{ID: sess.ID, MAC: sess.MAC, SizeBytes: sess.SizeBytes}
	if sess.OSType == domain.OSTypeLinux {
		// Ubuntu 安装程序从只读挂上的 ISO 按需读取，不再整张下进客户机内存。
		if disk.MediaPath = files.Files["media.iso"]; disk.MediaPath == "" {
			return s.abort(ctx, sess, "准备安装盘失败：安装资源里没有 ISO，请放弃后重新开始安装")
		}
	}
	info, err := s.Host.PrepareInstallation(ctx, disk)
	if err != nil {
		return s.abort(ctx, sess, "准备安装盘失败："+err.Error())
	}
	sess.Target, sess.Portal, sess.LUN = info.Target, s.portal(info.Server), info.System.LUN
	if sess.Target == "" {
		sess.Target = info.System.Target
	}
	if sess.Target == "" || net.ParseIP(sess.Portal) == nil {
		return s.abort(ctx, sess, "安装盘地址无效，请检查存储节点客户机网络")
	}
	sess.Phase = "waiting"
	sess.Message = "安装资源已准备好，请通过网络启动客户机「" + sess.MAC + "」"
	if err := s.update(ctx, sess); err != nil {
		return err
	}
	if s.SyncDHCP != nil {
		if err := s.SyncDHCP(ctx); err != nil {
			return s.abort(ctx, sess, "更新安装客户机 DHCP 配置失败："+err.Error())
		}
	}
	return nil
}

func (s *Service) validateNetwork(ctx context.Context, r StartRequest) error {
	ip, err := netip.ParseAddr(r.IP)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
		return errs.Invalid("请填写客户机安装时使用的有效 IPv4 地址")
	}
	mask := net.ParseIP(r.Netmask).To4()
	if mask == nil {
		return errs.Invalid("请填写有效子网掩码")
	}
	ones, bits := net.IPMask(mask).Size()
	if bits != 32 || ones < 1 || ones > 30 {
		return errs.Invalid("请填写连续的 IPv4 子网掩码")
	}
	prefix := netip.PrefixFrom(ip, ones).Masked()
	v4 := ip.As4()
	numeric := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	hostMask := uint32((uint64(1) << uint(32-ones)) - 1)
	if ip == prefix.Addr() || numeric&hostMask == hostMask {
		return errs.Invalid("客户机地址不能是网络地址或广播地址")
	}
	for _, a := range []string{r.Gateway, r.DNS1} {
		if a != "" {
			v, e := netip.ParseAddr(a)
			if e != nil || !v.Is4() {
				return errs.Invalid("网关和 DNS 必须是有效 IPv4 地址")
			}
		}
	}
	if r.Gateway != "" && !prefix.Contains(netip.MustParseAddr(r.Gateway)) {
		return errs.Invalid("安装网关必须与客户机处于同一网段")
	}
	if s.Network != nil {
		n, e := s.Network(ctx)
		if e != nil {
			return e
		}
		if !n.Known || !n.Contains(ip) || n.IsServerAddr(ip) {
			return errs.Invalid("请选择客户机启动网络内未被服务器占用的地址")
		}
	}
	ts, err := s.Store.Terminals().List(ctx)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.IP == r.IP && storage.NormalizeMAC(t.MAC) != r.MAC {
			return errs.Conflict("该 IP 已分配给客户机「" + t.Name + "」")
		}
	}
	sessions, err := s.Store.InstallSessions().List(ctx)
	if err != nil {
		return err
	}
	for _, v := range sessions {
		if v.Active() && v.IP == r.IP {
			return errs.Conflict("该 IP 已被其他安装任务使用")
		}
	}
	return nil
}

func (s *Service) Report(ctx context.Context, token string, r Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().ByToken(ctx, token)
	if err != nil {
		return err
	}
	if err := s.ensureOwner(sess); err != nil {
		return err
	}
	if r.Attempt != sess.Attempt {
		return errs.Conflict("安装回报属于旧任务，请重新获取安装配置")
	}
	if sess.Phase == "ready" && r.Phase == "verified" && r.BootID == sess.BootID {
		return nil
	}
	if !sess.Active() {
		return errs.Conflict("安装任务已结束")
	}
	now := s.now()
	sess.LastReportAt = &now
	switch r.Phase {
	case "installing":
		if sess.Phase != "waiting" && sess.Phase != "installing" {
			return errs.Conflict("安装阶段已结束，请勿重新安装")
		}
		sess.Phase = "installing"
		sess.Message = "正在安装系统"
		if r.Progress >= 0 && r.Progress <= 100 {
			sess.Progress = r.Progress * 70 / 100
		}
	case "initializing":
		if sess.Phase == "initializing" {
			return nil
		}
		if sess.Phase != "installing" {
			return errs.Conflict("安装文件尚未完成，不能进入首次系统初始化")
		}
		sess.Phase = "initializing"
		sess.Progress = 72
		sess.Message = "安装文件已准备好，正在首次启动系统"
	case "installed":
		if sess.Phase == "installed" || sess.Phase == "saving" || sess.Phase == "verifying" {
			return nil
		}
		if sess.Phase != "initializing" {
			return errs.Conflict("不能在当前阶段保存安装结果")
		}
		sess.Phase = "installed"
		sess.Progress = 75
		sess.Message = "系统安装完成，等待客户机断开后保存镜像"
	case "verified":
		if sess.Phase != "verifying" || sess.BootID == "" || r.BootID != sess.BootID {
			return errs.Conflict("请通过普通无盘克隆启动后再验证镜像")
		}
		active, e := s.Storage.ActiveClientMACs(ctx)
		if e != nil {
			return e
		}
		found := false
		for _, m := range active {
			if storage.NormalizeMAC(m) == sess.MAC {
				found = true
			}
		}
		if !found {
			return errs.Conflict("尚未检测到验证客户机的磁盘连接，请稍后重试")
		}
		sess.Phase = "ready"
		sess.Message = "镜像已就绪，已验证普通无盘启动"
		sess.Progress = 100
		private := sess
		sess.Password = ""
		img, cfg, red := sess.CandidateImage, sess.CandidateConfig, sess.CandidateReduction
		if img.ID == "" || cfg.ID == "" || red.ID == "" {
			return errs.Conflict("待验证镜像尚未保存")
		}
		img.State = domain.ImageStateNormal
		img.Origin = domain.ImageOriginImported
		img.Remark = "由 ISO 创建并验证无盘启动"
		img.Purpose = domain.ImagePurposeSystem
		red.Status = domain.ReductionStatusReady
		done := storage.ChangeCatalogue()
		err = s.Store.Tx(ctx, func(tx store.Store) error {
			if e := tx.Images().Create(ctx, img); e != nil {
				return e
			}
			if e := tx.Configs().Create(ctx, cfg); e != nil {
				return e
			}
			if e := tx.Reductions().Create(ctx, red); e != nil {
				return e
			}
			if e := recordVerifiedBoot(ctx, tx, sess, now); e != nil {
				return e
			}
			sess.UpdatedAt = now
			if e := tx.InstallSessions().Update(ctx, sess); e != nil {
				return e
			}
			task, e := tx.Tasks().Get(ctx, sess.TaskID)
			if e != nil {
				return e
			}
			return (tasks.Runner{Now: s.Now}).Finish(ctx, tx, task, sess.ImageID)
		})
		done()
		if err == nil {
			err = s.cleanupMedia(private)
		}
		if err == nil && s.SyncDHCP != nil {
			err = s.SyncDHCP(ctx)
		}
		return err
	case "failed":
		if sess.Phase == "cancelling" || sess.Phase == "failed" {
			return nil
		}
		if strings.TrimSpace(r.Message) == "" {
			return errs.Invalid("失败回报需要说明具体原因")
		}
		return s.fail(ctx, sess, r.Message)
	default:
		return errs.Invalid("安装阶段无效")
	}
	if strings.TrimSpace(r.Message) != "" {
		sess.Message = strings.TrimSpace(r.Message)
	}
	return s.update(ctx, sess)
}

// Finalize retries only the save checkpoint; it never re-runs an installer or formats a disk.
func (s *Service) Finalize(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ensureOwner(sess); err != nil {
		return err
	}
	if sess.Phase == "verifying" || sess.Phase == "ready" {
		return nil
	}
	if sess.Phase != "installed" && sess.Phase != "saving" {
		return errs.Conflict("系统尚未安装完成，不能保存镜像")
	}
	active, err := s.Storage.ActiveClientMACs(ctx)
	if err != nil {
		return err
	}
	for _, mac := range active {
		if storage.NormalizeMAC(mac) == sess.MAC {
			sess.DisconnectedAt = nil
			_ = s.update(ctx, sess)
			return errs.Conflict("请等待安装客户机关机，磁盘仍在使用中")
		}
	}
	grace := s.SaveGrace
	if grace == 0 {
		grace = 30 * time.Second
	}
	if grace > 0 {
		if sess.DisconnectedAt == nil {
			now := s.now()
			sess.DisconnectedAt = &now
			_ = s.update(ctx, sess)
			return errs.Conflict("正在确认安装盘已安全断开，请稍后保存")
		}
		if s.now().Sub(*sess.DisconnectedAt) < grace {
			return errs.Conflict("正在确认安装盘已安全断开，请稍后保存")
		}
	}
	if sess.Phase == "installed" {
		sess.Phase = "saving"
		sess.Message = "正在保存待验证镜像"
		if err := s.update(ctx, sess); err != nil {
			return err
		}
	}
	// 烘焙标准启动器并记下版本，客户机开机就不再逐台注入；验证那次开机由 /boot/mount-script 另带验证回报。
	var script []byte
	version := ""
	if sess.OSType == domain.OSTypeWindows && s.BakeMountScript != nil {
		script, version = s.BakeMountScript()
	} else if s.MountScript != nil {
		script = s.MountScript(sess)
	}
	defer storage.ChangeCatalogue()()
	saved, err := s.Host.SaveInstallation(ctx, storage.InstallationSaveReq{ID: sess.ID, MAC: sess.MAC, ImageID: sess.ImageID, Name: sess.Name, OSType: sess.OSType, SizeBytes: sess.SizeBytes, MountScript: script})
	if err != nil {
		sess.Error = "保存镜像失败，请重试保存：" + err.Error()
		_ = s.update(ctx, sess)
		return err
	}
	if saved.MountScriptInjected {
		saved.Image.MountScriptVersion = version
	}
	sess.CandidateImage, sess.CandidateConfig, sess.CandidateReduction = saved.Image, saved.Config, saved.Reduction
	sess.Phase = "verifying"
	sess.Error = ""
	sess.Progress = 90
	sess.BootID = ""
	sess.Message = "镜像已保存，请再次通过网络启动客户机验证无盘启动"
	return s.update(ctx, sess)
}

// VerificationMountScript 是 ISO 安装验证那次开机，镜像里的启动器取到的挂盘脚本：标准脚本另带验证回报。
func (s *Service) VerificationMountScript(ctx context.Context, mac string) ([]byte, bool, error) {
	if s.MountScript == nil {
		return nil, false, nil
	}
	sess, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac))
	if errs.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if sess.OSType != domain.OSTypeWindows || sess.Phase != "verifying" {
		return nil, false, nil
	}
	return s.MountScript(sess), true, nil
}

// isoInstallNonUEFIScript 给误用 BIOS/Legacy 网络启动的 ISO 安装客户机看。
// iPXE echo 必须用 ASCII：BIOS VGA 控制台无法可靠显示 UTF-8 中文（会乱码）。
// 中文说明留给 Web UI / API（writeError、会话 Message），不要写进 echo 行。
func isoInstallNonUEFIScript(firmware string) string {
	return "#!ipxe\n" +
		"echo ISO install blocked: this PXE boot is BIOS/Legacy (platform=" + firmware + "), not UEFI.\n" +
		"echo ISO install requires UEFI network boot (load snponly.efi, NOT undionly.kpxe).\n" +
		"echo Fix in VMware: VM Settings -> Options -> Boot Options -> Firmware = EFI.\n" +
		"echo Boot order: Network only (remove local disk / local ISO). Then power off and power on.\n" +
		"echo Do not boot from local disk before changing firmware. Task stays Waiting for client.\n" +
		"sleep 30\n" +
		"exit\n"
}

func (s *Service) BootScript(ctx context.Context, mac string) (string, bool, error) {
	// Ordinary clients must not queue behind ISO extraction or disk preparation.
	if _, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac)); errs.IsNotFound(err) {
		return "", false, nil
	} else if err != nil {
		return "", true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac))
	if errs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if err := s.ensureOwner(sess); err != nil {
		return "", true, err
	}
	if sess.Firmware != "" && sess.Firmware != "efi" {
		// iPXE 执行 200 脚本才能在客户机屏幕上读到提示；返回 Invalid/500 只会显示 HTTP 错误码。
		return isoInstallNonUEFIScript(sess.Firmware), true, nil
	}
	switch sess.Phase {
	case "preparing":
		return "#!ipxe\necho Preparing installation resources. Please retry after the task is ready.\nsleep 20\nexit\n", true, nil
	case "installing":
		return "#!ipxe\necho Installation was interrupted. Check the task center before restarting installation.\nsleep 30\nexit\n", true, nil
	case "initializing":
		// 来要盘的就是这台客户机：上一轮会话已作废；从装好的盘启动，不再挂安装介质。
		info, err := s.Host.PrepareInstallation(ctx, storage.InstallationDiskReq{ID: sess.ID, MAC: sess.MAC, SizeBytes: sess.SizeBytes, Booting: true})
		if err != nil {
			return "", true, err
		}
		info.Server = s.portal(info.Server)
		builder := s.Builder
		if builder == nil {
			builder = boot.Builder{}
		}
		script, err := builder.Build(domain.Terminal{MAC: sess.MAC, IP: sess.IP}, domain.Group{Netmask: sess.Netmask, Gateway: sess.Gateway, DNS1: sess.DNS1}, domain.Image{OSType: sess.OSType}, info)
		return script, true, err
	case "waiting":
		// Once setup reports installation complete, a reboot can no longer return to destructive installation.
		files, err := s.media(ctx, sess)
		if err != nil {
			return "", true, err
		}
		script, err := s.Render(ctx, sess, files)
		return script, true, err
	case "verifying":
		var script []byte
		if s.MountScript != nil {
			script = s.MountScript(sess)
		}
		info, err := s.Storage.CreateClientLUN(ctx, storage.ClientReq{MAC: sess.MAC, IP: sess.IP, System: storage.ClientSource{ImageID: sess.CandidateImage.ID, ConfigID: sess.CandidateConfig.ID, SnapshotName: strings.TrimPrefix(sess.CandidateReduction.Name, "@"), LUN: 0}, MountScript: script})
		if err != nil {
			return "", true, err
		}
		sess.BootID, err = randomID()
		if err != nil {
			return "", true, err
		}
		sess.BootStartedAt = s.now()
		sess.Message = "已下发普通无盘启动，等待系统启动回报"
		if err := s.update(ctx, sess); err != nil {
			return "", true, err
		}
		info.Server = s.portal(info.Server)
		builder := s.Builder
		if builder == nil {
			builder = boot.Builder{}
		}
		rendered, err := builder.Build(domain.Terminal{MAC: sess.MAC, IP: sess.IP}, domain.Group{Netmask: sess.Netmask, Gateway: sess.Gateway, DNS1: sess.DNS1}, sess.CandidateImage, info)
		return rendered, true, err
	default:
		return "#!ipxe\necho Installation is waiting for safe save or cancellation. Check the task center.\nsleep 20\nexit\n", true, nil
	}
}

func (s *Service) Cancel(ctx context.Context, id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CanWrite != nil {
		if err := s.CanWrite(); err != nil {
			return View{}, err
		}
	}
	sess, err := s.Store.InstallSessions().Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if sess.Phase == "ready" {
		return view(sess), errs.Conflict("镜像已就绪，请使用镜像管理删除")
	}
	if sess.Phase == "cancelled" {
		return view(sess), nil
	}
	if sess.ImageID != "" {
		if _, err := s.Store.Images().Get(ctx, sess.ImageID); err == nil {
			return view(sess), errs.Conflict("镜像已发布，请使用镜像管理删除")
		} else if !errs.IsNotFound(err) {
			return view(sess), err
		}
	}
	sess.Phase = "cancelling"
	sess.Message = "正在取消，等待客户机释放磁盘"
	if err := s.update(ctx, sess); err != nil {
		return view(sess), err
	}
	active, err := s.Storage.ActiveClientMACs(ctx)
	if err != nil {
		return view(sess), err
	}
	for _, m := range active {
		if storage.NormalizeMAC(m) == sess.MAC {
			return view(sess), nil
		}
	}
	defer storage.ChangeCatalogue()()
	if s.NodeID != "" && sess.ServerID != "" && sess.ServerID != s.NodeID {
		if s.CleanupRemote == nil {
			return view(sess), errs.Conflict("请恢复原安装节点后重试结束安装；安装资源仍保留")
		}
		if err := s.CleanupRemote(ctx, cleanupRequest(sess, sess.ServerID)); err != nil {
			return view(sess), err
		}
	}
	if err := s.cleanupInstallationResources(ctx, sess); err != nil {
		return view(sess), err
	}
	task, err := s.Store.Tasks().Get(ctx, sess.TaskID)
	if err == nil {
		err = (tasks.Runner{Store: s.Store, Now: s.Now}).Fail(ctx, task, fmt.Errorf("安装任务已取消"))
	}
	if err != nil {
		return view(sess), err
	}
	private := sess
	sess.Phase = "cancelled"
	sess.Password = ""
	sess.Message = "安装任务已取消"
	if err := s.update(ctx, sess); err != nil {
		return view(sess), err
	}
	if s.SyncDHCP != nil {
		err = s.SyncDHCP(ctx)
		if err != nil {
			// Keep credentials until every cleanup step can be retried successfully.
			if resetErr := s.update(ctx, private); resetErr != nil {
				return view(sess), fmt.Errorf("%v；恢复清理状态失败：%w", err, resetErr)
			}
			return view(private), err
		}
	}
	return view(sess), err
}
func (s *Service) Retry(ctx context.Context, id string) (View, error) {
	if err := s.Finalize(ctx, id); err != nil {
		return View{}, err
	}
	return s.Get(ctx, id)
}
func (s *Service) Resume(ctx context.Context) {
	all, err := s.Store.InstallSessions().List(ctx)
	if err != nil {
		return
	}
	for _, v := range all {
		if v.Phase != "cancelling" && s.ensureOwner(v) != nil {
			continue
		}
		switch v.Phase {
		case "preparing":
			_ = s.prepare(ctx, v.ID)
		case "installed", "saving":
			_ = s.Finalize(ctx, v.ID)
		case "cancelling":
			_, _ = s.Cancel(ctx, v.ID)
		case "installing", "initializing":
			_ = s.expire(ctx, v.ID)
		case "ready", "cancelled":
			_ = s.cleanupMedia(v)
		}
	}
}
func (s *Service) RunSweeper(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Resume(ctx)
		}
	}
}
func (s *Service) update(ctx context.Context, v domain.InstallSession) error {
	v.Message = string(s.redact(v, []byte(v.Message)))
	v.Error = string(s.redact(v, []byte(v.Error)))
	v.UpdatedAt = s.now()
	return s.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.InstallSessions().Update(ctx, v); err != nil {
			return err
		}
		task, err := tx.Tasks().Get(ctx, v.TaskID)
		if err != nil {
			return err
		}
		task.Message, task.Progress = v.Message, v.Progress
		return tx.Tasks().Update(ctx, task)
	})
}
func (s *Service) fail(ctx context.Context, v domain.InstallSession, msg string) error {
	msg = string(s.redact(v, []byte(msg)))
	v.Phase = "failed"
	v.Error = msg
	v.Message = msg
	v.UpdatedAt = s.now()
	if err := s.Store.InstallSessions().Update(ctx, v); err != nil {
		return err
	}
	task, err := s.Store.Tasks().Get(ctx, v.TaskID)
	if err == nil {
		err = (tasks.Runner{Store: s.Store, Now: s.Now}).Fail(ctx, task, fmt.Errorf("%s", msg))
	}
	if s.SyncDHCP != nil {
		_ = s.SyncDHCP(ctx)
	}
	if err != nil {
		return err
	}
	return nil
}
func randomID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Service) abort(ctx context.Context, v domain.InstallSession, msg string) error {
	if err := s.fail(ctx, v, msg); err != nil {
		return err
	}
	return fmt.Errorf("%s", msg)
}

func (s *Service) ensureOwner(v domain.InstallSession) error {
	if s.CanWrite != nil {
		if err := s.CanWrite(); err != nil {
			return err
		}
	}
	if s.NodeID != "" && v.ServerID != "" && v.ServerID != s.NodeID {
		return errs.Conflict("安装任务位于其他存储节点，请恢复原执行节点后继续；为保护安装盘，当前节点不会重新创建或格式化磁盘")
	}
	return nil
}

func (s *Service) media(ctx context.Context, v domain.InstallSession) (MediaFiles, error) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	return s.Prepare(ctx, v)
}

// Storage agent addressing uses node IDs; the boot script needs the client-facing portal.
func (s *Service) portal(fallback string) string {
	if u, err := url.Parse(s.ReportBase); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return fallback
}

var initialAccountPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)

// checkInitialAccount 账户名和密码分开报：点名哪一项、差在哪。
func checkInitialAccount(name, password string) error {
	switch {
	case reservedInitialAccount(name):
		return errs.Invalid("初始账户不能用系统保留名「" + name + "」，请换一个，例如 nexboot")
	case !initialAccountPattern.MatchString(name):
		return errs.Invalid("初始账户「" + name + "」不可用：须为 1 至 20 位，小写字母开头，只含小写字母、数字、下划线和连字符")
	case len(password) < 8:
		return errs.Invalid(fmt.Sprintf("初始密码至少 8 位，现在 %d 位", utf8.RuneCountInString(password)))
	case len(password) > 127:
		return errs.Invalid("初始密码最多 127 位，请缩短后再试")
	case strings.ContainsAny(password, "\x00\r\n"):
		return errs.Invalid("初始密码不能包含换行")
	}
	return nil
}

func reservedInitialAccount(name string) bool {
	switch name {
	case "administrator", "guest", "root", "defaultaccount", "wdagutilityaccount", "nobody":
		return true
	}
	return false
}

func validInitialAccount(name string) bool {
	if !initialAccountPattern.MatchString(name) {
		return false
	}
	switch name {
	case "administrator", "guest", "root", "defaultaccount", "wdagutilityaccount", "nobody":
		return false
	}
	return true
}

func (s *Service) expire(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().Get(ctx, id)
	if err != nil {
		return err
	}
	if err = s.ensureOwner(sess); err != nil {
		return err
	}
	timeout := 2 * time.Hour
	label := "安装系统"
	if sess.Phase == "initializing" {
		timeout = 45 * time.Minute
		label = "首次启动系统"
	} else if sess.Phase != "installing" {
		return nil
	}
	since := sess.UpdatedAt
	if sess.LastReportAt != nil && sess.LastReportAt.After(since) {
		since = *sess.LastReportAt
	}
	if s.now().Sub(since) < timeout {
		return nil
	}
	return s.fail(ctx, sess, "客户机「"+sess.MAC+"」"+label+"长时间没有回报；请检查客户机屏幕与网络，在任务详情查看日志。安装盘已保留，确认关机后取消任务并重新创建")
}

func (s *Service) RecordHardware(ctx context.Context, mac string, hints hwprofile.Hints) (bool, error) {
	if _, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac)); errs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac))
	if errs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err = s.ensureOwner(sess); err != nil {
		return true, err
	}
	sess.Firmware = hints.Platform
	if vendor, device, ok := hwprofile.ParseBusID(hints.BusID); ok {
		sess.HWID = "PCI\\VEN_" + vendor + "&DEV_" + device
		sess.BusLoc = hints.BusLoc
	}
	return true, s.Store.InstallSessions().Update(ctx, sess)
}

func (s *Service) RecordBootFailure(ctx context.Context, mac, stage, code string) (bool, error) {
	if _, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac)); errs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac))
	if errs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err = s.ensureOwner(sess); err != nil {
		return true, err
	}
	sess.Error = "客户机网络启动失败，请检查客户机启动网络和磁盘连接：" + stage + " " + code
	return true, s.update(ctx, sess)
}

// A verified report belongs to this session's issued clone, never the terminal's previous point.
func recordVerifiedBoot(ctx context.Context, st store.Store, sess domain.InstallSession, now time.Time) error {
	if sess.BootStartedAt.IsZero() {
		return nil
	} // Older sessions have no reliable start ledger.
	t, err := st.Terminals().GetByMAC(ctx, sess.MAC)
	if errs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if t.LastBootAt != nil && t.LastBootAt.After(sess.BootStartedAt) {
		return nil
	}
	before := t
	profileID := ""
	if t.HardwareProfileID != "" {
		p, e := st.HardwareProfiles().Get(ctx, t.HardwareProfileID)
		if e != nil && !errs.IsNotFound(e) {
			return e
		}
		hwid := fmt.Sprintf(`PCI\VEN_%s&DEV_%s`, p.NICVendor, p.NICDevice)
		if e == nil && p.NICSubsys == "" && strings.EqualFold(sess.HWID, hwid) && sess.Firmware == p.Platform && sess.BusLoc == p.BusLoc {
			profileID = p.ID
		}
	}
	t.LastBootAt, t.LastBootOKAt = &sess.BootStartedAt, &now
	t.LastBootReductionID, t.LastBootProfileID = sess.CandidateReduction.ID, profileID
	_, err = st.Terminals().UpdateBootIfUnchanged(ctx, before, t)
	return err
}
