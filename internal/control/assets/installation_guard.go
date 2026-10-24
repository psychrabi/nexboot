package assets

import (
	"context"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

func ensureNoInstallation(ctx context.Context, st store.Store, mac string) error {
	if _, err := st.InstallSessions().ActiveByMAC(ctx, storage.NormalizeMAC(mac)); err == nil {
		return errs.Conflict("客户机「" + mac + "」正在从 ISO 创建镜像，请先完成或取消安装任务")
	} else if !errs.IsNotFound(err) {
		return err
	}
	return nil
}
