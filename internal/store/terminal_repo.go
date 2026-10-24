package store

import (
	"context"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

type terminalRepository struct {
	repository[domain.Terminal]
}

func (r terminalRepository) GetByMAC(ctx context.Context, mac string) (domain.Terminal, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE mac = "+r.placeholder(1),
		mac,
	))
	return scanOne(v, err, r.codec.table, mac)
}

// ByHardwareProfile 列出归到该机型的终端。
func (r terminalRepository) ByHardwareProfile(ctx context.Context, profileID string) ([]domain.Terminal, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE hardware_profile_id = "+r.placeholder(1)+" ORDER BY id",
		profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Terminal
	for rows.Next() {
		v, err := r.codec.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateBootIfUnchanged only advances the boot ledger; a newer boot, callback or hardware change wins.
func (r terminalRepository) UpdateBootIfUnchanged(ctx context.Context, before, after domain.Terminal) (bool, error) {
	args := []any{timePtrValue(after.LastBootAt), timePtrValue(after.LastBootOKAt), after.LastBootReductionID, after.LastBootProfileID, before.ID, before.MAC, before.HardwareProfileID, before.LastBootReductionID, before.LastBootProfileID}
	sets := []string{"last_boot_at", "last_boot_ok_at", "last_boot_reduction_id", "last_boot_profile_id"}
	for i := range sets {
		sets[i] += " = " + r.placeholder(i+1)
	}
	where := []string{"id", "mac", "hardware_profile_id", "last_boot_reduction_id", "last_boot_profile_id"}
	for i := range where {
		where[i] += " = " + r.placeholder(i+5)
	}
	for _, field := range []struct {
		name  string
		value any
	}{{"last_boot_at", timePtrValue(before.LastBootAt)}, {"last_boot_ok_at", timePtrValue(before.LastBootOKAt)}} {
		if field.value == nil {
			where = append(where, field.name+" IS NULL")
		} else {
			args = append(args, field.value)
			where = append(where, field.name+" = "+r.placeholder(len(args)))
		}
	}
	res, err := r.q.ExecContext(ctx, "UPDATE terminals SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Runtime writers own disjoint columns; stale presence/placement reads must not erase boot success.
func (r terminalRepository) updateRuntime(ctx context.Context, t domain.Terminal, columns []string, args []any) error {
	for i := range columns {
		columns[i] += " = " + r.placeholder(i+1)
	}
	args = append(args, t.ID, t.MAC)
	_, err := r.q.ExecContext(ctx, "UPDATE terminals SET "+strings.Join(columns, ", ")+" WHERE id = "+r.placeholder(len(args)-1)+" AND mac = "+r.placeholder(len(args)), args...)
	return err
}
func (r terminalRepository) UpdatePresence(ctx context.Context, t domain.Terminal) error {
	return r.updateRuntime(ctx, t, []string{"state", "online_since", "offline_at", "last_heartbeat_at"}, []any{string(t.State), timePtrValue(t.OnlineSince), timePtrValue(t.OfflineAt), timePtrValue(t.LastHeartbeatAt)})
}
func (r terminalRepository) UpdateHardwareProfile(ctx context.Context, t domain.Terminal) error {
	return r.updateRuntime(ctx, t, []string{"hardware_profile_id"}, []any{t.HardwareProfileID})
}
func (r terminalRepository) UpdatePlacement(ctx context.Context, t domain.Terminal) error {
	return r.updateRuntime(ctx, t, []string{"storage_server_id"}, []any{stringPtrValue(t.StorageServerID)})
}
