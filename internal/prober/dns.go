package prober

import (
	"context"
	"fmt"
	"net"
	"time"

	"netprobe/internal/store"
)

// dnsProber 域名解析探测：解析成功即存活，可选校验期望 IP。
type dnsProber struct{}

func (d *dnsProber) Name() string { return "dns" }

type dnsExpect struct {
	// 解析结果必须包含这些 IP（可选）
	IPs []string `json:"ips"`
}

func (d *dnsProber) Probe(ctx context.Context, dev *store.Device) Result {
	start := time.Now()
	var exp dnsExpect
	parseExpect(dev.Expect, &exp)

	addrs, err := net.DefaultResolver.LookupHost(ctx, dev.Target)
	if err != nil {
		return newResult(false, time.Since(start), err.Error(), nil)
	}
	extra := map[string]any{"addrs": addrs}

	if len(exp.IPs) > 0 {
		set := make(map[string]bool, len(addrs))
		for _, a := range addrs {
			set[a] = true
		}
		for _, want := range exp.IPs {
			if !set[want] {
				return newResult(false, time.Since(start),
					fmt.Sprintf("expected ip %s missing (got %v)", want, addrs), extra)
			}
		}
	}
	return newResult(true, time.Since(start),
		fmt.Sprintf("resolved %s -> %v", dev.Target, addrs), extra)
}
