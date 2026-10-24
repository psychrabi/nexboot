package control

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

// RecordBootFailure 记录客户机 iPXE 脚本放弃时上报的失败。
func (s BootService) RecordBootFailure(ctx context.Context, mac, stage, code, platform string) error {
	if recorder, ok := s.Install.(interface {
		RecordBootFailure(context.Context, string, string, string) (bool, error)
	}); ok {
		if handled, err := recorder.RecordBootFailure(ctx, mac, stage, code); handled || err != nil {
			return err
		}
	}
	if sess, ok, err := s.installSession(ctx, mac); err != nil {
		return err
	} else if ok {
		sess.Error = "客户机网络启动失败，请检查客户机启动网络和磁盘连接：" + stage + " " + code
		return s.Store.InstallSessions().Update(ctx, sess)
	}
	if stage != domain.BootStageSanhook && stage != domain.BootStageSanboot {
		return errs.Invalid(fmt.Sprintf("未知的开机阶段 %q", stage))
	}
	t, err := s.terminalByMAC(ctx, mac)
	if err != nil {
		return err
	}
	imageID, modes, _, err := s.bootModes(ctx, t)
	if err != nil {
		return err
	}
	// 盘挂上了却引导不了，引导方式不符就是最具体的原因。
	if stage == domain.BootStageSanboot && bootModeMismatch(platform, modes) {
		stage = domain.BootStageBootMode
	}
	return s.Store.BootFailures().Upsert(ctx, domain.BootFailure{
		MAC: storage.NormalizeMAC(t.MAC), Stage: stage, Code: ipxeErrno(code),
		Platform: platform, ImageID: imageID, At: s.now(),
	})
}

// CheckBootMode 在客户机尝试前检查：固件启动不了即将下发的镜像时先记一次失败。
func (s BootService) CheckBootMode(ctx context.Context, mac, platform string) error {
	if _, ok, err := s.installSession(ctx, mac); err != nil {
		return err
	} else if ok {
		return nil
	}
	t, err := s.terminalByMAC(ctx, mac)
	if err != nil {
		return err
	}
	imageID, modes, osType, err := s.bootModes(ctx, t)
	if err != nil || !firmwareImageMismatch(platform, osType, modes) {
		return err
	}
	return s.Store.BootFailures().Upsert(ctx, domain.BootFailure{
		MAC: storage.NormalizeMAC(t.MAC), Stage: domain.BootStageBootMode,
		Platform: platform, ImageID: imageID, At: s.now(),
	})
}

// DisklessFirmwareReject 在普通无盘开机（非 ISO 安装）时：BIOS 固件开不了 UEFI-only
// Windows 镜像则返回 ASCII iPXE 提示脚本，且不走 sanhook/sanboot（避免卡在 Booting from SAN）。
// reject=false 表示继续正常 BuildBootScript。ISO 安装的同类拒绝见 install.isoInstallNonUEFIScript。
func (s BootService) DisklessFirmwareReject(ctx context.Context, mac, platform string) (script string, reject bool, err error) {
	if _, ok, err := s.installSession(ctx, mac); err != nil {
		return "", false, err
	} else if ok {
		return "", false, nil
	}
	if platform != "pcbios" {
		return "", false, nil
	}
	t, err := s.terminalByMAC(ctx, mac)
	if err != nil {
		return "", false, err
	}
	imageID, modes, osType, err := s.bootModes(ctx, t)
	if err != nil {
		return "", false, err
	}
	if !firmwareImageMismatch(platform, osType, modes) {
		return "", false, nil
	}
	if err := s.Store.BootFailures().Upsert(ctx, domain.BootFailure{
		MAC: storage.NormalizeMAC(t.MAC), Stage: domain.BootStageBootMode,
		Platform: platform, ImageID: imageID, At: s.now(),
	}); err != nil {
		return "", false, err
	}
	return disklessNonUEFIScript(platform), true, nil
}

// disklessNonUEFIScript 给误用 BIOS/Legacy 网络启动的无盘客户机看。
// iPXE echo 必须用 ASCII：BIOS VGA 控制台无法可靠显示 UTF-8 中文（会乱码）。
func disklessNonUEFIScript(firmware string) string {
	if firmware == "" {
		firmware = "pcbios"
	}
	return "#!ipxe\n" +
		"echo Diskless boot blocked: this PXE boot is BIOS/Legacy (platform=" + firmware + "), not UEFI.\n" +
		"echo This image is UEFI-only (GPT/ESP). BIOS sanboot would hang at \"Booting from SAN device\".\n" +
		"echo Requires UEFI network boot (load snponly.efi, NOT undionly.kpxe).\n" +
		"echo Fix in VMware: VM Settings -> Options -> Boot Options -> Firmware = EFI.\n" +
		"echo Boot order: Network only (remove local disk). Then power off and power on.\n" +
		"sleep 30\n" +
		"exit\n"
}

func (s BootService) bootModes(ctx context.Context, t domain.Terminal) (string, []string, domain.OSType, error) {
	g, err := s.Store.Groups().Get(ctx, t.GroupID)
	if err != nil {
		return "", nil, "", err
	}
	img, err := s.Store.Images().Get(ctx, g.SystemImageID)
	if err != nil {
		return "", nil, "", err
	}
	report, err := s.Store.ImageHealthReports().GetByImage(ctx, g.SystemImageID)
	if errs.IsNotFound(err) {
		return g.SystemImageID, nil, img.OSType, nil
	}
	if err != nil {
		return g.SystemImageID, nil, img.OSType, err
	}
	return g.SystemImageID, report.BootModes, img.OSType, nil
}

func bootModeMismatch(platform string, modes []string) bool {
	if len(modes) == 0 {
		return false
	}
	switch platform {
	case "efi":
		return !slices.Contains(modes, domain.BootModeUEFI)
	case "pcbios":
		return !slices.Contains(modes, domain.BootModeBIOS)
	}
	return false
}

// firmwareImageMismatch 判断客户机固件与镜像启动方式是否冲突。
// 体检尚未写出 boot_modes 时：无盘 Windows 按 GPT/ESP UEFI-only 处理（与 ISO 安装一致），
// 避免 BIOS 客户机 sanboot 后静默卡在 Booting from SAN。
func firmwareImageMismatch(platform string, osType domain.OSType, modes []string) bool {
	if bootModeMismatch(platform, modes) {
		return true
	}
	return platform == "pcbios" && osType == domain.OSTypeWindows && len(modes) == 0
}

// ipxeErrno 按 ipxe.org/err 的写法显示 iPXE 错误码。
func ipxeErrno(code string) string {
	n, err := strconv.ParseUint(code, 10, 32)
	if err != nil || n == 0 {
		return code
	}
	return fmt.Sprintf("0x%08x", n)
}

func (s BootService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
