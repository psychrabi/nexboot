package hwprofile

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// Service 维护机型与「还原点 × 机型」的适配记录。
type Service struct {
	Store  store.Store
	Now    func() time.Time
	Logger *slog.Logger
}

// Observe 把 /boot 上报的硬件归并成机型并挂到终端。没有网卡 ID 的请求（老 iPXE）不改动终端。
func (s Service) Observe(ctx context.Context, t domain.Terminal, h Hints) (domain.Terminal, error) {
	want, ok := ProfileFromHints(h)
	if !ok {
		return t, nil
	}
	profile, err := s.Store.HardwareProfiles().ByFingerprint(ctx, want.Fingerprint)
	if errs.IsNotFound(err) {
		now := s.now()
		want.ID, want.CreatedAt, want.UpdatedAt = fmt.Sprintf("hwp-%d", time.Now().UnixNano()), now, now
		if err := s.Store.HardwareProfiles().Create(ctx, want); err != nil {
			// 同机型两台机器同时首次开机：输掉的那台读回赢家建的。
			if profile, err = s.Store.HardwareProfiles().ByFingerprint(ctx, want.Fingerprint); err != nil {
				return t, err
			}
		} else {
			profile = want
		}
	} else if err != nil {
		return t, err
	}
	if t.HardwareProfileID == profile.ID {
		return t, nil
	}
	t.HardwareProfileID = profile.ID
	return t, s.Store.Terminals().UpdateHardwareProfile(ctx, t)
}

// NoteBootStarted records the issued point. A missing callback alone proves neither failure nor success.
func (s Service) NoteBootStarted(ctx context.Context, t domain.Terminal, img domain.Image, reductionID string) (domain.Terminal, error) {
	attemptAt := s.now()
	now := attemptAt
	for attempt := 0; attempt < 2; attempt++ {
		if t.LastBootAt != nil && t.LastBootAt.After(attemptAt) {
			break
		}
		before := t
		t.LastBootAt, t.LastBootReductionID, t.LastBootProfileID = &now, reductionID, t.HardwareProfileID
		changed, err := s.Store.Terminals().UpdateBootIfUnchanged(ctx, before, t)
		if err != nil || changed {
			return t, err
		}
		if attempt == 0 {
			t, err = s.Store.Terminals().Get(ctx, t.ID)
			if err != nil {
				return t, err
			}
			// The old boot may have reported success after our first attempt timestamp.
			// Keep that success, but put this new start strictly after it.
			now = s.now()
			if t.LastBootOKAt != nil && !now.After(*t.LastBootOKAt) {
				break
			}
		}
	}
	return t, errs.Conflict("客户机启动状态已更新，请重新获取启动配置")
}

// NoteBooted 是开机脚本回调：记成功时间，并把「这个机型用这个还原点能开机」记成一条 observed。
func (s Service) NoteBooted(ctx context.Context, t domain.Terminal) error {
	fresh, err := s.Store.Terminals().Get(ctx, t.ID)
	if err != nil {
		return err
	}
	before := fresh
	now := s.now()
	fresh.LastBootOKAt = &now
	changed, err := s.Store.Terminals().UpdateBootIfUnchanged(ctx, before, fresh)
	if err != nil || !changed {
		return err
	}
	if fresh.HardwareProfileID == "" || fresh.LastBootProfileID != fresh.HardwareProfileID || fresh.LastBootReductionID == "" {
		return nil
	}
	if used, err := s.noteTemplateBooted(ctx, fresh); err != nil || used {
		return err
	}
	if _, err := s.Store.ModelAdaptations().ForProfileReduction(ctx, fresh.HardwareProfileID, fresh.LastBootReductionID); err == nil {
		return nil
	} else if !errs.IsNotFound(err) {
		return err
	}
	red, err := s.Store.Reductions().Get(ctx, fresh.LastBootReductionID)
	if err != nil {
		if errs.IsNotFound(err) {
			return nil
		}
		return err
	}
	err = s.Store.ModelAdaptations().Create(ctx, domain.ModelAdaptation{
		ID: fmt.Sprintf("ad-%d", time.Now().UnixNano()), ProfileID: fresh.HardwareProfileID, ReductionID: red.ID, ConfigID: red.ConfigID,
		Source: domain.AdaptationObserved, TerminalID: fresh.ID, CreatedAt: now,
	})
	if err != nil {
		// 同机型两台同时回调：唯一索引挡住第二条即可。
		if _, again := s.Store.ModelAdaptations().ForProfileReduction(ctx, fresh.HardwareProfileID, red.ID); again == nil {
			return nil
		}
	}
	return err
}

// templateBootWindow 内回调的开机才算「这次开机用了网卡模板」：身份层记下模板开机的时间与终端开机时间应几乎同时。
const templateBootWindow = 2 * time.Minute

// noteTemplateBooted 在这次开机用了网卡模板时，只在身份记录上记成功。
// 模板开机不能写进适配表：写了，机型就被当成已适配，「未适配」提示与「开始适配」入口都会消失。
func (s Service) noteTemplateBooted(ctx context.Context, t domain.Terminal) (bool, error) {
	v, err := s.Store.TerminalIdentities().Get(ctx, t.ID)
	if errs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if v.TemplateBootRef == "" || v.TemplateBootRef != t.LastBootReductionID || (v.TemplateFailedRef == v.TemplateBootRef && v.TemplateStrikes == 0) || t.LastBootAt == nil {
		return false, nil
	}
	if d := t.LastBootAt.Sub(v.TemplateBootAt); d > templateBootWindow || d < -templateBootWindow {
		return false, nil
	}
	if v.TemplateStrikes > 0 {
		v.TemplateFailedRef = ""
	}
	v.TemplateOKRef, v.TemplateStrikes, v.UpdatedAt = v.TemplateBootRef, 0, s.now()
	return true, s.Store.TerminalIdentities().Update(ctx, v)
}

// Rename 改机型名字。
func (s Service) Rename(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errs.Invalid("机型名称不能为空")
	}
	p, err := s.Store.HardwareProfiles().Get(ctx, id)
	if err != nil {
		return err
	}
	p.Name, p.UpdatedAt = name, s.now()
	return s.Store.HardwareProfiles().Update(ctx, p)
}

// InheritFrom 把 from 还原点的适配记录复制给派生出来的 to（超管保存、手动建点）：模板还在盘里，机型照样能开机。
func InheritFrom(ctx context.Context, st store.Store, fromReductionID string, to domain.Reduction, now time.Time) error {
	rows, err := st.ModelAdaptations().ByReduction(ctx, fromReductionID)
	if err != nil {
		return err
	}
	for i, row := range rows {
		if _, err := st.ModelAdaptations().ForProfileReduction(ctx, row.ProfileID, to.ID); err == nil {
			continue
		} else if !errs.IsNotFound(err) {
			return err
		}
		row.ID = fmt.Sprintf("ad-%d-%d", time.Now().UnixNano(), i)
		row.ReductionID, row.ConfigID, row.Source, row.CreatedAt = to.ID, to.ConfigID, domain.AdaptationInherited, now
		if err := st.ModelAdaptations().Create(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

// InheritLatest 让 to 从同配置里（除它自己）最新的就绪还原点继承；没有别的点时什么也不做。
func InheritLatest(ctx context.Context, st store.Store, to domain.Reduction, now time.Time) error {
	reductions, err := st.Reductions().ListByConfig(ctx, to.ConfigID)
	if err != nil {
		return err
	}
	var latest *domain.Reduction
	for i := range reductions {
		r := &reductions[i]
		if r.ID == to.ID || r.Status != domain.ReductionStatusReady {
			continue
		}
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) {
			latest = r
		}
	}
	if latest == nil {
		return nil
	}
	return InheritFrom(ctx, st, latest.ID, to, now)
}

func bootedAfter(t domain.Terminal) bool {
	return t.LastBootOKAt != nil && t.LastBootAt != nil && !t.LastBootOKAt.Before(*t.LastBootAt)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
