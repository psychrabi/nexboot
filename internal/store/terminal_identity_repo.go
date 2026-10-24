package store

import "github.com/tianwei/diskless/internal/domain"

var terminalIdentityCodec = entityCodec[domain.TerminalIdentity]{
	table:   "terminal_identities",
	columns: []string{"id", "terminal_id", "profile_id", "instance_id", "source", "layer_reduction_ref", "layer_server_id", "template_boot_ref", "template_boot_at", "template_ok_ref", "template_failed_ref", "template_strikes", "template_gen", "updated_at"},
	values: func(v domain.TerminalIdentity) []any {
		return []any{v.ID, v.TerminalID, v.ProfileID, v.InstanceID, v.Source, v.LayerReductionRef, v.LayerServerID, v.TemplateBootRef, timeValue(v.TemplateBootAt), v.TemplateOKRef, v.TemplateFailedRef, v.TemplateStrikes, v.TemplateGen, timeValue(v.UpdatedAt)}
	},
	scan: func(s rowScanner) (domain.TerminalIdentity, error) {
		var v domain.TerminalIdentity
		var updated, bootAt string
		err := s.Scan(&v.ID, &v.TerminalID, &v.ProfileID, &v.InstanceID, &v.Source, &v.LayerReductionRef, &v.LayerServerID, &v.TemplateBootRef, &bootAt, &v.TemplateOKRef, &v.TemplateFailedRef, &v.TemplateStrikes, &v.TemplateGen, &updated)
		if err != nil {
			return v, err
		}
		if v.TemplateBootAt, err = parseTime(bootAt); err != nil {
			return v, err
		}
		v.UpdatedAt, err = parseTime(updated)
		return v, err
	},
}
