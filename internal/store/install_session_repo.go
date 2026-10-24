package store

import (
	"context"
	"encoding/json"

	"github.com/tianwei/diskless/internal/domain"
)

type InstallSessionRepo interface {
	Repository[domain.InstallSession]
	ActiveByMAC(context.Context, string) (domain.InstallSession, error)
	ByToken(context.Context, string) (domain.InstallSession, error)
}

type installSessionRepository struct {
	repository[domain.InstallSession]
}

var installSessionCodec = entityCodec[domain.InstallSession]{table: "install_sessions", columns: []string{"id", "mac", "token", "phase", "payload"},
	values: func(v domain.InstallSession) []any {
		payload, _ := json.Marshal(v)
		return []any{v.ID, v.MAC, v.Token, v.Phase, string(payload)}
	},
	scan: func(s rowScanner) (domain.InstallSession, error) {
		var v domain.InstallSession
		var id, mac, token, phase, payload string
		err := s.Scan(&id, &mac, &token, &phase, &payload)
		if err != nil {
			return v, err
		}
		err = json.Unmarshal([]byte(payload), &v)
		return v, err
	},
}

func (s *SQLStore) InstallSessions() InstallSessionRepo {
	return installSessionRepository{newRepository(s.q, s.dialect, installSessionCodec)}
}
func (r installSessionRepository) ActiveByMAC(ctx context.Context, mac string) (domain.InstallSession, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx, "SELECT "+joinColumns(r.codec.columns)+" FROM install_sessions WHERE mac = "+r.placeholder(1)+" AND phase NOT IN ('ready','cancelled') LIMIT 1", mac))
	return scanOne(v, err, "install_sessions", mac)
}
func (r installSessionRepository) ByToken(ctx context.Context, token string) (domain.InstallSession, error) {
	v, err := r.codec.scan(r.q.QueryRowContext(ctx, "SELECT "+joinColumns(r.codec.columns)+" FROM install_sessions WHERE token = "+r.placeholder(1), token))
	return scanOne(v, err, "install_sessions", token)
}
