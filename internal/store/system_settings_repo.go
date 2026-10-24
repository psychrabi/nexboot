package store

import (
	"context"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

type systemSettingsRepository struct {
	repository[domain.SystemSettings]
}

func (r systemSettingsRepository) Upsert(ctx context.Context, value domain.SystemSettings) error {
	// 列名取自 codec，新增列不会被这里漏掉（写死列名时新列会被静默丢弃）。
	cols := r.codec.columns
	sets := make([]string, 0, len(cols)-1)
	for _, c := range cols[1:] {
		sets = append(sets, c+" = excluded."+c)
	}
	_, err := r.q.ExecContext(ctx,
		"INSERT INTO system_settings ("+strings.Join(cols, ", ")+") VALUES ("+r.placeholders(1, len(cols))+")"+
			" ON CONFLICT(id) DO UPDATE SET "+strings.Join(sets, ", "),
		r.codec.values(value)...,
	)
	return err
}
