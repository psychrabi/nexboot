package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"

	"github.com/tianwei/diskless/internal/control/pe"
)

func taskLogHandler(read func(context.Context, string, string) (pe.PEFile, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := read(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "name"))
		if writeError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+path.Base(f.Name)+"\"")
		if f.Path != "" {
			http.ServeFile(w, r, f.Path)
			return
		}
		_, _ = w.Write(f.Content)
	}
}

const (
	maxBCDUploadBytes = 16 << 20 // BCD 只有几十 KB，留足余量即可
	maxADKUploadBytes = 3 << 30  // 带驱动的 WinPE 包可到 1～2 GB
)

// uploadBCDHandler 收 multipart 里的 file 字段，整体读进内存后交给服务校验。
func uploadBCDHandler(service WinPEService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBCDUploadBytes)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "上传表单无效", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "缺少文件字段 file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "读取上传内容失败", http.StatusBadRequest)
			return
		}
		env, err := service.UploadBCD(r.Context(), data)
		if writeError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, env)
	}
}

// uploadADKHandler 流式读取 multipart 里的 file 字段：包有几百 MB 到 1～2 GB，不经 ParseMultipartForm 落两次盘。
func uploadADKHandler(service WinPEService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxADKUploadBytes)
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "上传表单无效", http.StatusBadRequest)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "上传表单无效", http.StatusBadRequest)
				return
			}
			if part.FormName() != "file" {
				_ = part.Close()
				continue
			}
			env, err := service.UploadADK(r.Context(), part)
			_ = part.Close()
			if writeError(w, err) {
				return
			}
			writeJSON(w, http.StatusCreated, env)
			return
		}
		http.Error(w, "缺少文件字段 file", http.StatusBadRequest)
	}
}

const (
	maxPEResultBytes = 64 << 10
	maxPELogBytes    = 16<<20 + 1
)

// peSession 解析令牌并核对来源：会话不存在、已结束或来源不是该客户机登记的地址，一律按「没有」作答。
func peSession(w http.ResponseWriter, r *http.Request, service PESessionService, peerCheckOff bool, logger *slog.Logger) (string, bool) {
	token := chi.URLParam(r, "token")
	_, terminal, err := service.SessionByToken(r.Context(), token)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return "", false
	}
	if !peerCheckOff && terminal.IP != "" {
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			host = r.RemoteAddr
		}
		if !net.ParseIP(host).Equal(net.ParseIP(terminal.IP)) {
			logger.Warn("refused a WinPE request from an address other than the client's", "terminal", terminal.ID, "from", host, "path", redactSessionPath(r.URL.Path))
			http.Error(w, "not found", http.StatusNotFound)
			return "", false
		}
	}
	return token, true
}

func peFileHandler(service PESessionService, peerCheckOff bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := peSession(w, r, service, peerCheckOff, logger)
		if !ok {
			return
		}
		f, err := service.File(r.Context(), token, chi.URLParam(r, "*"), r.URL.Query().Get("platform"))
		if writeError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		if f.Path != "" {
			http.ServeFile(w, r, f.Path)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\""+path.Base(f.Name)+"\"")
		_, _ = w.Write(f.Content)
	}
}

func peBootedHandler(service PESessionService, peerCheckOff bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := peSession(w, r, service, peerCheckOff, logger)
		if !ok {
			return
		}
		if writeError(w, service.MarkBooted(r.Context(), token)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func peFailedHandler(service PESessionService, peerCheckOff bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := peSession(w, r, service, peerCheckOff, logger)
		if !ok {
			return
		}
		result := pe.PEResult{Stage: "boot", Message: "WinPE引导失败，iPXE错误码 " + r.URL.Query().Get("err")}
		if writeError(w, service.Complete(r.Context(), token, result)) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("recorded\n"))
	}
}

func peResultHandler(service PESessionService, peerCheckOff bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := peSession(w, r, service, peerCheckOff, logger)
		if !ok {
			return
		}
		var result pe.PEResult
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPEResultBytes)).Decode(&result); err != nil {
			http.Error(w, "结果不是合法 JSON", http.StatusBadRequest)
			return
		}
		if writeError(w, service.Complete(r.Context(), token, result)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func peLogHandler(service PESessionService, peerCheckOff bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := peSession(w, r, service, peerCheckOff, logger)
		if !ok {
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxPELogBytes)
		if writeError(w, service.SaveLog(r.Context(), token, chi.URLParam(r, "name"), body)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
