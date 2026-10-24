package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tianwei/diskless/internal/storage/zfs"
)

// poolStatusCache 让 /healthz 不必每次现起 zpool：忙时那要一两秒，探针会把慢当死。
// 结果用 every 时长，过期后先答旧结果、后台刷新；池卡死时 zpool 不返回，超过 staleAfter 没刷新出来就判不健康。
type poolStatusCache struct {
	fetch      func(context.Context, string) (zfs.PoolStatus, error)
	now        func() time.Time
	every      time.Duration
	staleAfter time.Duration

	mu         sync.Mutex
	pool       string
	status     zfs.PoolStatus
	err        error
	at         time.Time // 上次查询结束的时间
	refreshing bool
}

func (c *poolStatusCache) get(ctx context.Context, pool string) (zfs.PoolStatus, error) {
	c.mu.Lock()
	if c.pool != pool || c.at.IsZero() {
		// 刚启动或换了池（界面里新建池）：同步查，结果要准。
		c.mu.Unlock()
		st, err := c.fetch(ctx, pool)
		c.mu.Lock()
		c.pool, c.status, c.err, c.at = pool, st, err, c.now()
		c.mu.Unlock()
		return st, err
	}
	age := c.now().Sub(c.at)
	if age >= c.every && !c.refreshing {
		c.refreshing = true
		go c.refresh(pool)
	}
	st, err := c.status, c.err
	c.mu.Unlock()
	if age > c.staleAfter {
		return st, fmt.Errorf("存储池 %s 的状态 %d 秒没刷新出来，zpool 命令可能卡住了", pool, int(age.Seconds()))
	}
	return st, err
}

func (c *poolStatusCache) refresh(pool string) {
	ctx, cancel := context.WithTimeout(context.Background(), c.staleAfter)
	defer cancel()
	st, err := c.fetch(ctx, pool)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	if c.pool == pool {
		c.status, c.err, c.at = st, err, c.now()
	}
}
