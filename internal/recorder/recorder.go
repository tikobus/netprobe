// Package recorder 负责探测结果双写：SQLite（报表）+ 按天滚动的日志文件（审计）。
package recorder

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/prober"
	"netprobe/internal/store"
)

type Recorder struct {
	cfg *config.Config
	st  *store.Store
	log *slog.Logger

	mu   sync.Mutex
	day  string
	file *os.File
}

func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Recorder {
	return &Recorder{cfg: cfg, st: st, log: log}
}

// Start 启动周期清理（DB 原始记录 + 过期日志文件），阻塞直到 ctx 结束。
func (r *Recorder) Start(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	r.cleanup()
	for {
		select {
		case <-ctx.Done():
			r.Close()
			return
		case <-t.C:
			r.cleanup()
		}
	}
}

func (r *Recorder) cleanup() {
	// DB：默认保留 db_retain_days 天原始记录（0 = 不清理）
	if days := r.cfg.Storage.DBRetainDays; days > 0 {
		if n, err := r.st.DeleteProbeLogsBefore(context.Background(), time.Now().AddDate(0, 0, -days)); err != nil {
			r.log.Error("db log cleanup failed", "err", err)
		} else if n > 0 {
			r.log.Info("db probe logs cleaned", "deleted", n)
		}
	}
	// 文件：删除过期 probe-YYYYMMDD.log
	entries, err := filepath.Glob(filepath.Join(r.cfg.Log.Dir, "probe-*.log"))
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -r.cfg.Log.RetainDays)
	for _, f := range entries {
		base := filepath.Base(f)
		if len(base) != len("probe-20060102.log") {
			continue
		}
		ts, err := time.ParseInLocation("20060102", base[6:14], time.Local)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			_ = os.Remove(f)
		}
	}
}

// Record 写入一条探测结果。DB 失败时仍写文件（保留审计）。
func (r *Recorder) Record(ctx context.Context, dev *store.Device, res *prober.Result) error {
	at := time.Now()
	l := &store.ProbeLog{
		DeviceID:  dev.ID,
		Success:   res.Success,
		LatencyMS: res.LatencyMS,
		Detail:    res.Detail,
		ProbedAt:  at,
	}
	if err := r.st.InsertProbeLog(ctx, l); err != nil {
		r.log.Error("insert probe log failed", "device", dev.Name, "err", err)
	}
	return r.writeFileLine(dev, res, at)
}

type fileLine struct {
	Time      string         `json:"time"`
	DeviceID  int64          `json:"device_id"`
	Name      string         `json:"name"`
	Method    string         `json:"method"`
	Target    string         `json:"target"`
	Success   bool           `json:"success"`
	LatencyMS float64        `json:"latency_ms"`
	Detail    string         `json:"detail"`
	Extra     map[string]any `json:"extra,omitempty"`
}

func (r *Recorder) writeFileLine(dev *store.Device, res *prober.Result, at time.Time) error {
	line, err := json.Marshal(fileLine{
		Time:      at.Format(time.RFC3339Nano),
		DeviceID:  dev.ID,
		Name:      dev.Name,
		Method:    dev.Method,
		Target:    dev.Target,
		Success:   res.Success,
		LatencyMS: res.LatencyMS,
		Detail:    res.Detail,
		Extra:     res.Extra,
	})
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	day := at.Format("20060102")
	if r.file == nil || day != r.day {
		if r.file != nil {
			r.file.Close()
		}
		if err := os.MkdirAll(r.cfg.Log.Dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(r.cfg.Log.Dir, "probe-"+day+".log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		r.file, r.day = f, day
	}
	_, err = r.file.Write(append(line, '\n'))
	return err
}

// Close 关闭当前日志文件句柄。
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
}
