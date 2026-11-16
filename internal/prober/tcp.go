package prober

import (
	"context"
	"net"
	"time"

	"netprobe/internal/store"
)

// tcpProber TCP 端口连通性探测。
type tcpProber struct{}

func (t *tcpProber) Name() string { return "tcp" }

func (t *tcpProber) Probe(ctx context.Context, d *store.Device) Result {
	start := time.Now()
	if _, _, err := net.SplitHostPort(d.Target); err != nil {
		return newResult(false, 0, "target 需要形如 host:port，当前: "+d.Target, nil)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.Target)
	if err != nil {
		return newResult(false, time.Since(start), err.Error(), nil)
	}
	remote := conn.RemoteAddr().String()
	conn.Close()
	return newResult(true, time.Since(start), "connected "+remote, map[string]any{"addr": remote})
}
