package store

import (
	"context"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

type bootFailureRepository struct {
	repository[domain.BootFailure]
}

var bootFailureCodec = entityCodec[domain.BootFailure]{
	table:   "boot_failures",
	columns: []string{"mac", "stage", "code", "platform", "image_id", "at", "reduction_id", "profile_id"},
	values: func(v domain.BootFailure) []any {
		return []any{v.MAC, v.Stage, v.Code, v.Platform, v.ImageID, timeValue(v.At), v.ReductionID, v.ProfileID}
	},
	scan: func(s rowScanner) (domain.BootFailure, error) {
		var v domain.BootFailure
		var at string
		if err := s.Scan(&v.MAC, &v.Stage, &v.Code, &v.Platform, &v.ImageID, &at, &v.ReductionID, &v.ProfileID); err != nil {
			return v, err
		}
		t, err := parseTime(at)
		v.At = t
		return v, err
	},
}

func (r bootFailureRepository) List(ctx context.Context) ([]domain.BootFailure, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT "+joinColumns(r.codec.columns)+" FROM boot_failures ORDER BY mac")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BootFailure
	for rows.Next() {
		v, err := r.codec.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r bootFailureRepository) Upsert(ctx context.Context, v domain.BootFailure) error {
	_, err := r.q.ExecContext(ctx,
		"INSERT INTO boot_failures ("+joinColumns(r.codec.columns)+") VALUES ("+r.placeholders(1, len(r.codec.columns))+")"+
			" ON CONFLICT(mac) DO UPDATE SET stage = excluded.stage, code = excluded.code, platform = excluded.platform,"+
			" image_id = excluded.image_id, at = excluded.at, reduction_id = excluded.reduction_id, profile_id = excluded.profile_id",
		r.codec.values(v)...,
	)
	return err
}

// DeleteIfUnchanged must not erase a newer failure that replaced the row after alarm evaluation read it.
func (r bootFailureRepository) DeleteIfUnchanged(ctx context.Context, v domain.BootFailure) (bool, error) {
	conditions := make([]string, len(r.codec.columns))
	for i, column := range r.codec.columns {
		conditions[i] = column + " = " + r.placeholder(i+1)
	}
	result, err := r.q.ExecContext(ctx, "DELETE FROM boot_failures WHERE "+strings.Join(conditions, " AND "), r.codec.values(v)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
