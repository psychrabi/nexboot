package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tianwei/diskless/internal/control/install"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
)

type InstallationService interface {
	Inspect(context.Context, install.InspectRequest) (install.Metadata, error)
	Sources(context.Context) (install.Sources, error)
	Start(context.Context, install.StartRequest) (install.StartResult, error)
	List(context.Context) (install.ListResult, error)
	Get(context.Context, string) (install.View, error)
	Cancel(context.Context, string) (install.View, error)
	Retry(context.Context, string) (install.View, error)
	SessionByToken(context.Context, string) (domain.InstallSession, error)
	File(context.Context, string, string) (pe.PEFile, error)
	Report(context.Context, string, install.Report) error
	SaveLog(context.Context, string, string, io.Reader) error
	TaskLog(context.Context, string, string) (pe.PEFile, error)
}

func installSession(w http.ResponseWriter, r *http.Request, svc InstallationService, off bool, logger *slog.Logger) (string, bool) {
	token := chi.URLParam(r, "token")
	sess, err := svc.SessionByToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return "", false
	}
	if !off {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if sess.IP == "" || !net.ParseIP(host).Equal(net.ParseIP(sess.IP)) {
			logger.Warn("refused installation request from another client", "session", sess.ID, "from", host)
			http.NotFound(w, r)
			return "", false
		}
	}
	return token, true
}
func mountInstallationPublic(r chi.Router, svc InstallationService, off bool, logger *slog.Logger) {
	r.Get("/install/{token}/*", func(w http.ResponseWriter, r *http.Request) {
		token, ok := installSession(w, r, svc, off, logger)
		if !ok {
			return
		}
		file, err := svc.File(r.Context(), token, chi.URLParam(r, "*"))
		if writeError(w, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/octet-stream")
		if file.Path != "" {
			http.ServeFile(w, r, file.Path)
			return
		}
		_, _ = w.Write(file.Content)
	})
	r.Post("/install/{token}/report", func(w http.ResponseWriter, r *http.Request) {
		token, ok := installSession(w, r, svc, off, logger)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var report install.Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, "安装回报格式无效", http.StatusBadRequest)
			return
		}
		if writeError(w, svc.Report(r.Context(), token, report)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Post("/install/{token}/log/{name}", func(w http.ResponseWriter, r *http.Request) {
		token, ok := installSession(w, r, svc, off, logger)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20+1)
		if writeError(w, svc.SaveLog(r.Context(), token, chi.URLParam(r, "name"), r.Body)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
func mountInstallationAdmin(r chi.Router, svc InstallationService) {
	r.Get("/api/installations", handle(svc.List))
	r.Get("/api/installations/sources", handle(svc.Sources))
	r.Post("/api/installations/inspect", handleIn(svc.Inspect))
	r.Post("/api/installations", handleIn(svc.Start, status(http.StatusAccepted)))
	r.Get("/api/installations/{id}", handleID(svc.Get))
	r.Post("/api/installations/{id}/cancel", handleID(svc.Cancel))
	r.Post("/api/installations/{id}/retry", handleID(svc.Retry))
	r.Get("/api/installations/{id}/logs/{name}", func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if writeError(w, err) {
			return
		}
		file, err := svc.TaskLog(r.Context(), v.TaskID, chi.URLParam(r, "name"))
		if writeError(w, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/octet-stream")
		if file.Path != "" {
			http.ServeFile(w, r, file.Path)
			return
		}
		_, _ = w.Write(file.Content)
	})
}
