package prober

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"netprobe/internal/store"
)

func tcpServer(t *testing.T, open bool) string {
	t.Helper()
	if !open {
		// 拿一个已关闭的端口
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		return addr
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func TestTCPProber(t *testing.T) {
	p := &tcpProber{}

	res := p.Probe(context.Background(), &store.Device{Target: tcpServer(t, true), Timeout: 2})
	if !res.Success {
		t.Errorf("open port should succeed: %+v", res)
	}

	res = p.Probe(context.Background(), &store.Device{Target: tcpServer(t, false), Timeout: 1})
	if res.Success {
		t.Error("closed port should fail")
	}

	res = p.Probe(context.Background(), &store.Device{Target: "no-port-host", Timeout: 1})
	if res.Success || res.Detail == "" {
		t.Errorf("target without port should fail with detail: %+v", res)
	}
}

func TestCurlProber(t *testing.T) {
	p := &curlProber{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("hello-netprobe"))
	}))
	defer srv.Close()

	// 成功 + 关键字
	res := p.Probe(context.Background(), &store.Device{
		Target: srv.URL, Timeout: 3,
		Expect: `{"status_codes":[200],"body_contains":"hello"}`,
	})
	if !res.Success {
		t.Errorf("expect success: %+v", res)
	}
	if res.Extra["status_code"] != 200 {
		t.Errorf("extra status_code = %v", res.Extra["status_code"])
	}

	// 500 失败
	res = p.Probe(context.Background(), &store.Device{Target: srv.URL + "/bad", Timeout: 3})
	if res.Success {
		t.Error("500 should fail")
	}

	// 关键字不匹配
	res = p.Probe(context.Background(), &store.Device{
		Target: srv.URL, Timeout: 3, Expect: `{"body_contains":"nope"}`,
	})
	if res.Success {
		t.Error("body mismatch should fail")
	}

	// 超时失败
	res = p.Probe(context.Background(), &store.Device{Target: "http://127.0.0.1:1", Timeout: 1})
	if res.Success {
		t.Error("unreachable should fail")
	}
}

func TestDNSProber(t *testing.T) {
	p := &dnsProber{}

	// localhost 解析不依赖网络
	res := p.Probe(context.Background(), &store.Device{Target: "localhost", Timeout: 3})
	if !res.Success {
		t.Errorf("localhost should resolve: %+v", res)
	}

	res = p.Probe(context.Background(), &store.Device{
		Target: "localhost", Timeout: 3, Expect: `{"ips":["127.0.0.1"]}`,
	})
	if !res.Success {
		t.Errorf("expect 127.0.0.1: %+v", res)
	}

	res = p.Probe(context.Background(), &store.Device{
		Target: "localhost", Timeout: 3, Expect: `{"ips":["9.9.9.9"]}`,
	})
	if res.Success {
		t.Error("mismatched ip should fail")
	}

	res = p.Probe(context.Background(), &store.Device{Target: "invalid..domain..", Timeout: 3})
	if res.Success {
		t.Error("invalid domain should fail")
	}
}

func TestPingLocalhost(t *testing.T) {
	// 127.0.0.1 上 icmp 或 tcp 至少一种方式应成功
	p := &pingProber{}
	res := p.Probe(context.Background(), &store.Device{Target: "127.0.0.1", Timeout: 3})
	if !res.Success {
		t.Errorf("ping 127.0.0.1 should succeed via icmp/udp/tcp: %+v", res)
	}
}

// fakeProber 可编程失败次数的假探测器。
type fakeProber struct {
	failures int // 前 N 次失败
	calls    int
}

func (f *fakeProber) Name() string { return "fake" }

func (f *fakeProber) Probe(ctx context.Context, d *store.Device) Result {
	f.calls++
	if f.calls <= f.failures {
		return newResult(false, 10*time.Millisecond, "fake failure", nil)
	}
	return newResult(true, 20*time.Millisecond, "fake ok", nil)
}

func TestRunWithRetries(t *testing.T) {
	dev := &store.Device{Method: "fake", Target: "x", Timeout: 1, Retries: 2}

	// 第 2 次成功
	f := &fakeProber{failures: 1}
	res := RunWithRetries(context.Background(), dev, f)
	if !res.Success || res.Extra["attempts"] != 2 {
		t.Errorf("expect success on 2nd attempt: %+v", res)
	}

	// 全部失败
	f2 := &fakeProber{failures: 99}
	res = RunWithRetries(context.Background(), dev, f2)
	if res.Success || res.Extra["attempts"] != 3 {
		t.Errorf("expect fail after 3 attempts: %+v", res)
	}

	// retries=0 只探测一次
	f3 := &fakeProber{failures: 99}
	res = RunWithRetries(context.Background(), &store.Device{Timeout: 1, Retries: 0}, f3)
	if f3.calls != 1 {
		t.Errorf("retries=0 should probe once, got %d calls", f3.calls)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	for _, m := range []string{"ping", "curl", "tcp", "dns"} {
		if _, ok := r.Get(m); !ok {
			t.Errorf("method %s not registered", m)
		}
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("unknown method should miss")
	}
}

func TestTimeoutOf(t *testing.T) {
	if d := timeoutOf(context.Background()); d != 5*time.Second {
		t.Errorf("default timeout = %v", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if d := timeoutOf(ctx); d > 2*time.Second || d <= 0 {
		t.Errorf("deadline timeout = %v", d)
	}
}

var _ = errors.New // 保持 import
