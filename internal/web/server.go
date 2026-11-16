package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/scheduler"
	"netprobe/internal/store"
	"netprobe/internal/webui"
)

// Version 由构建时注入（-ldflags "-X netprobe/internal/web.Version=xxx"）。
var Version = "0.1.0"

type Server struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	sched *scheduler.Scheduler // 可为 nil（未启动探活引擎时）
}

func New(cfg *config.Config, st *store.Store, log *slog.Logger, sched *scheduler.Scheduler) *Server {
	return &Server{cfg: cfg, store: st, log: log, sched: sched}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(webui.StaticFS()))))

	mux.HandleFunc("GET /api/devices", s.handleListDevices)
	mux.HandleFunc("POST /api/devices", s.handleCreateDevice)
	mux.HandleFunc("GET /api/devices/{id}", s.handleGetDevice)
	mux.HandleFunc("PUT /api/devices/{id}", s.handleUpdateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.handleDeleteDevice)
	mux.HandleFunc("POST /api/devices/{id}/test", s.handleTestDevice)

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/report/overview", s.handleReportOverview)
	mux.HandleFunc("GET /api/logs", s.handleListLogs)

	return s.withBasicAuth(mux)
}

// ---------- 中间件 ----------

func (s *Server) withBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz 不鉴权：供 Docker / Kubernetes / 负载均衡探活使用
		if r.URL.Path == "/healthz" || !s.cfg.Server.BasicAuth.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != s.cfg.Server.BasicAuth.Username || p != s.cfg.Server.BasicAuth.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="netprobe"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 基础 ----------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleIndex 返回内嵌 Web UI 首页。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webui.IndexHTML())
}

// ---------- 设备 CRUD ----------

// deviceRequest 创建/更新设备的请求体，零值字段将应用配置默认值。
type deviceRequest struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Method   string `json:"method"`
	Target   string `json:"target"`
	Interval int    `json:"interval"`
	Timeout  int    `json:"timeout"`
	Retries  *int   `json:"retries"` // nil 时用默认值，显式 0 表示不重试
	Expect   string `json:"expect"`
	Enabled  *bool  `json:"enabled"`
	Tags     string `json:"tags"`
}

func (req *deviceRequest) toDevice(p config.ProberConfig) store.Device {
	d := store.Device{
		Name:     req.Name,
		Type:     req.Type,
		Method:   req.Method,
		Target:   req.Target,
		Interval: req.Interval,
		Timeout:  req.Timeout,
		Retries:  p.DefaultRetries,
		Expect:   req.Expect,
		Enabled:  true,
		Tags:     req.Tags,
	}
	if req.Retries != nil {
		d.Retries = *req.Retries
	}
	if req.Enabled != nil {
		d.Enabled = *req.Enabled
	}
	if d.Interval <= 0 {
		d.Interval = p.DefaultInterval
	}
	if d.Timeout <= 0 {
		d.Timeout = p.DefaultTimeout
	}
	if d.Retries < 0 {
		d.Retries = p.DefaultRetries
	}
	return d
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.log.Error("list devices failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if devices == nil {
		devices = []*store.Device{}
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	d := req.toDevice(s.cfg.Prober)
	if err := s.store.CreateDevice(r.Context(), &d); err != nil {
		s.mapStoreError(w, err)
		return
	}
	s.kickScheduler()
	s.log.Info("device created", "id", d.ID, "name", d.Name, "target", d.Target)
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetDevice(r.Context(), id)
	if err != nil {
		s.mapStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req deviceRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	d := req.toDevice(s.cfg.Prober)
	if err := s.store.UpdateDevice(r.Context(), id, &d); err != nil {
		s.mapStoreError(w, err)
		return
	}
	s.kickScheduler()
	s.log.Info("device updated", "id", d.ID, "name", d.Name)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteDevice(r.Context(), id); err != nil {
		s.mapStoreError(w, err)
		return
	}
	s.kickScheduler()
	s.log.Info("device deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 立即探测 / 状态 ----------

// handleTestDevice 同步探测一次并返回结果（已写入日志）。
func (s *Server) handleTestDevice(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		writeError(w, http.StatusServiceUnavailable, "探活引擎未启动")
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	res, err := s.sched.TestNow(r.Context(), id)
	if err != nil {
		s.mapStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// deviceStatus 设备 + 最近一次探测结果。
type deviceStatus struct {
	store.Device
	LastProbe *store.ProbeLog `json:"last_probe,omitempty"`
}

// handleStatus 返回全部设备及其当前状态。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.log.Error("list devices failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	latest, err := s.store.LatestProbeLogs(r.Context())
	if err != nil {
		s.log.Error("latest logs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	items := make([]deviceStatus, 0, len(devices))
	for _, d := range devices {
		items = append(items, deviceStatus{Device: *d, LastProbe: latest[d.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(items), "items": items})
}

// ---------- 报表 ----------

// overviewItem 单设备的可用率统计。
type overviewItem struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Method     string  `json:"method"`
	Target     string  `json:"target"`
	Enabled    bool    `json:"enabled"`
	Up         *bool   `json:"up"`   // 最近一次探测结果（无数据为 null）
	LatencyMS  *float64 `json:"latency_ms"`
	Total      int64   `json:"total"`   // 窗口内探测次数
	Success    int64   `json:"success"` // 窗口内成功次数
	AvailPct   float64 `json:"avail_pct"` // 窗口内可用率 0~100（无数据为 -1）
	AvgLatency float64 `json:"avg_latency_ms"`
}

// handleReportOverview 总览报表：设备当前状态 + 窗口期可用率。hours 默认 24。
func (s *Server) handleReportOverview(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*30 {
			hours = n
		}
	}
	now := time.Now()

	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.log.Error("list devices failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	latest, err := s.store.LatestProbeLogs(r.Context())
	if err != nil {
		s.log.Error("latest logs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	stats, err := s.store.AvailabilityStats(r.Context(), now.Add(-time.Duration(hours)*time.Hour), now)
	if err != nil {
		s.log.Error("availability stats failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	statByID := make(map[int64]*store.AvailStat, len(stats))
	for _, st := range stats {
		statByID[st.DeviceID] = st
	}

	items := make([]overviewItem, 0, len(devices))
	var upCnt, downCnt int
	for _, d := range devices {
		it := overviewItem{
			ID: d.ID, Name: d.Name, Type: d.Type, Method: d.Method,
			Target: d.Target, Enabled: d.Enabled, AvailPct: -1,
		}
		if l, ok := latest[d.ID]; ok {
			up := l.Success
			lat := l.LatencyMS
			it.Up = &up
			it.LatencyMS = &lat
			if up {
				upCnt++
			} else if d.Enabled {
				downCnt++
			}
		}
		if st, ok := statByID[d.ID]; ok && st.Total > 0 {
			it.Total = st.Total
			it.Success = st.Success
			it.AvailPct = float64(st.Success) / float64(st.Total) * 100
			it.AvgLatency = st.AvgLatency
		}
		items = append(items, it)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window_hours": hours,
		"total":        len(items),
		"up":           upCnt,
		"down":         downCnt,
		"items":        items,
	})
}

// ---------- 探测日志查询 ----------

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{Limit: 100, Offset: 0}
	if v := q.Get("device_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			writeError(w, http.StatusBadRequest, "device_id 非法")
			return
		}
		f.DeviceID = id
	}
	if v := q.Get("result"); v == "success" || v == "failure" {
		b := v == "success"
		f.Success = &b
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.Offset = n
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	logs, err := s.store.ListProbeLogs(r.Context(), f)
	if err != nil {
		s.log.Error("list logs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if logs == nil {
		logs = []*store.ProbeLog{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(logs), "items": logs})
}

// ---------- 工具 ----------

// kickScheduler 设备变更后通知调度器重载（未启动引擎时忽略）。
func (s *Server) kickScheduler() {
	if s.sched != nil {
		s.sched.Kick()
	}
}

func (s *Server) mapStoreError(w http.ResponseWriter, err error) {
	switch err {
	case store.ErrNotFound:
		writeError(w, http.StatusNotFound, "device not found")
	default:
		if _, ok := err.(interface{ Unwrap() error }); ok {
			// 保底：校验类错误
		}
		if containsInvalid(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Error("store error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func containsInvalid(err error) bool {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if err.Error() == "invalid input" {
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id 非法")
		return 0, false
	}
	return id, true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "请求体 JSON 解析失败: "+err.Error())
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
