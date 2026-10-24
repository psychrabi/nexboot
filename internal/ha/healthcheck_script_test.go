package ha

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// keepalived 是否搬走 VIP 全看这个脚本的退出码，它是高可用链条上唯一不是 Go 写的判据。
// 最坏路径不能超出 keepalived 的 timeout，否则脚本被杀、健康节点被判故障。
// 脚本要分清三种情况，混淆任何一对都会出事：
//
//	200        健康 → 留住 VIP
//	其它 HTTP  服务活着但自己说不行（池坏/库坏/维护标记/刚退位）→ 立刻让出
//	连不上     可能只是切换中的重启空窗 → 看切换标记，新鲜就再等一轮
//
// 最后一条防自激：把切换造成的短暂连不上判成故障，会滚成「切换→探针失败→再切换」的死循环。
func TestHealthCheckScriptDistinguishesUnhealthyFromUnreachable(t *testing.T) {
	script := filepath.Join("..", "..", "deploy", "ndiskless-check.sh")
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Skipf("找不到探针脚本，跳过：%v", err)
	}

	run := func(t *testing.T, httpCode string, marker markerState) (int, time.Duration) {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		// 假 curl：脚本只看 -w '%{http_code}' 的那一行输出。
		stub(t, filepath.Join(bin, "curl"), fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", httpCode))
		// 假 stat：脚本用 GNU 的 `stat -c %Y`，开发机上未必有；桩掉它，保证在哪都测同一套判断逻辑。
		switchFile := filepath.Join(dir, "switching")
		stampScript := "#!/bin/sh\necho 0\n"
		switch marker {
		case markerFresh:
			touch(t, switchFile)
			stampScript = "#!/bin/sh\nexec date +%s\n" // 刚打的标记 = 现在
		case markerStale:
			touch(t, switchFile)
			stampScript = "#!/bin/sh\necho 1\n" // 1970 年，必然过期
		}
		stub(t, filepath.Join(bin, "stat"), stampScript)

		body := strings.ReplaceAll(string(raw), "@API_PORT@", "18080")
		body = strings.ReplaceAll(body, "/run/ndiskless.switching", switchFile)
		path := filepath.Join(dir, "check.sh")
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("/bin/sh", path)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		start := time.Now()
		err := cmd.Run()
		elapsed := time.Since(start)
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("脚本没跑起来：%v", err)
		}
		return code, elapsed
	}

	t.Run("健康则留住VIP", func(t *testing.T) {
		if code, _ := run(t, "200", markerNone); code != 0 {
			t.Fatalf("200 应当 exit 0，得到 %d", code)
		}
	})

	t.Run("服务自称不健康则立刻让出VIP", func(t *testing.T) {
		for _, c := range []string{"503", "500", "404"} {
			if code, _ := run(t, c, markerNone); code != 1 {
				t.Fatalf("HTTP %s 应当 exit 1（服务活着、自己说不行），得到 %d", c, code)
			}
		}
	})

	t.Run("连不上且没在切换则判死", func(t *testing.T) {
		if code, _ := run(t, "000", markerNone); code != 1 {
			t.Fatalf("连不上且无切换标记应当 exit 1，得到 %d", code)
		}
	})

	t.Run("切换窗口内连不上不搬VIP", func(t *testing.T) {
		code, _ := run(t, "000", markerFresh)
		if code != 0 {
			t.Fatalf("切换中的重启空窗必须 exit 0，得到 %d——"+
				"判成故障会让「切换→探针失败→再切换」滚成死循环", code)
		}
	})

	t.Run("切换标记过期说明它没回来", func(t *testing.T) {
		if code, _ := run(t, "000", markerStale); code != 1 {
			t.Fatalf("标记过期（超过 15 秒）应当 exit 1，得到 %d——"+
				"否则一台真的死掉的节点会永远占着 VIP", code)
		}
	})

	// 时间预算：keepalived 会杀掉超时脚本并判失败。最坏路径是「连不上 + 切换窗口内」：两次 curl 加一次 sleep 0.5，要留足余量。
	t.Run("最坏路径留在keepalived的时间预算内", func(t *testing.T) {
		_, elapsed := run(t, "000", markerFresh)
		if elapsed > 3*time.Second {
			t.Fatalf("最坏路径耗时 %v，超过 keepalived 的 timeout 预算——"+
				"脚本会被杀并判失败，健康节点因此进 FAULT，谁都不接管", elapsed)
		}
	})
}

// 忙时 /healthz 要一两秒才回：探针等得太短会把慢当死，两台可能一起让出 VIP。
// 放宽后最坏路径仍须小于 keepalived 的 timeout，否则脚本被杀照样判失败。
func TestHealthProbeToleratesASlowButHealthyNode(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "deploy", "ndiskless-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "deploy", "keepalived.conf.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	number := func(re string, src []byte) []float64 {
		var out []float64
		for _, m := range regexp.MustCompile(re).FindAllSubmatch(src, -1) {
			v, err := strconv.ParseFloat(string(m[1]), 64)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}
	waits := number(`curl [^\n]*-m ([0-9.]+)`, script)
	sleeps := number(`(?m)^\s*sleep ([0-9.]+)`, script)
	timeout := number(`(?m)^\s*timeout ([0-9]+)`, tmpl)
	fall := number(`(?m)^\s*fall ([0-9]+)`, tmpl)
	if len(waits) == 0 || len(timeout) != 1 || len(fall) != 1 {
		t.Fatalf("没读到探针参数：waits=%v timeout=%v fall=%v", waits, timeout, fall)
	}
	if waits[0] < 3 {
		t.Errorf("单次探测只等 %v 秒，忙时健康节点也回不过来", waits[0])
	}
	if fall[0] < 3 {
		t.Errorf("连续 %v 次失败就让出 VIP，忙时一抖就切", fall[0])
	}
	worst := 0.0
	for _, v := range append(waits, sleeps...) {
		worst += v
	}
	if worst >= timeout[0] {
		t.Errorf("最坏路径 %v 秒不小于 keepalived timeout %v 秒，脚本会被杀并判失败", worst, timeout[0])
	}
}

type markerState int

const (
	markerNone markerState = iota
	markerFresh
	markerStale
)

func stub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
