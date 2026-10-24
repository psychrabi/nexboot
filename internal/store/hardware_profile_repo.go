package store

import (
	"context"
	"strings"

	"github.com/tianwei/diskless/internal/domain"
)

type hardwareProfileRepository struct {
	repository[domain.HardwareProfile]
}

var hardwareProfileCodec = entityCodec[domain.HardwareProfile]{
	table:   "hardware_profiles",
	columns: []string{"id", "name", "fingerprint", "nic_vendor", "nic_device", "nic_subsys", "bus_loc", "platform", "manufacturer", "product", "created_at", "updated_at"},
	values: func(v domain.HardwareProfile) []any {
		return []any{v.ID, v.Name, v.Fingerprint, v.NICVendor, v.NICDevice, v.NICSubsys, v.BusLoc, v.Platform, v.Manufacturer, v.Product, timeValue(v.CreatedAt), timeValue(v.UpdatedAt)}
	},
	scan: func(s rowScanner) (domain.HardwareProfile, error) {
		var v domain.HardwareProfile
		var created, updated string
		if err := s.Scan(&v.ID, &v.Name, &v.Fingerprint, &v.NICVendor, &v.NICDevice, &v.NICSubsys, &v.BusLoc, &v.Platform, &v.Manufacturer, &v.Product, &created, &updated); err != nil {
			return v, err
		}
		var err error
		if v.CreatedAt, err = parseTime(created); err != nil {
			return v, err
		}
		v.UpdatedAt, err = parseTime(updated)
		return v, err
	},
}

// ByFingerprint 按归并键找机型，没有时为未找到。
func (r hardwareProfileRepository) ByFingerprint(ctx context.Context, fingerprint string) (domain.HardwareProfile, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE fingerprint = "+r.placeholder(1),
		fingerprint,
	))
	return scanOne(v, err, r.codec.table, fingerprint)
}

type modelAdaptationRepository struct {
	repository[domain.ModelAdaptation]
}

var modelAdaptationCodec = entityCodec[domain.ModelAdaptation]{
	table:   "model_adaptations",
	columns: []string{"id", "profile_id", "reduction_id", "config_id", "source", "template_instance_id", "class_key", "netcfg_instance_id", "task_id", "terminal_id", "driver_pack_ids", "created_at"},
	values: func(v domain.ModelAdaptation) []any {
		return []any{v.ID, v.ProfileID, v.ReductionID, v.ConfigID, string(v.Source), v.TemplateInstanceID, v.ClassKey, v.NetCfgInstanceID, v.TaskID, v.TerminalID, strings.Join(v.DriverPackIDs, ","), timeValue(v.CreatedAt)}
	},
	scan: func(s rowScanner) (domain.ModelAdaptation, error) {
		var v domain.ModelAdaptation
		var source, packs, created string
		if err := s.Scan(&v.ID, &v.ProfileID, &v.ReductionID, &v.ConfigID, &source, &v.TemplateInstanceID, &v.ClassKey, &v.NetCfgInstanceID, &v.TaskID, &v.TerminalID, &packs, &created); err != nil {
			return v, err
		}
		v.Source = domain.ModelAdaptationSource(source)
		if packs != "" {
			v.DriverPackIDs = strings.Split(packs, ",")
		}
		t, err := parseTime(created)
		v.CreatedAt = t
		return v, err
	},
}

func (r modelAdaptationRepository) ForProfileReduction(ctx context.Context, profileID, reductionID string) (domain.ModelAdaptation, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE profile_id = "+r.placeholder(1)+" AND reduction_id = "+r.placeholder(2),
		profileID, reductionID,
	))
	return scanOne(v, err, r.codec.table, profileID+"/"+reductionID)
}

func (r modelAdaptationRepository) ByReduction(ctx context.Context, reductionID string) ([]domain.ModelAdaptation, error) {
	return r.where(ctx, "reduction_id", reductionID)
}

func (r modelAdaptationRepository) ByProfile(ctx context.Context, profileID string) ([]domain.ModelAdaptation, error) {
	return r.where(ctx, "profile_id", profileID)
}

func (r modelAdaptationRepository) where(ctx context.Context, column, value string) ([]domain.ModelAdaptation, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+joinColumns(r.codec.columns)+" FROM "+r.codec.table+" WHERE "+column+" = "+r.placeholder(1)+" ORDER BY created_at, id",
		value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ModelAdaptation
	for rows.Next() {
		v, err := r.codec.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r modelAdaptationRepository) DeleteByReduction(ctx context.Context, reductionID string) error {
	_, err := r.q.ExecContext(ctx, "DELETE FROM "+r.codec.table+" WHERE reduction_id = "+r.placeholder(1), reductionID)
	return err
}

func (r modelAdaptationRepository) DeleteByConfig(ctx context.Context, configID string) error {
	_, err := r.q.ExecContext(ctx, "DELETE FROM "+r.codec.table+" WHERE config_id = "+r.placeholder(1), configID)
	return err
}
