package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrInvalid  = errors.New("invalid input")
)

// 设备类型 -> 默认探活方式
var defaultMethodByType = map[string]string{
	"host": "ping",
	"http": "curl",
	"tcp":  "tcp",
	"dns":  "dns",
}

var validMethods = map[string]bool{
	"ping": true, "curl": true, "tcp": true, "dns": true,
}

// Device 被监控设备。
type Device struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`   // host | http | tcp | dns
	Method    string    `json:"method"` // ping | curl | tcp | dns
	Target    string    `json:"target"` // IP / URL / host:port / 域名
	Interval  int       `json:"interval"`
	Timeout   int       `json:"timeout"`
	Retries   int       `json:"retries"`
	Expect    string    `json:"expect"` // 可选，JSON：状态码/关键字等
	Enabled   bool      `json:"enabled"`
	Tags      string    `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// ProbeLog 一次探测的结果记录。
type ProbeLog struct {
	ID        int64     `json:"id"`
	DeviceID  int64     `json:"device_id"`
	Success   bool      `json:"success"`
	LatencyMS float64   `json:"latency_ms"`
	Detail    string    `json:"detail"`
	ProbedAt  time.Time `json:"probed_at"`
}

type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并执行建表迁移。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// 单连接串行化写，避免 database is locked
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS device (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  type       TEXT NOT NULL,
  method     TEXT NOT NULL,
  target     TEXT NOT NULL,
  interval   INTEGER NOT NULL DEFAULT 30,
  timeout    INTEGER NOT NULL DEFAULT 5,
  retries    INTEGER NOT NULL DEFAULT 2,
  expect     TEXT NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  tags       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS probe_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id  INTEGER NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  success    INTEGER NOT NULL,
  latency_ms REAL NOT NULL DEFAULT 0,
  detail     TEXT NOT NULL DEFAULT '',
  probed_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_probe_log_dev_time ON probe_log(device_id, probed_at);
`)
	return err
}

// ---------- Device CRUD ----------

func normalizeDevice(d *Device) error {
	d.Name = strings.TrimSpace(d.Name)
	d.Type = strings.ToLower(strings.TrimSpace(d.Type))
	d.Method = strings.ToLower(strings.TrimSpace(d.Method))
	d.Target = strings.TrimSpace(d.Target)
	d.Tags = strings.TrimSpace(d.Tags)
	d.Expect = strings.TrimSpace(d.Expect)

	def, typeOK := defaultMethodByType[d.Type]
	if !typeOK {
		return fmt.Errorf("type 必须是 host/http/tcp/dns 之一，当前: %q", d.Type)
	}
	if d.Method == "" {
		d.Method = def
	}
	if !validMethods[d.Method] {
		return fmt.Errorf("method 必须是 ping/curl/tcp/dns 之一，当前: %q", d.Method)
	}
	if d.Target == "" {
		return fmt.Errorf("target 不能为空")
	}
	if d.Interval < 1 {
		return fmt.Errorf("interval 必须 >= 1 秒")
	}
	if d.Timeout < 1 {
		return fmt.Errorf("timeout 必须 >= 1 秒")
	}
	if d.Retries < 0 {
		return fmt.Errorf("retries 必须 >= 0")
	}
	if d.Expect != "" && !json.Valid([]byte(d.Expect)) {
		return fmt.Errorf("expect 必须是合法 JSON")
	}
	return nil
}

const deviceCols = `id, name, type, method, target, interval, timeout, retries, expect, enabled, tags, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var enabled int
	var created int64
	if err := row.Scan(&d.ID, &d.Name, &d.Type, &d.Method, &d.Target, &d.Interval, &d.Timeout,
		&d.Retries, &d.Expect, &enabled, &d.Tags, &created); err != nil {
		return nil, err
	}
	d.Enabled = enabled == 1
	d.CreatedAt = time.Unix(created, 0)
	return &d, nil
}

func (s *Store) CreateDevice(ctx context.Context, d *Device) error {
	if err := normalizeDevice(d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO device (name, type, method, target, interval, timeout, retries, expect, enabled, tags, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		d.Name, d.Type, d.Method, d.Target, d.Interval, d.Timeout, d.Retries, d.Expect, d.Enabled, d.Tags, now)
	if err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	created, err := s.GetDevice(ctx, id)
	if err != nil {
		return err
	}
	*d = *created
	return nil
}

func (s *Store) GetDevice(ctx context.Context, id int64) (*Device, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM device WHERE id = ?`, id)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *Store) ListDevices(ctx context.Context) ([]*Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM device ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateDevice 全量更新（PUT 语义），id 之外以传入值为准。
func (s *Store) UpdateDevice(ctx context.Context, id int64, d *Device) error {
	if err := normalizeDevice(d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE device SET name=?, type=?, method=?, target=?, interval=?, timeout=?, retries=?, expect=?, enabled=?, tags=?
		 WHERE id=?`,
		d.Name, d.Type, d.Method, d.Target, d.Interval, d.Timeout, d.Retries, d.Expect, d.Enabled, d.Tags, id)
	if err != nil {
		return fmt.Errorf("update device: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	updated, err := s.GetDevice(ctx, id)
	if err != nil {
		return err
	}
	*d = *updated
	return nil
}

func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM device WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- ProbeLog ----------

func (s *Store) InsertProbeLog(ctx context.Context, l *ProbeLog) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO probe_log (device_id, success, latency_ms, detail, probed_at) VALUES (?,?,?,?,?)`,
		l.DeviceID, l.Success, l.LatencyMS, l.Detail, l.ProbedAt.Unix())
	if err != nil {
		return fmt.Errorf("insert probe_log: %w", err)
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// LogFilter 日志查询过滤条件。
type LogFilter struct {
	DeviceID int64     // 0 = 全部设备
	Success  *bool     // nil = 全部结果
	From     time.Time // 零值 = 不限
	To       time.Time
	Limit    int // <=0 时默认 100，上限 1000
	Offset   int
}

func (s *Store) ListProbeLogs(ctx context.Context, f LogFilter) ([]*ProbeLog, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}
	var sb strings.Builder
	sb.WriteString(`SELECT id, device_id, success, latency_ms, detail, probed_at FROM probe_log WHERE 1=1`)
	args := []any{}
	if f.DeviceID > 0 {
		sb.WriteString(` AND device_id=?`)
		args = append(args, f.DeviceID)
	}
	if f.Success != nil {
		sb.WriteString(` AND success=?`)
		args = append(args, *f.Success)
	}
	if !f.From.IsZero() {
		sb.WriteString(` AND probed_at>=?`)
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		sb.WriteString(` AND probed_at<=?`)
		args = append(args, f.To.Unix())
	}
	sb.WriteString(` ORDER BY id DESC LIMIT ` + strconv.Itoa(f.Limit) + ` OFFSET ` + strconv.Itoa(f.Offset))

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProbeLog
	for rows.Next() {
		var l ProbeLog
		var success int
		var ts int64
		if err := rows.Scan(&l.ID, &l.DeviceID, &success, &l.LatencyMS, &l.Detail, &ts); err != nil {
			return nil, err
		}
		l.Success = success == 1
		l.ProbedAt = time.Unix(ts, 0)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// LatestProbeLogs 返回每个设备最近一次探测结果（device_id -> log）。
func (s *Store) LatestProbeLogs(ctx context.Context) (map[int64]*ProbeLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.device_id, l.success, l.latency_ms, l.detail, l.probed_at
		FROM probe_log l
		JOIN (SELECT device_id, MAX(id) AS mid FROM probe_log GROUP BY device_id) m ON l.id = m.mid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]*ProbeLog)
	for rows.Next() {
		var l ProbeLog
		var success int
		var ts int64
		if err := rows.Scan(&l.ID, &l.DeviceID, &success, &l.LatencyMS, &l.Detail, &ts); err != nil {
			return nil, err
		}
		l.Success = success == 1
		l.ProbedAt = time.Unix(ts, 0)
		out[l.DeviceID] = &l
	}
	return out, rows.Err()
}

// AvailabilityStats 返回设备在 [from, to] 内的成功事数与总次数。
type AvailStat struct {
	DeviceID int64
	Total    int64
	Success  int64
	AvgLatency float64 // 成功探测的平均延迟 ms
}

func (s *Store) AvailabilityStats(ctx context.Context, from, to time.Time) ([]*AvailStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT device_id, COUNT(*), SUM(success), AVG(CASE WHEN success=1 THEN latency_ms END)
		FROM probe_log WHERE probed_at >= ? AND probed_at <= ?
		GROUP BY device_id`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AvailStat
	for rows.Next() {
		var st AvailStat
		var succ sql.NullInt64
		var avg sql.NullFloat64
		if err := rows.Scan(&st.DeviceID, &st.Total, &succ, &avg); err != nil {
			return nil, err
		}
		if succ.Valid {
			st.Success = succ.Int64
		}
		if avg.Valid {
			st.AvgLatency = avg.Float64
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}

// DeleteProbeLogsBefore 清理 before 之前的探测记录，返回删除条数。
func (s *Store) DeleteProbeLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM probe_log WHERE probed_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
