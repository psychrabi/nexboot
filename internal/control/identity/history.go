package identity

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// ServerIdentity is an association made by the server, separate from the original PE report data.
type captureHistory struct {
	ProfileID   string `json:"profile_id"`
	Fingerprint string `json:"fingerprint"`
	BusLoc      string `json:"bus_loc"`
	Platform    string `json:"platform"`
	InstanceID  string `json:"instance_id"`
}

type captureReport struct {
	pe.PEResult
	ServerIdentity *captureHistory `json:"server_identity,omitempty"`
}

func historyProfile(p domain.HardwareProfile, instance string) captureHistory {
	return captureHistory{p.ID, p.Fingerprint, p.BusLoc, p.Platform, instance}
}

func matchesProfile(instance string, p domain.HardwareProfile) bool {
	parts := strings.Split(strings.ToUpper(instance), `\`)
	if len(parts) != 3 || parts[0] != "PCI" || parts[2] == "" {
		return false
	}
	fields := strings.Split(parts[1], "&")
	if len(fields) < 2 || fields[0] != "VEN_"+strings.ToUpper(p.NICVendor) || fields[1] != "DEV_"+strings.ToUpper(p.NICDevice) {
		return false
	}
	if p.NICSubsys != "" {
		for _, f := range fields[2:] {
			if f == "SUBSYS_"+strings.ToUpper(p.NICSubsys) {
				return true
			}
		}
		return false
	}
	return true
}

func successfulReport(sess domain.PESession, terminal domain.Terminal, mode domain.PEMode) (captureReport, bool) {
	var result captureReport
	if sess.TerminalID != terminal.ID || storage.NormalizeMAC(sess.MAC) != storage.NormalizeMAC(terminal.MAC) || sess.Mode != mode || sess.State != domain.PEStateDone || json.Unmarshal([]byte(sess.Result), &result) != nil || !result.OK || result.Stage != string(mode) {
		return result, false
	}
	return result, true
}

// Archive before replacing the only current row; both writes use the caller's transaction.
func archiveCaptured(ctx context.Context, st store.Store, old domain.TerminalIdentity) error {
	if old.Source != "captured" {
		return nil
	}
	p, err := st.HardwareProfiles().Get(ctx, old.ProfileID)
	if errs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	terminal, err := st.Terminals().Get(ctx, old.TerminalID)
	if err != nil {
		return err
	}
	sessions, err := st.PESessions().List(ctx)
	if err != nil {
		return err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	for _, sess := range sessions {
		result, ok := successfulReport(sess, terminal, domain.PEModeIdentity)
		if !ok || sess.UpdatedAt.After(old.UpdatedAt) {
			continue
		}
		var nic pe.NICIdentity
		if json.Unmarshal(result.Data, &nic) != nil || !strings.EqualFold(nic.InstanceID, old.InstanceID) || !matchesProfile(nic.InstanceID, p) {
			continue
		}
		note := historyProfile(p, nic.InstanceID)
		if result.ServerIdentity != nil {
			if *result.ServerIdentity == note {
				return nil
			}
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(sess.Result), &envelope); err != nil {
			return err
		}
		envelope["server_identity"], err = json.Marshal(note)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		sess.Result = string(raw)
		return st.PESessions().Update(ctx, sess)
	}
	return nil
}

func (s *Service) ownHistoricalInstance(ctx context.Context, t domain.Terminal, p domain.HardwareProfile) (string, error) {
	// A physical machine's old report without a PCI position cannot prove the NIC is still in that slot.
	if !strings.Contains(strings.ToLower(p.Manufacturer), "vmware") && p.BusLoc == "" {
		return "", nil
	}
	adaptations, err := s.Store.ModelAdaptations().ByProfile(ctx, p.ID)
	if err != nil {
		return "", err
	}
	sort.Slice(adaptations, func(i, j int) bool { return adaptations[i].CreatedAt.After(adaptations[j].CreatedAt) })
	for _, a := range adaptations {
		if a.TerminalID == t.ID && a.ProfileID == p.ID && a.Source == domain.AdaptationAdapted && hasTemplate(a) && matchesProfile(a.TemplateInstanceID, p) {
			sess, err := s.Store.PESessions().ByTask(ctx, a.TaskID)
			if errs.IsNotFound(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			result, ok := successfulReport(sess, t, domain.PEModeAdapt)
			var nic pe.NICIdentity
			if ok && json.Unmarshal(result.Data, &nic) == nil && strings.EqualFold(nic.InstanceID, a.TemplateInstanceID) {
				return a.TemplateInstanceID, nil
			}
		}
	}
	sessions, err := s.Store.PESessions().List(ctx)
	if err != nil {
		return "", err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	for _, sess := range sessions {
		result, ok := successfulReport(sess, t, domain.PEModeIdentity)
		if !ok || result.ServerIdentity == nil {
			continue
		}
		var nic pe.NICIdentity
		if json.Unmarshal(result.Data, &nic) != nil || !matchesProfile(nic.InstanceID, p) {
			continue
		}
		if *result.ServerIdentity == historyProfile(p, nic.InstanceID) {
			return nic.InstanceID, nil
		}
	}
	return "", nil
}
