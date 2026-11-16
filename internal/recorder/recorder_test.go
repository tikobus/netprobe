package recorder

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/prober"
	"netprobe/internal/store"
)

func TestRecordWritesDBAndFile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.Default()
	cfg.Log.Dir = filepath.Join(dir, "logs")
	r := New(&cfg, st, slog.Default())
	defer r.Close()

	dev := &store.Device{Name: "d1", Type: "tcp", Method: "tcp", Target: "127.0.0.1:80",
		Interval: 30, Timeout: 5, Retries: 0, Enabled: true}
	if err := st.CreateDevice(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	res := &prober.Result{Success: true, LatencyMS: 3.21, Detail: "connected",
		Extra: map[string]any{"mode": "tcp"}}

	if err := r.Record(context.Background(), dev, res); err != nil {
		t.Fatalf("record: %v", err)
	}

	// DB
	logs, err := st.ListProbeLogs(context.Background(), store.LogFilter{DeviceID: 1})
	if err != nil || len(logs) != 1 {
		t.Fatalf("db logs: %v len=%d", err, len(logs))
	}
	if !logs[0].Success || logs[0].LatencyMS != 3.21 {
		t.Errorf("db log mismatch: %+v", logs[0])
	}

	// 文件
	day := time.Now().Format("20060102")
	b, err := os.ReadFile(filepath.Join(cfg.Log.Dir, "probe-"+day+".log"))
	if err != nil {
		t.Fatalf("log file missing: %v", err)
	}
	line := string(b)
	for _, want := range []string{`"device_id":1`, `"name":"d1"`, `"success":true`, `"mode":"tcp"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %s: %s", want, line)
		}
	}
}

func TestCleanupOldFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "probe-20200101.log")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, time.Now().Format("probe-20060102")+".log")
	if err := os.WriteFile(fresh, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Log.Dir = dir
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	r := New(&cfg, st, slog.Default())
	r.cleanup()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old log file should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh log file should be kept")
	}
}
