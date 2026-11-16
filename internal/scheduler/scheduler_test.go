package scheduler

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/prober"
	"netprobe/internal/recorder"
	"netprobe/internal/store"
)

// okProber 恒定成功的假探测器（覆盖 tcp 方法）。
type okProber struct{ calls int }

func (p *okProber) Name() string { return "tcp" }
func (p *okProber) Probe(ctx context.Context, d *store.Device) prober.Result {
	p.calls++
	return prober.Result{Success: true, LatencyMS: 1.5, Detail: "fake ok"}
}

// flakyProber 先失败后成功，验证状态变化标记。
type flakyProber struct{ calls int }

func (p *flakyProber) Name() string { return "tcp" }
func (p *flakyProber) Probe(ctx context.Context, d *store.Device) prober.Result {
	p.calls++
	if p.calls%2 == 1 {
		return prober.Result{Success: false, Detail: "flaky fail"}
	}
	return prober.Result{Success: true, LatencyMS: 2, Detail: "flaky ok"}
}

func newTestSetup(t *testing.T, p prober.Prober) (*config.Config, *store.Store, *Scheduler) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Default()
	cfg.Log.Dir = filepath.Join(t.TempDir(), "logs")
	cfg.Prober.Concurrency = 2

	reg := prober.NewRegistry()
	reg.Register(p)
	rec := recorder.New(&cfg, st, slog.Default())
	s := New(&cfg, st, reg, rec, slog.Default())
	return &cfg, st, s
}

func TestSchedulerPeriodicProbing(t *testing.T) {
	p := &okProber{}
	_, st, s := newTestSetup(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	dev := store.Device{Name: "t1", Type: "tcp", Method: "tcp", Target: "127.0.0.1:80",
		Interval: 1, Timeout: 1, Retries: 0, Enabled: true}
	if err := st.CreateDevice(context.Background(), &dev); err != nil {
		t.Fatal(err)
	}

	// 等调度器 reload（30s 周期太长 —— 首次 reload 在 Start 时立即执行，
	// 设备在 Start 后创建，需等下一次 reload；测试中直接调用内部方法刷新）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.reloadForTest()
		logs, _ := st.ListProbeLogs(context.Background(), store.LogFilter{DeviceID: dev.ID})
		if len(logs) >= 2 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no periodic probes recorded within timeout")
}

func TestStatusChangeDetection(t *testing.T) {
	p := &flakyProber{}
	_, st, s := newTestSetup(t, p)

	dev := store.Device{Name: "f1", Type: "tcp", Method: "tcp", Target: "127.0.0.1:80",
		Interval: 60, Timeout: 1, Retries: 0, Enabled: true}
	if err := st.CreateDevice(context.Background(), &dev); err != nil {
		t.Fatal(err)
	}

	// 第一次：失败（无状态变化标记）
	res1, err := s.TestNow(context.Background(), dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Success {
		t.Fatalf("first probe should fail: %+v", res1)
	}
	if _, has := res1.Extra["status_change"]; has {
		t.Error("first probe should not mark status change")
	}

	// 第二次：成功（down->up）
	res2, err := s.TestNow(context.Background(), dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Success || res2.Extra["status_change"] != "down->up" {
		t.Errorf("second probe should mark down->up: %+v", res2)
	}

	// 日志文件已写入
	logs, err := st.ListProbeLogs(context.Background(), store.LogFilter{DeviceID: dev.ID})
	if err != nil || len(logs) != 2 {
		t.Fatalf("expect 2 logs, got %d (%v)", len(logs), err)
	}
}

func TestTestNowUnknownDevice(t *testing.T) {
	_, _, s := newTestSetup(t, &okProber{})
	if _, err := s.TestNow(context.Background(), 999); err != store.ErrNotFound {
		t.Errorf("expect ErrNotFound, got %v", err)
	}
}
