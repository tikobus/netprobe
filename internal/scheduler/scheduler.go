// Package scheduler 周期调度探测任务并由协程池并发执行。
package scheduler

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/prober"
	"netprobe/internal/recorder"
	"netprobe/internal/store"
)

const (
	reloadInterval = 30 * time.Second // 设备列表重载周期
	tickInterval   = 500 * time.Millisecond
	taskQueueSize  = 256
)

type entry struct {
	dev  *store.Device
	next time.Time
}

type Scheduler struct {
	cfg *config.Config
	st  *store.Store
	reg *prober.Registry
	rec *recorder.Recorder
	log *slog.Logger

	tasks chan *store.Device
	kick  chan struct{} // 收到信号后尽快重载设备列表

	mu       sync.Mutex
	entries  map[int64]*entry
	statuses map[int64]bool // 最近一次探测是否成功
}

func New(cfg *config.Config, st *store.Store, reg *prober.Registry, rec *recorder.Recorder, log *slog.Logger) *Scheduler {
	return &Scheduler{
		cfg:      cfg,
		st:       st,
		reg:      reg,
		rec:      rec,
		log:      log,
		tasks:    make(chan *store.Device, taskQueueSize),
		kick:     make(chan struct{}, 1),
		entries:  make(map[int64]*entry),
		statuses: make(map[int64]bool),
	}
}

// Start 启动调度循环与工作协程，阻塞直到 ctx 结束。
func (s *Scheduler) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < s.cfg.Prober.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	s.loop(ctx)
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context) {
	reload := time.NewTicker(reloadInterval)
	defer reload.Stop()
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()

	s.reload()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			s.reload()
		case <-reload.C:
			s.reload()
		case <-tick.C:
			s.dispatch()
		}
	}
}

// reload 全量刷新设备列表：新增/更新/删除均以 DB 为准。
func (s *Scheduler) reload() {
	devs, err := s.st.ListDevices(context.Background())
	if err != nil {
		s.log.Error("reload devices failed", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[int64]bool, len(devs))
	for _, d := range devs {
		seen[d.ID] = true
		if e, ok := s.entries[d.ID]; ok {
			e.dev = d
		} else {
			// 首次调度随机打散，避免同时探测打爆网络
			s.entries[d.ID] = &entry{
				dev:  d,
				next: time.Now().Add(time.Duration(rand.Intn(d.Interval)) * time.Second),
			}
		}
	}
	for id := range s.entries {
		if !seen[id] {
			delete(s.entries, id)
			delete(s.statuses, id)
		}
	}
}

func (s *Scheduler) dispatch() {
	now := time.Now()
	s.mu.Lock()
	var due []*store.Device
	for _, e := range s.entries {
		if e.dev.Enabled && !now.Before(e.next) {
			e.next = now.Add(time.Duration(e.dev.Interval) * time.Second)
			due = append(due, e.dev)
		}
	}
	s.mu.Unlock()

	for _, dev := range due {
		select {
		case s.tasks <- dev:
		default:
			s.log.Warn("task queue full, probe dropped", "device", dev.Name, "id", dev.ID)
		}
	}
}

func (s *Scheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case dev := <-s.tasks:
			s.probeAndRecord(ctx, dev)
		}
	}
}

// probeAndRecord 执行一次完整探测（含重试）并记录，返回结果。
func (s *Scheduler) probeAndRecord(ctx context.Context, dev *store.Device) prober.Result {
	p, ok := s.reg.Get(dev.Method)
	var res prober.Result
	if !ok {
		res = prober.Result{Success: false, Detail: "unknown probe method: " + dev.Method}
	} else {
		res = prober.RunWithRetries(ctx, dev, p)
	}

	// 服务停机导致的取消不作为真实探测结果记录
	if ctx.Err() != nil {
		res.Detail += " (skipped: shutting down)"
		return res
	}

	if ch := s.noteStatus(dev.ID, res.Success); ch != "" {
		if res.Extra == nil {
			res.Extra = map[string]any{}
		}
		res.Extra["status_change"] = ch
		s.log.Info("device status changed", "device", dev.Name, "change", ch)
	}

	if err := s.rec.Record(context.Background(), dev, &res); err != nil {
		s.log.Error("record result failed", "device", dev.Name, "err", err)
	}
	if !res.Success {
		s.log.Warn("probe failed", "device", dev.Name, "method", dev.Method,
			"target", dev.Target, "detail", res.Detail)
	}
	return res
}

// noteStatus 记录最近状态，返回状态变化描述（无变化返回空串）。
func (s *Scheduler) noteStatus(id int64, success bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, has := s.statuses[id]
	s.statuses[id] = success
	if !has || prev == success {
		return ""
	}
	if success {
		return "down->up"
	}
	return "up->down"
}

// TestNow 立即探测一次指定设备（同步执行并记录结果），供 Web "立即测试" 使用。
func (s *Scheduler) TestNow(ctx context.Context, id int64) (prober.Result, error) {
	dev, err := s.st.GetDevice(ctx, id)
	if err != nil {
		return prober.Result{}, err
	}
	return s.probeAndRecord(ctx, dev), nil
}

// Kick 通知调度器尽快重载设备列表（设备增删改后由 Web 层调用）。
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// reloadForTest 手动触发设备列表重载（测试用）。
func (s *Scheduler) reloadForTest() { s.reload() }
