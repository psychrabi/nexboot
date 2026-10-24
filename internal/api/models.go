package api

import (
	"context"

	"github.com/tianwei/diskless/internal/control/hwprofile"
)

// ModelService 是机型页与各处「开机失败」提示的数据来源。
type ModelService interface {
	List(context.Context) ([]hwprofile.View, error)
	Get(context.Context, string) (hwprofile.View, error)
	Rename(ctx context.Context, id string, req RenameModelRequest) error
	FailingByGroup(context.Context) (map[string][]hwprofile.Failing, error)
	ForTerminalID(context.Context, string) (hwprofile.TerminalStatus, error)
}

// RenameModelRequest 是改机型名字的请求体。
type RenameModelRequest struct {
	Name string `json:"name"`
}

// ModelAPI 把 hwprofile.Service 适配成路由需要的签名。
type ModelAPI struct {
	hwprofile.Service
}

func (m ModelAPI) Rename(ctx context.Context, id string, req RenameModelRequest) error {
	return m.Service.Rename(ctx, id, req.Name)
}

func (m ModelAPI) ForTerminalID(ctx context.Context, id string) (hwprofile.TerminalStatus, error) {
	t, err := m.Store.Terminals().Get(ctx, id)
	if err != nil {
		return hwprofile.TerminalStatus{}, err
	}
	return m.Service.ForTerminal(ctx, t)
}

// ForReduction 返回该还原点上有记录的机型及记录来源，供还原点详情显示；记录本身不代表开过机，不给状态。
func (m ModelAPI) ForReduction(ctx context.Context, id string) ([]hwprofile.View, error) {
	if _, err := m.Store.Reductions().Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := m.Store.ModelAdaptations().ByReduction(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]hwprofile.View, 0, len(rows))
	for _, row := range rows {
		p, err := m.Store.HardwareProfiles().Get(ctx, row.ProfileID)
		if err != nil {
			return nil, err
		}
		out = append(out, hwprofile.View{ID: p.ID, Name: hwprofile.DisplayName(p), NICName: hwprofile.NICName(p.NICVendor, p.NICDevice), NICLabel: hwprofile.NICLabel(p), Source: row.Source})
	}
	return out, nil
}
