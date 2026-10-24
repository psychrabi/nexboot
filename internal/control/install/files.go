package install

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/storage"
)

func (s *Service) SessionByToken(ctx context.Context, token string) (domain.InstallSession, error) {
	sess, err := s.Store.InstallSessions().ByToken(ctx, token)
	if err != nil {
		return sess, err
	}
	if err := s.ensureOwner(sess); err != nil {
		return sess, err
	}
	if !sess.Active() && sess.Phase != "ready" {
		return sess, errs.NotFound("安装会话已结束")
	}
	return sess, nil
}
func (s *Service) File(ctx context.Context, token, name string) (pe.PEFile, error) {
	sess, err := s.SessionByToken(ctx, token)
	if err != nil {
		return pe.PEFile{}, err
	}
	if name == "boot.json" {
		body, _ := json.Marshal(map[string]any{"phase": sess.Phase, "attempt": sess.Attempt, "boot_id": sess.BootID})
		return pe.PEFile{Name: name, Content: body}, nil
	}
	if !sess.Active() {
		return pe.PEFile{}, errs.NotFound("安装会话已结束")
	}
	if name == "media.iso" {
		return pe.PEFile{Name: name, Path: filepath.Join(sess.MediaDir, "media.iso")}, nil
	}
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\x00") || filepath.IsAbs(name) {
		return pe.PEFile{}, errs.NotFound("安装文件不存在")
	}
	// Runtime returns the exact allowlist; directory traversal and arbitrary host files are never served.
	files, err := s.media(ctx, sess)
	if err != nil {
		return pe.PEFile{}, err
	}
	if content, ok := files.Content[name]; ok {
		return pe.PEFile{Name: name, Content: content}, nil
	}
	if path, ok := files.Files[name]; ok {
		return pe.PEFile{Name: name, Path: path}, nil
	}
	return pe.PEFile{}, errs.NotFound("安装文件不存在")
}

var allowedLogs = map[string]bool{"console.log": true, "install.log": true, "dism.log": true, "setupact.log": true, "subiquity.log": true, "anaconda.log": true, "storage.log": true, "program.log": true, "iscsi.log": true}

// Installer storage diagnostics may contain iBFT credentials unknown to the server.
var diagnosticCredential = regexp.MustCompile(`(?im)([^\s=:\n]*(?:password|passwd|secret|token)[^\s=:\n]*\s*[=:]\s*)[^\r\n]*`)
var diagnosticURL = regexp.MustCompile(`https?://[^\s"'<>]+`)

func redactDiagnosticLog(name string, b []byte) []byte {
	if name != "storage.log" && name != "program.log" && name != "iscsi.log" {
		return b
	}
	b = diagnosticCredential.ReplaceAll(b, []byte("${1}[REDACTED]"))
	return diagnosticURL.ReplaceAll(b, []byte("[URL REDACTED]"))
}

func (s *Service) SaveLog(ctx context.Context, token, name string, r io.Reader) error {
	sess, err := s.SessionByToken(ctx, token)
	if err != nil {
		return err
	}
	if !sess.Active() {
		return errs.Conflict("安装任务已结束，不能继续上传安装日志")
	}
	if !allowedLogs[name] {
		return errs.Invalid("不支持该安装日志名称")
	}
	if sess.MediaDir == "" {
		return errs.Conflict("安装资源尚未准备好")
	}
	dir := filepath.Join(sess.MediaDir, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(r, 16<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return errs.Invalid("安装日志超过 16 MiB，请缩小日志后重试")
	}
	f, err := os.CreateTemp(dir, "log-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(redactDiagnosticLog(name, s.redact(sess, b)))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
func (s *Service) TaskLog(ctx context.Context, taskID, name string) (pe.PEFile, error) {
	if !allowedLogs[name] {
		return pe.PEFile{}, errs.NotFound("安装日志不存在")
	}
	all, err := s.Store.InstallSessions().List(ctx)
	if err != nil {
		return pe.PEFile{}, err
	}
	for _, sess := range all {
		if sess.TaskID == taskID {
			p := filepath.Join(sess.MediaDir, "logs", name)
			if _, err := os.Stat(p); err != nil {
				return pe.PEFile{}, errs.NotFound("安装日志尚未上传")
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return pe.PEFile{}, err
			}
			return pe.PEFile{Name: name, Content: redactDiagnosticLog(name, s.redact(sess, b))}, nil
		}
	}
	return pe.PEFile{}, errs.NotFound("安装任务不存在")
}

type Source struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}
type Sources struct {
	Items     []Source `json:"items"`
	ImportDir string   `json:"import_dir"`
}

func (s *Service) Sources(ctx context.Context) (Sources, error) {
	out := Sources{Items: []Source{}}
	if s.ImportDir == nil {
		return out, errs.Invalid("请先创建存储池：上传和导入的文件存放在存储池的 imports 目录")
	}
	dir, err := s.ImportDir(ctx)
	if err != nil {
		return out, err
	}
	out.ImportDir = dir
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".iso") {
			info, err := e.Info()
			if err != nil {
				return out, err
			}
			out.Items = append(out.Items, Source{Name: e.Name(), Path: filepath.Join(dir, e.Name()), SizeBytes: info.Size()})
		}
	}
	return out, nil
}

func (s *Service) redact(sess domain.InstallSession, b []byte) []byte {
	// DISM/setup logs may be UTF-16, which must be decoded before exact secret matching.
	if len(b) >= 2 && (bytes.HasPrefix(b, []byte{0xff, 0xfe}) || bytes.HasPrefix(b, []byte{0xfe, 0xff})) {
		little := b[0] == 0xff
		var units []uint16
		for i := 2; i+1 < len(b); i += 2 {
			n := uint16(b[i])<<8 | uint16(b[i+1])
			if little {
				n = uint16(b[i]) | uint16(b[i+1])<<8
			}
			units = append(units, n)
		}
		b = []byte(string(utf16.Decode(units)))
	}
	secrets := []string{sess.Token, sess.Password}
	if sess.MediaDir != "" {
		if hash, err := os.ReadFile(filepath.Join(sess.MediaDir, "password.hash")); err == nil {
			secrets = append(secrets, strings.TrimSpace(string(hash)))
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), html.EscapeString(secret), string(encoded[1 : len(encoded)-1])} {
			b = bytes.ReplaceAll(b, []byte(value), []byte("[REDACTED]"))
		}
	}
	if s.RedactSecrets != nil {
		b = s.RedactSecrets(sess, b)
	}
	return b
}

func cleanupRequest(sess domain.InstallSession, nodeID string) storage.InstallationCleanupReq {
	return storage.InstallationCleanupReq{NodeID: nodeID, ID: sess.ID, MAC: sess.MAC, ImageID: sess.ImageID, Password: sess.Password, Token: sess.Token}
}

// CleanupLocalInstallation accepts only an exact attempt from the authenticated writer, without reading this node's catalogue.
func (s *Service) CleanupLocalInstallation(ctx context.Context, req storage.InstallationCleanupReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.NodeID == "" || req.NodeID != s.NodeID || !regexp.MustCompile(`^install-[0-9a-f]{16}$`).MatchString(req.ID) ||
		!regexp.MustCompile(`^[0-9A-F]{12}$`).MatchString(req.MAC) || (req.ImageID != "" && req.ImageID != "iso_"+strings.TrimPrefix(req.ID, "install-")) {
		return errs.Invalid("安装资源清理目标不匹配")
	}
	if s.FilesDir == nil {
		return errs.Invalid("当前节点尚未配置安装文件目录")
	}
	return s.cleanupInstallationResources(ctx, domain.InstallSession{ID: req.ID, MAC: req.MAC, ImageID: req.ImageID, Password: req.Password, Token: req.Token})
}

func (s *Service) cleanupInstallationResources(ctx context.Context, sess domain.InstallSession) error {
	active, err := s.Storage.ActiveClientMACs(ctx)
	if err != nil {
		return err
	}
	for _, mac := range active {
		if storage.NormalizeMAC(mac) == sess.MAC {
			return errs.Conflict("请先关闭安装客户机「" + sess.MAC + "」，再重试结束安装")
		}
	}
	if err := s.Host.CleanupInstallation(ctx, storage.InstallationDiskReq{ID: sess.ID, MAC: sess.MAC, SizeBytes: sess.SizeBytes}); err != nil {
		return err
	}
	if sess.ImageID != "" {
		if err := s.Host.CleanupInstallationCandidate(ctx, cleanupRequest(sess, s.NodeID)); err != nil {
			return err
		}
	}
	if s.FilesDir != nil {
		root, err := s.FilesDir(ctx)
		if err != nil {
			return err
		}
		sess.MediaDir = filepath.Join(root, "installations", sess.ID)
	}
	return s.cleanupMedia(sess)
}

// Terminal sessions retain diagnostics, but no longer retain installation credentials or ISO copies.
func (s *Service) cleanupMedia(sess domain.InstallSession) error {
	if sess.MediaDir == "" {
		return nil
	}
	entries, err := os.ReadDir(sess.MediaDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(sess.MediaDir, entry.Name())
		if entry.Name() == "logs" {
			for name := range allowedLogs {
				logfile := filepath.Join(path, name)
				b, err := os.ReadFile(logfile)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return err
				}
				if err = os.WriteFile(logfile, redactDiagnosticLog(name, s.redact(sess, b)), 0600); err != nil {
					return err
				}
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}
