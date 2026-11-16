package prober

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"netprobe/internal/store"
)

// curlProber HTTP(S) 探活：GET 目标 URL，校验状态码与响应体关键字。
type curlProber struct{}

func (c *curlProber) Name() string { return "curl" }

type curlExpect struct {
	// 允许的状态码列表，为空时默认 200~399
	StatusCodes []int `json:"status_codes"`
	// 响应体必须包含的关键字（前 1MB 内检索）
	BodyContains string `json:"body_contains"`
	// 跳过 TLS 证书校验（自签名场景）
	InsecureTLS bool `json:"insecure_tls"`
}

type traceTimings struct {
	redirects int
	dns       time.Duration
	connect   time.Duration
	tls       time.Duration

	dnsStart    time.Time
	connStart   time.Time
	tlsStart    time.Time
	doStart     time.Time
	ttfb        time.Duration
}

func (t *traceTimings) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { t.dnsStart = time.Now() },
		DNSDone:  func(httptrace.DNSDoneInfo) { t.dns = time.Since(t.dnsStart) },
		ConnectStart: func(_, _ string) {
			if t.connStart.IsZero() {
				t.connStart = time.Now()
			}
		},
		ConnectDone: func(_, _ string, err error) {
			if err == nil && t.connect == 0 {
				t.connect = time.Since(t.connStart)
			}
		},
		TLSHandshakeStart: func() { t.tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.tls = time.Since(t.tlsStart)
			}
		},
		GotFirstResponseByte: func() { t.ttfb = time.Since(t.doStart) },
	}
}

func (c *curlProber) Probe(ctx context.Context, d *store.Device) Result {
	start := time.Now()
	var exp curlExpect
	parseExpect(d.Expect, &exp)

	url := strings.TrimSpace(d.Target)
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}

	tr := &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: exp.InsecureTLS},
	}
	defer tr.CloseIdleConnections()
	tm := &traceTimings{doStart: start}
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			tm.redirects++
			return nil
		},
	}

	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(ctx, tm.trace()), http.MethodGet, url, nil)
	if err != nil {
		return newResult(false, time.Since(start), "bad url: "+err.Error(), nil)
	}
	req.Header.Set("User-Agent", "netprobe")

	resp, err := client.Do(req)
	if err != nil {
		return newResult(false, time.Since(start), err.Error(), nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	extra := map[string]any{
		"status_code": resp.StatusCode,
		"redirects":   tm.redirects,
		"dns_ms":      ms(tm.dns),
		"connect_ms":  ms(tm.connect),
		"tls_ms":      ms(tm.tls),
		"ttfb_ms":     ms(tm.ttfb),
		"final_url":   resp.Request.URL.String(),
	}

	codeOK := false
	if len(exp.StatusCodes) == 0 {
		codeOK = resp.StatusCode >= 200 && resp.StatusCode < 400
	} else {
		for _, c := range exp.StatusCodes {
			if resp.StatusCode == c {
				codeOK = true
				break
			}
		}
	}
	if !codeOK {
		return newResult(false, time.Since(start),
			fmt.Sprintf("unexpected status %d", resp.StatusCode), extra)
	}
	if exp.BodyContains != "" && !bytes.Contains(body, []byte(exp.BodyContains)) {
		extra["body_snippet"] = snippet(body)
		return newResult(false, time.Since(start),
			fmt.Sprintf("body not contains %q", exp.BodyContains), extra)
	}
	return newResult(true, time.Since(start),
		fmt.Sprintf("HTTP %d", resp.StatusCode), extra)
}

func snippet(b []byte) string {
	const n = 120
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
