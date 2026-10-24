package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/storage/zfs"
)

type poolFetchFake struct {
	mu     sync.Mutex
	calls  []string
	health string
	err    error
	block  chan struct{} // 非空时每次查询都卡住，直到被关闭
}

func (f *poolFetchFake) fetch(ctx context.Context, pool string) (zfs.PoolStatus, error) {
	f.mu.Lock()
	f.calls = append(f.calls, pool)
	health, err, block := f.health, f.err, f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return zfs.PoolStatus{Name: pool, Health: health}, err
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// eventually 等后台刷新把结果写回缓存。
func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("等了 1 秒条件仍不成立")
		}
	}
}

func (f *poolFetchFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// 忙时 zpool 命令要一两秒；健康检查每两秒一次，每次现查会把慢当死。查到的状态用几秒，过期了后台刷新。
func TestPoolStatusCacheAnswersFromTheLastLookup(t *testing.T) {
	clock := &testClock{t: time.Unix(1000, 0)}
	f := &poolFetchFake{health: "ONLINE"}
	c := &poolStatusCache{fetch: f.fetch, now: clock.now, every: 5 * time.Second, staleAfter: 30 * time.Second}

	// 第一次同步查，刚启动的结果要准。
	if st, err := c.get(context.Background(), "tank"); err != nil || st.Health != "ONLINE" || f.count() != 1 {
		t.Fatalf("first = %+v, %v, calls %d", st, err, f.count())
	}
	clock.add(3 * time.Second)
	if _, err := c.get(context.Background(), "tank"); err != nil || f.count() != 1 {
		t.Fatalf("5 秒内不该再查：calls %d, %v", f.count(), err)
	}

	// 过期：先答旧结果，后台刷新；刷新到的新状态下一次生效。
	clock.add(3 * time.Second)
	f.mu.Lock()
	f.health = "DEGRADED"
	f.mu.Unlock()
	if st, err := c.get(context.Background(), "tank"); err != nil || st.Health != "ONLINE" {
		t.Fatalf("过期时先答旧结果：%+v, %v", st, err)
	}
	eventually(t, func() bool { st, _ := c.get(context.Background(), "tank"); return st.Health == "DEGRADED" })
	if f.count() != 2 {
		t.Fatalf("刷新一次就够，calls %d", f.count())
	}

	// 界面里新建了池：换了名字就同步重查。
	if st, _ := c.get(context.Background(), "data"); st.Name != "data" {
		t.Fatalf("换池名后 = %+v", st)
	}
}

// zpool 卡死时后台刷新回不来；不能一直拿旧的 ONLINE 报健康。
func TestPoolStatusCacheFailsWhenRefreshHangs(t *testing.T) {
	clock := &testClock{t: time.Unix(1000, 0)}
	f := &poolFetchFake{health: "ONLINE"}
	c := &poolStatusCache{fetch: f.fetch, now: clock.now, every: 5 * time.Second, staleAfter: 30 * time.Second}
	if _, err := c.get(context.Background(), "tank"); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	defer close(block)
	f.mu.Lock()
	f.block = block
	f.mu.Unlock()

	clock.add(10 * time.Second)
	if _, err := c.get(context.Background(), "tank"); err != nil {
		t.Fatalf("30 秒内仍用旧结果：%v", err)
	}
	eventually(t, func() bool { return f.count() == 2 })
	clock.add(10 * time.Second)
	_, _ = c.get(context.Background(), "tank")
	time.Sleep(10 * time.Millisecond)
	if f.count() != 2 {
		t.Fatalf("同时只该有一次刷新在跑，calls %d", f.count())
	}
	clock.add(15 * time.Second)
	if _, err := c.get(context.Background(), "tank"); err == nil || !strings.Contains(err.Error(), "秒没刷新出来") {
		t.Fatalf("卡死超过 30 秒要判不健康：%v", err)
	}
}

// 查询本身报错（池读不了）也要原样报出来，nodeHealth 据此判断。
func TestPoolStatusCacheReportsLookupErrors(t *testing.T) {
	f := &poolFetchFake{err: errors.New("cannot open 'tank': no such pool")}
	c := &poolStatusCache{fetch: f.fetch, now: time.Now, every: 5 * time.Second, staleAfter: 30 * time.Second}
	if _, err := c.get(context.Background(), "tank"); err == nil || !strings.Contains(err.Error(), "no such pool") {
		t.Fatalf("err = %v", err)
	}
}
