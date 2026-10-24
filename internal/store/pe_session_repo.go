package store

import (
	"context"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

type peSessionRepository struct {
	repository[domain.PESession]
}

var peSessionCodec = entityCodec[domain.PESession]{
	table: "pe_sessions",
	columns: []string{"id", "terminal_id", "mac", "mode", "task_id", "token", "env_id", "bcd_id", "bundle_id", "target_iqn", "lun",
		"config_id", "reduction_id", "reduction_name", "reduction_display_name", "target_portal", "driver_pack_ids", "state", "result", "created_at", "updated_at", "expires_at"},
	values: func(v domain.PESession) []any {
		return []any{v.ID, v.TerminalID, v.MAC, string(v.Mode), v.TaskID, v.Token, v.EnvID, v.BCDID, "", v.TargetIQN, v.LUN,
			v.ConfigID, v.ReductionID, v.ReductionName, v.ReductionDisplayName, v.TargetPortal, strings.Join(v.DriverPackIDs, ","), string(v.State), v.Result, timeValue(v.CreatedAt), timeValue(v.UpdatedAt), timeValue(v.ExpiresAt)}
	},
	scan: func(s rowScanner) (domain.PESession, error) {
		var v domain.PESession
		var mode, state, created, updated, expires, packs, retiredBundle string
		if err := s.Scan(&v.ID, &v.TerminalID, &v.MAC, &mode, &v.TaskID, &v.Token, &v.EnvID, &v.BCDID, &retiredBundle, &v.TargetIQN, &v.LUN,
			&v.ConfigID, &v.ReductionID, &v.ReductionName, &v.ReductionDisplayName, &v.TargetPortal, &packs, &state, &v.Result, &created, &updated, &expires); err != nil {
			return v, err
		}
		if packs != "" {
			v.DriverPackIDs = strings.Split(packs, ",")
		}
		v.Mode, v.State = domain.PEMode(mode), domain.PESessionState(state)
		var err error
		if v.CreatedAt, err = parseTime(created); err != nil {
			return v, err
		}
		if v.UpdatedAt, err = parseTime(updated); err != nil {
			return v, err
		}
		v.ExpiresAt, err = parseTime(expires)
		return v, err
	},
}

const activeStates = "('preparing','pending','booted','finalizing')"

// ActiveByTerminal 返回终端当前进行中的会话（等开机或已进 WinPE），没有为未找到。
func (r peSessionRepository) ActiveByTerminal(ctx context.Context, terminalID string) (domain.PESession, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE terminal_id = "+r.placeholder(1)+
			" AND state IN "+activeStates+" ORDER BY created_at DESC LIMIT 1", terminalID))
	return scanOne(v, err, r.codec.table, terminalID)
}

func (r peSessionRepository) ByToken(ctx context.Context, token string) (domain.PESession, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE token = "+r.placeholder(1), token))
	return scanOne(v, err, r.codec.table, token)
}

func (r peSessionRepository) ByTask(ctx context.Context, taskID string) (domain.PESession, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE task_id = "+r.placeholder(1)+" ORDER BY created_at DESC LIMIT 1", taskID))
	return scanOne(v, err, r.codec.table, taskID)
}

func (r peSessionRepository) ListActive(ctx context.Context) ([]domain.PESession, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE state IN "+activeStates+" ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PESession
	for rows.Next() {
		v, err := r.codec.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
