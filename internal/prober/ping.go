package prober

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"netprobe/internal/store"
)

// pingProber ICMP ping 探活。
// 优先 raw ICMP（需 root/CAP_NET_RAW），失败后尝试无特权 UDP ICMP，
// 均不可用时自动降级为 TCP ping（对常见端口发起连接）。
type pingProber struct{}

func (p *pingProber) Name() string { return "ping" }

type pingExpect struct {
	// ICMP 不可用时 TCP ping 尝试的端口，默认 [80 443 22]
	TCPFallbackPorts []int `json:"tcp_fallback_ports"`
}

func (p *pingProber) Probe(ctx context.Context, d *store.Device) Result {
	var exp pingExpect
	parseExpect(d.Expect, &exp)
	ports := exp.TCPFallbackPorts
	if len(ports) == 0 {
		ports = []int{80, 443, 22}
	}

	start := time.Now()
	ip, err := lookupIP(ctx, d.Target)
	if err != nil {
		return newResult(false, time.Since(start), "resolve: "+err.Error(), nil)
	}

	if rtt, mode, err := probeICMP(ctx, ip); err == nil {
		return newResult(true, rtt, fmt.Sprintf("icmp reply from %s", ip),
			map[string]any{"mode": mode, "ip": ip.String()})
	} else if time.Since(start) > timeoutOf(ctx) {
		return newResult(false, time.Since(start), "icmp: "+err.Error(), nil)
	} else {
		return p.tcpFallback(ctx, ip, ports, start, err)
	}
}

// tcpFallback 对 ip 的候选端口逐个尝试 TCP 连接，任一成功即视为存活。
func (p *pingProber) tcpFallback(ctx context.Context, ip net.IP, ports []int, start time.Time, icmpErr error) Result {
	for _, port := range ports {
		addr := net.JoinHostPort(ip.String(), fmt.Sprint(port))
		dialCtx, cancel := context.WithTimeout(ctx, timeoutOf(ctx))
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			return newResult(true, time.Since(start), "tcp-ping connected "+addr,
				map[string]any{"mode": "tcp", "ip": ip.String(), "port": port, "icmp_error": icmpErr.Error()})
		}
	}
	return newResult(false, time.Since(start),
		fmt.Sprintf("icmp: %v; tcp fallback: all ports(%v) unreachable", icmpErr, ports), nil)
}

func lookupIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no ip for %s", host)
	}
	for _, ip := range ips { // 优先 IPv4
		if ip.IP.To4() != nil {
			return ip.IP, nil
		}
	}
	return ips[0].IP, nil
}

// probeICMP 依次尝试 raw / udp 两种 ICMP socket，返回 RTT 与实际模式。
func probeICMP(ctx context.Context, ip net.IP) (time.Duration, string, error) {
	timeout := timeoutOf(ctx)
	var tries []struct{ network, src string }
	if ip.To4() != nil {
		tries = []struct{ network, src string }{{"ip4:icmp", "0.0.0.0"}, {"udp4", "0.0.0.0"}}
	} else {
		tries = []struct{ network, src string }{{"ip6:ipv6-icmp", "::"}, {"udp6", "::"}}
	}

	var lastErr error
	for _, t := range tries {
		rtt, err := icmpOnce(t.network, t.src, ip, timeout)
		if err == nil {
			mode := "icmp"
			if t.network == "udp4" || t.network == "udp6" {
				mode = "icmp-udp"
			}
			return rtt, mode, nil
		}
		lastErr = err
	}
	return 0, "", lastErr
}

// icmpOnce 在单个 socket 上完成 echo 请求 + 等待匹配应答。
func icmpOnce(network, src string, ip net.IP, timeout time.Duration) (time.Duration, error) {
	conn, err := icmp.ListenPacket(network, src)
	if err != nil {
		return 0, fmt.Errorf("listen %s: %w", network, err)
	}
	defer conn.Close()

	isV4 := network == "ip4:icmp" || network == "udp4"
	isUDP := network == "udp4" || network == "udp6"
	proto := 58
	var msgType icmp.Type
	if isV4 {
		proto = 1
		msgType = ipv4.ICMPTypeEcho
	} else {
		msgType = ipv6.ICMPTypeEchoRequest
	}

	id := os.Getpid() & 0xffff
	seq := rand.Intn(1 << 15)
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload, uint64(time.Now().UnixNano()))

	m := icmp.Message{Type: msgType, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}
	var wb []byte
	if network == "udp6" {
		// UDP ICMPv6 需要用户计算校验和（伪首部）
		la := conn.LocalAddr().(*net.UDPAddr)
		ph := make([]byte, 44)
		copy(ph[0:16], la.IP.To16())
		copy(ph[16:32], ip.To16())
		binary.BigEndian.PutUint32(ph[32:36], uint32(len(payload)+8))
		ph[39] = 58
		wb, err = m.Marshal(ph)
	} else {
		wb, err = m.Marshal(nil)
	}
	if err != nil {
		return 0, err
	}

	var dst net.Addr
	if isUDP {
		dst = &net.UDPAddr{IP: ip}
	} else {
		dst = &net.IPAddr{IP: ip}
	}

	sentAt := time.Now()
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return 0, fmt.Errorf("timeout waiting echo reply")
			}
			return 0, err
		}
		msg, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		if isV4 && msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		if !isV4 && msg.Type != ipv6.ICMPTypeEchoReply {
			continue
		}
		if echo.Seq != seq {
			continue
		}
		// UDP socket 由内核按 id 分发，无需比对；raw socket 需比对 id
		if !isUDP && echo.ID != id {
			continue
		}
		return time.Since(sentAt), nil
	}
}
