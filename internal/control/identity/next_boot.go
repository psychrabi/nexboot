package identity

import (
	"context"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

// NextBoot describes the current boot decision, without starting a task or touching storage.
// It is an expectation from the current configuration, not a guarantee that hardware will boot.
type NextBoot struct {
	Kind        string                `json:"kind"`
	Reason      string                `json:"reason"`
	Mode        domain.PEMode         `json:"mode,omitempty"`
	State       domain.PESessionState `json:"state,omitempty"`
	TaskID      string                `json:"task_id,omitempty"`
	ExpiresAt   *time.Time            `json:"expires_at,omitempty"`
	ImageID     string                `json:"image_id,omitempty"`
	ConfigID    string                `json:"config_id,omitempty"`
	ReductionID string                `json:"reduction_id,omitempty"`
	InstallID   string                `json:"install_id,omitempty"`
}

// planIdentity is shared by the read-only preview and the actual boot preparation.
func (s *Service) planIdentity(ctx context.Context, t domain.Terminal, a domain.ModelAdaptation, v *domain.TerminalIdentity) (string, string, error) {
	if !hasTemplate(a) {
		return "no_template", "", nil
	}
	p, err := s.Store.HardwareProfiles().Get(ctx, t.HardwareProfileID)
	if err != nil {
		return "", "", err
	}
	if v != nil && v.ProfileID == t.HardwareProfileID && v.Source == "model" && !strings.Contains(strings.ToLower(p.Manufacturer), "vmware") {
		return "capture", "", nil
	}
	if v != nil && v.ProfileID == t.HardwareProfileID && v.Source == "model" {
		state := s.bootState(t, *v, a.ReductionID, true)
		if state.TemplateFailedRef == a.ReductionID {
			return "capture", "", nil
		}
	}
	if v != nil && v.ProfileID == t.HardwareProfileID {
		stale, err := s.computedStale(ctx, t, *v)
		if err != nil {
			return "", "", err
		}
		if !stale {
			return "identity_layer", "", nil
		}
	}
	if instance, ok := ComputedInstance(p, t.MAC, a.TemplateInstanceID); ok {
		return "compute", instance, nil
	}
	if instance, err := s.ownHistoricalInstance(ctx, t, p); err != nil {
		return "", "", err
	} else if instance != "" {
		return "history", instance, nil
	}
	instance, err := s.modelInstance(ctx, t, p, a.TemplateInstanceID)
	if err != nil {
		return "", "", err
	}
	if instance != "" {
		return "reuse", instance, nil
	}
	return "capture", "", nil
}

func hasTemplate(a domain.ModelAdaptation) bool {
	return a.TemplateInstanceID != "" && a.ClassKey != "" && a.NetCfgInstanceID != ""
}

func (s *Service) nextBoot(ctx context.Context, t domain.Terminal, identity *domain.TerminalIdentity) (NextBoot, error) {
	now := time.Now()
	if s.PE != nil && s.PE.Now != nil {
		now = s.PE.Now()
	}
	// 与 /boot 同序：进行中的 ISO 安装先于维护环境和分组系统盘。
	if sess, err := s.Store.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(t.MAC)); err == nil {
		return NextBoot{Kind: "install", Reason: sess.Phase, TaskID: sess.TaskID, InstallID: sess.ID}, nil
	} else if !errs.IsNotFound(err) {
		return NextBoot{}, err
	}
	if s.PE != nil {
		sess, err := s.Store.PESessions().ActiveByTerminal(ctx, t.ID)
		if err != nil && !errs.IsNotFound(err) {
			return NextBoot{}, err
		}
		if err == nil {
			v := NextBoot{Kind: "winpe", Reason: "pending_" + string(sess.Mode), Mode: sess.Mode, State: sess.State, TaskID: sess.TaskID, ConfigID: sess.ConfigID, ReductionID: sess.ReductionID}
			if sess.ConfigID != "" {
				cfg, err := s.Store.Configs().Get(ctx, sess.ConfigID)
				if err == nil {
					v.ImageID = cfg.ImageID
				} else if !errs.IsNotFound(err) {
					return NextBoot{}, err
				}
			}
			// Actual /boot refuses these states before checking expiration.
			if sess.State == domain.PEStatePreparing || sess.State == domain.PEStateFinalizing {
				v.Kind, v.Reason = "wait", string(sess.State)
				return v, nil
			}
			if sess.ExpiresAt.IsZero() || !now.After(sess.ExpiresAt) {
				if !sess.ExpiresAt.IsZero() {
					v.ExpiresAt = &sess.ExpiresAt
				}
				return v, nil
			}
		}
	}
	v := NextBoot{Kind: "unknown", Reason: "unconfigured"}
	g, err := s.Store.Groups().Get(ctx, t.GroupID)
	if errs.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	img, err := s.Store.Images().Get(ctx, g.SystemImageID)
	if errs.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	cfg, err := s.Store.Configs().Get(ctx, g.SystemConfigID)
	if errs.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.ImageID, v.ConfigID, v.ReductionID = img.ID, cfg.ID, g.SystemReductionID
	if cfg.DefaultReductionID != nil && *cfg.DefaultReductionID != "" {
		v.ReductionID = *cfg.DefaultReductionID
	}
	if t.IsSuper {
		reds, err := s.Store.Reductions().List(ctx)
		if err != nil {
			return v, err
		}
		v.ReductionID = ""
		var latest time.Time
		for _, r := range reds {
			if r.ConfigID == cfg.ID && r.Status == domain.ReductionStatusReady && (v.ReductionID == "" || r.CreatedAt.After(latest)) {
				v.ReductionID, latest = r.ID, r.CreatedAt
			}
		}
	}
	if v.ReductionID == "" {
		return v, nil
	}
	if _, err := s.Store.Reductions().Get(ctx, v.ReductionID); errs.IsNotFound(err) {
		return v, nil
	} else if err != nil {
		return v, err
	}
	v.Kind, v.Reason = "system", "direct"
	if t.IsSuper {
		v.Reason = "super"
		return v, nil
	}
	if img.OSType != domain.OSTypeWindows {
		v.Reason = "linux"
		return v, nil
	}
	if t.HardwareProfileID == "" {
		v.Reason = "unverified"
		return v, nil
	}
	a, err := s.Store.ModelAdaptations().ForProfileReduction(ctx, t.HardwareProfileID, v.ReductionID)
	if errs.IsNotFound(err) {
		v.Reason = "unverified"
		if identity != nil {
			state := s.bootState(t, *identity, v.ReductionID, true)
			identity = &state
		}
		plan, err := s.planTemplate(ctx, t, v.ReductionID, identity)
		if err != nil {
			return v, err
		}
		switch plan.Kind {
		case "blocked":
			v.Kind, v.Reason = "unknown", "unverified"
		case "template":
			v.Reason = "automatic_template"
		case "capture":
			v.Kind, v.Reason, v.Mode = "winpe", "automatic_identity", domain.PEModeIdentity
		}
		return v, nil
	}
	if err != nil {
		return v, err
	}
	decision, _, err := s.planIdentity(ctx, t, a, identity)
	if err != nil {
		return v, err
	}
	switch decision {
	case "capture":
		v.Kind, v.Reason, v.Mode = "winpe", "automatic_identity", domain.PEModeIdentity
	case "compute", "reuse", "history":
		v.Reason = "automatic_config"
	case "identity_layer":
		node := s.NodeID
		if t.StorageServerID != nil {
			node = *t.StorageServerID
		}
		if identity.LayerReductionRef != v.ReductionID || identity.LayerServerID != node {
			v.Reason = "identity_rebuild"
		}
	}
	return v, nil
}
