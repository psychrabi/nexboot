package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

const (
	// A later session this long retires an older missing-report inference; it is not Windows success.
	bootedAfter = 3 * time.Minute
	// 没修也没再开机的，一天后不再提。
	bootFailureAlarmFor = 24 * time.Hour
)

// BootFailureAlarmRule 展示每台机器最近一次开机失败，直到它开机成功或失败已超过一天。
func BootFailureAlarmRule(st store.Store, now func() time.Time) AlarmRule {
	return AlarmRule{Source: "boot", Eval: func(ctx context.Context) ([]AlarmCondition, error) {
		failures, err := st.BootFailures().List(ctx)
		if err != nil {
			return nil, err
		}
		terminals, err := st.Terminals().List(ctx)
		if err != nil {
			return nil, err
		}
		byMAC := map[string]domain.Terminal{}
		for _, t := range terminals {
			byMAC[storage.NormalizeMAC(t.MAC)] = t
		}
		images, err := st.Images().List(ctx)
		if err != nil {
			return nil, err
		}
		imageName := map[string]string{}
		for _, img := range images {
			imageName[img.ID] = img.Name
		}
		at := now().UTC()
		var conds []AlarmCondition
		for _, t := range terminals {
			if t.State != domain.TerminalStateOffline || t.LastBootAt == nil || t.OfflineAt == nil ||
				t.OfflineAt.Before(*t.LastBootAt) || t.OfflineAt.Sub(*t.LastBootAt) > time.Minute ||
				at.Sub(*t.OfflineAt) > bootFailureAlarmFor ||
				(t.LastBootOKAt != nil && !t.LastBootOKAt.Before(*t.LastBootAt)) {
				continue
			}
			v, err := st.TerminalIdentities().Get(ctx, t.ID)
			if errs.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if v.Source != "computed" || v.ProfileID != t.LastBootProfileID || v.ProfileID != t.HardwareProfileID || v.LayerReductionRef != t.LastBootReductionID {
				continue
			}
			name := t.Name
			if name == "" {
				name = t.MAC
			}
			conds = append(conds, AlarmCondition{Key: "computed:" + t.MAC, Severity: "warn", Type: "网卡身份待确认", Resource: name,
				Message: fmt.Sprintf("客户机「%s」当前网卡身份来自计算，本次开机后很快断开；若未进入 Windows，请到客户机详情点「重新取证」，随后再开机", name)})
		}
		for _, f := range failures {
			t, ok := byMAC[f.MAC]
			if !ok || at.Sub(f.At) > bootFailureAlarmFor || t.LastBootOKAt != nil && !t.LastBootOKAt.Before(f.At) {
				continue
			}
			if resolvedMissingReport(t, f, at) {
				removed, err := st.BootFailures().DeleteIfUnchanged(ctx, f)
				if err != nil {
					return nil, err
				}
				if removed {
					continue
				}
			}
			name := t.Name
			if name == "" {
				name = t.MAC
			}
			img := imageName[f.ImageID]
			if img == "" {
				img = f.ImageID
			}
			severity, kind := "error", "开机失败"
			if f.Stage == domain.BootStageWindows {
				severity, kind = "warn", "启动待确认"
			}
			conds = append(conds, AlarmCondition{
				Key:      "boot:" + f.MAC,
				Severity: severity,
				Type:     kind,
				Resource: name,
				Value:    f.Stage,
				Message:  bootFailureMessage(name, img, f) + "（记录时间：" + clock(f.At) + "）",
			})
		}
		return conds, nil
	}}
}

// Stable iSCSI is only enough to retire an older missing-report inference, never to assert Windows success.
func resolvedMissingReport(t domain.Terminal, f domain.BootFailure, now time.Time) bool {
	if f.Stage != domain.BootStageWindows || t.LastBootAt == nil || !f.At.Before(*t.LastBootAt) || t.OnlineSince == nil {
		return false
	}
	from := *t.OnlineSince
	if t.LastBootAt.After(from) {
		from = *t.LastBootAt
	}
	end := now
	if t.State == domain.TerminalStateOffline {
		if t.OfflineAt == nil || t.OfflineAt.After(now) {
			return false
		}
		end = *t.OfflineAt
	} else if t.State != domain.TerminalStateOnline {
		return false
	}
	return end.Sub(from) >= bootedAfter
}

func bootFailureMessage(name, image string, f domain.BootFailure) string {
	code := ""
	if f.Code != "" {
		code = "，错误码 " + f.Code
	}
	switch f.Stage {
	case domain.BootStageWindows:
		return fmt.Sprintf("请先查看客户机 %s 的实际画面：镜像「%s」上次启动未收到 Windows 启动成功回报，当前结果待确认；仅在确认蓝屏 0x7B 等问题后，到「机型」页检查适配", name, image)
	case domain.BootStageBootMode:
		fw, other := "UEFI", "BIOS"
		if f.Platform == "pcbios" {
			fw, other = "BIOS", "UEFI"
		}
		return fmt.Sprintf("%s 按 %s 启动，镜像 %s 只能 %s 引导：请把客户机固件改成 %s，或给它的分组换一个支持 %s 引导的镜像",
			name, fw, image, other, other, fw)
	case domain.BootStageSanhook:
		return fmt.Sprintf("%s 开机时连不上系统盘（iSCSI 登录失败%s）：请检查客户机到服务器 3260 端口的网络，以及服务器上的 iSCSI 服务", name, code)
	default:
		return fmt.Sprintf("%s 挂上了系统盘但没能从盘上引导（镜像 %s%s）：请到「镜像」页给 %s 重跑体检，看引导方式与客户机固件是否一致", name, image, code, image)
	}
}
