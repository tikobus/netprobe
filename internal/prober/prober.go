// Package prober 实现各探活方式（ping/curl/tcp/dns）。
package prober

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"netprobe/internal/store"
)

// Result 一次探测的结果。
type Result struct {
	Success   bool           `json:"success"`
	LatencyMS float64        `json:"latency_ms"`
	Detail    string         `json:"detail"`
	Extra     map[string]any `json:"extra,omitempty"`
}

func newResult(success bool, d time.Duration, detail string, extra map[string]any) Result {
	return Result{Success: success, LatencyMS: ms(d), Detail: detail, Extra: extra}
}

// ms 时长转毫秒，保留两位小数。
func ms(d time.Duration) float64 {
	return math.Round(float64(d.Microseconds())/10) / 100
}

// Prober 探测器接口。
type Prober interface {
	Name() string
	Probe(ctx context.Context, d *store.Device) Result
}

// Registry method -> Prober 注册表。
type Registry struct {
	m map[string]Prober
}

func NewRegistry() *Registry {
	r := &Registry{m: make(map[string]Prober)}
	r.Register(&pingProber{})
	r.Register(&curlProber{})
	r.Register(&tcpProber{})
	r.Register(&dnsProber{})
	return r
}

func (r *Registry) Register(p Prober) { r.m[p.Name()] = p }

func (r *Registry) Get(method string) (Prober, bool) {
	p, ok := r.m[method]
	return p, ok
}

// timeoutOf 取本次探测可用的时间预算（ctx 截止时间），无截止时默认 5s。
func timeoutOf(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d
		}
		return 100 * time.Millisecond
	}
	return 5 * time.Second
}

// parseExpect 解析设备 Expect JSON 到 v，空/解析失败时保持零值。
func parseExpect(s string, v any) {
	if s == "" {
		return
	}
	_ = json.Unmarshal([]byte(s), v) // 合法性已由 store 层校验
}

// RunWithRetries 带重试的探测：每 attempt 用 device.Timeout 截止，
// 失败后间隔 500ms 重试，最多 retries+1 次。
func RunWithRetries(ctx context.Context, d *store.Device, p Prober) Result {
	timeout := time.Duration(d.Timeout) * time.Second
	attempts := d.Retries + 1
	var last Result
	for i := 1; i <= attempts; i++ {
		actx, cancel := context.WithTimeout(ctx, timeout)
		last = p.Probe(actx, d)
		cancel()
		if last.Extra == nil {
			last.Extra = map[string]any{}
		}
		last.Extra["attempts"] = i
		if last.Success {
			return last
		}
		if i < attempts {
			select {
			case <-ctx.Done():
				last.Detail += " (cancelled)"
				return last
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return last
}
