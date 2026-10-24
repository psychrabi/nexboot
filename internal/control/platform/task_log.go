package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/control/pe"
	"github.com/tianwei/diskless/internal/domain"
)

// TaskLog 优先读本机文件；尚未复制过来的日志从登记的节点读取。
func (c ClusterTasks) TaskLog(ctx context.Context, id, name string) (pe.PEFile, error) {
	f, localErr := c.LocalTaskLog(ctx, id, name)
	if !errs.IsNotFound(localErr) {
		return f, localErr
	}
	servers, err := c.Store.Servers().List(ctx)
	if err != nil {
		return pe.PEFile{}, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var failed []error
	var offline []string
	for _, srv := range servers {
		if srv.ID == c.NodeID || srv.APIURL == "" {
			continue
		}
		// 离线节点问了也是等超时，错误还会盖住「日志不存在」这个真实结果。
		if srv.Status != domain.ServerStatusUp {
			offline = append(offline, srv.ID)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(srv.APIURL, "/")+"/internal/node/tasks/"+url.PathEscape(id)+"/logs/"+url.PathEscape(name), nil)
		if err != nil {
			return pe.PEFile{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			failed = append(failed, fmt.Errorf("节点 %s 日志读不到：%w", srv.ID, err))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if readErr != nil {
			failed = append(failed, readErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			failed = append(failed, fmt.Errorf("节点 %s 日志读取失败：%s", srv.ID, resp.Status))
			continue
		}
		if len(data) > 16<<20 {
			return pe.PEFile{}, errs.Invalid("日志超过 16MB")
		}
		return pe.PEFile{Name: name, Content: data}, nil
	}
	if len(failed) > 0 {
		return pe.PEFile{}, errors.Join(failed...)
	}
	if len(offline) > 0 {
		return pe.PEFile{}, errs.NotFound(fmt.Sprintf("%s（节点 %s 离线，没能问到）", localErr.Error(), strings.Join(offline, "、")))
	}
	return pe.PEFile{}, localErr
}
