package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"netprobe/internal/config"
	"netprobe/internal/store"
)

// 简化：直接构造 handler 的测试助手
type testEnv struct {
	s *Server
	h http.Handler
}

func newEnv(t *testing.T) *testEnv {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	s := New(&cfg, st, slog.Default(), nil)
	return &testEnv{s: s, h: s.Routes()}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func TestDeviceCRUDFlow(t *testing.T) {
	e := newEnv(t)

	// 创建（带默认值填充）
	rec := e.do(t, "POST", "/api/devices", map[string]any{
		"name": "web-1", "type": "http", "target": "https://example.com",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var d store.Device
	decode(t, rec, &d)
	if d.Method != "curl" || d.Interval != 30 || !d.Enabled {
		t.Errorf("defaults not applied: %+v", d)
	}

	// 非法类型 -> 400
	rec = e.do(t, "POST", "/api/devices", map[string]any{"name": "x", "type": "router", "target": "1.1.1.1"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid type: expect 400, got %d", rec.Code)
	}

	// 列表
	rec = e.do(t, "GET", "/api/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var list []store.Device
	decode(t, rec, &list)
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}

	// 单个查询
	path := fmt.Sprintf("/api/devices/%d", d.ID)
	rec = e.do(t, "GET", path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}

	// 更新
	rec = e.do(t, "PUT", path, map[string]any{
		"name": "web-1", "type": "http", "target": "https://example.com/health", "interval": 60,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var updated store.Device
	decode(t, rec, &updated)
	if updated.Interval != 60 {
		t.Errorf("update not applied: %+v", updated)
	}

	// 不存在 -> 404
	rec = e.do(t, "GET", "/api/devices/999", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: expect 404, got %d", rec.Code)
	}

	// 删除 -> 204
	rec = e.do(t, "DELETE", path, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec = e.do(t, "GET", path, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("after delete expect 404, got %d", rec.Code)
	}
}

func TestBasicAuth(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Server.BasicAuth.Enabled = true
	h := New(&cfg, st, slog.Default(), nil).Routes()

	// 无凭证 -> 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/devices", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cred: expect 401, got %d", rec.Code)
	}

	// 正确凭证 -> 200
	req := httptest.NewRequest("GET", "/api/devices", nil)
	req.SetBasicAuth("admin", "change-me")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with cred: expect 200, got %d", rec.Code)
	}
}

func TestMalformedJSON(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest("POST", "/api/devices", strings.NewReader("{broken"))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed json: expect 400, got %d", rec.Code)
	}
}
