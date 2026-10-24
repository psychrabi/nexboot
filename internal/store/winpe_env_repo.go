package store

import (
	"context"
	"encoding/json"

	"github.com/tianwei/diskless/internal/domain"
)

type winpeEnvRepository struct {
	repository[domain.WinPEEnv]
}

var winpeEnvCodec = entityCodec[domain.WinPEEnv]{
	table:   "winpe_envs",
	columns: []string{"id", "kind", "image_id", "version", "build", "dir", "files", "created_at"},
	values: func(v domain.WinPEEnv) []any {
		files, _ := json.Marshal(v.Files)
		if v.Files == nil {
			files = []byte("{}")
		}
		return []any{v.ID, string(v.Kind), v.ImageID, v.Version, v.Build, v.Dir, string(files), timeValue(v.CreatedAt)}
	},
	scan: func(s rowScanner) (domain.WinPEEnv, error) {
		var v domain.WinPEEnv
		var kind, files, created string
		if err := s.Scan(&v.ID, &kind, &v.ImageID, &v.Version, &v.Build, &v.Dir, &files, &created); err != nil {
			return v, err
		}
		v.Kind = domain.WinPEEnvKind(kind)
		if err := json.Unmarshal([]byte(files), &v.Files); err != nil {
			return v, err
		}
		t, err := parseTime(created)
		v.CreatedAt = t
		return v, err
	},
}

// ByImage 返回从该镜像提取的 WinRE 记录，没有时为未找到。
func (r winpeEnvRepository) ByImage(ctx context.Context, imageID string) (domain.WinPEEnv, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE kind = "+r.placeholder(1)+" AND image_id = "+r.placeholder(2),
		string(domain.WinPEEnvImageWinRE), imageID,
	))
	return scanOne(v, err, r.codec.table, imageID)
}

func (r winpeEnvRepository) ByKind(ctx context.Context, kind domain.WinPEEnvKind) ([]domain.WinPEEnv, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE kind = "+r.placeholder(1)+" ORDER BY created_at DESC, id",
		string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WinPEEnv
	for rows.Next() {
		v, err := r.codec.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
