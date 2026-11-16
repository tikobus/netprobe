package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func validDevice() Device {
	return Device{
		Name: "Google DNS", Type: "host", Target: "8.8.8.8",
		Interval: 30, Timeout: 5, Retries: 2, Enabled: true, Tags: "dns,google",
	}
}

func TestDeviceCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create：method 留空应默认为 host -> ping
	d := validDevice()
	if err := s.CreateDevice(ctx, &d); err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.ID == 0 || d.Method != "ping" {
		t.Errorf("create result: id=%d method=%q", d.ID, d.Method)
	}
	if d.CreatedAt.IsZero() {
		t.Error("created_at should be set")
	}

	// Get
	got, err := s.GetDevice(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != d.Name || got.Method != "ping" {
		t.Errorf("get mismatch: %+v", got)
	}

	// List
	list, err := s.ListDevices(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v (len=%d)", err, len(list))
	}

	// Update
	got.Name = "renamed"
	got.Enabled = false
	if err := s.UpdateDevice(ctx, got.ID, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := s.GetDevice(ctx, d.ID)
	if again.Name != "renamed" || again.Enabled {
		t.Errorf("update not applied: %+v", again)
	}

	// Delete
	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetDevice(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expect ErrNotFound, got %v", err)
	}
	if err := s.DeleteDevice(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete expect ErrNotFound, got %v", err)
	}
}

func TestDeviceValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(*Device)
	}{
		{"bad type", func(d *Device) { d.Type = "router" }},
		{"bad method", func(d *Device) { d.Method = "snmp" }},
		{"empty target", func(d *Device) { d.Target = "  " }},
		{"zero interval", func(d *Device) { d.Interval = 0 }},
		{"bad expect json", func(d *Device) { d.Expect = "{oops" }},
		{"negative retries", func(d *Device) { d.Retries = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDevice()
			tc.mut(&d)
			err := s.CreateDevice(ctx, &d)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expect ErrInvalid, got %v", err)
			}
		})
	}
}

func TestProbeLogLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	d := validDevice()
	if err := s.CreateDevice(ctx, &d); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	older := now.Add(-48 * time.Hour)
	logs := []ProbeLog{
		{DeviceID: d.ID, Success: true, LatencyMS: 12.5, Detail: "ok", ProbedAt: older},
		{DeviceID: d.ID, Success: false, LatencyMS: 5000, Detail: "timeout", ProbedAt: now},
		{DeviceID: d.ID, Success: true, LatencyMS: 13, Detail: "ok", ProbedAt: now},
	}
	for i := range logs {
		if err := s.InsertProbeLog(ctx, &logs[i]); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// 全量（倒序）
	all, err := s.ListProbeLogs(ctx, LogFilter{DeviceID: d.ID})
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: %v len=%d", err, len(all))
	}
	if all[0].ProbedAt.Unix() != now.Unix() {
		t.Errorf("expect newest first, got %v", all[0].ProbedAt)
	}

	// 仅失败
	fail := false
	fails, err := s.ListProbeLogs(ctx, LogFilter{DeviceID: d.ID, Success: &fail})
	if err != nil || len(fails) != 1 || fails[0].Detail != "timeout" {
		t.Fatalf("filter success=false: %v %+v", err, fails)
	}

	// 时间范围（不含 48h 前那条）
	rangeLogs, err := s.ListProbeLogs(ctx, LogFilter{DeviceID: d.ID, From: now.Add(-time.Hour)})
	if err != nil || len(rangeLogs) != 2 {
		t.Fatalf("range filter: %v len=%d", err, len(rangeLogs))
	}

	// 清理 24h 前的数据：应删除 1 条
	n, err := s.DeleteProbeLogsBefore(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("cleanup: %v n=%d", err, n)
	}
	rest, _ := s.ListProbeLogs(ctx, LogFilter{DeviceID: d.ID})
	if len(rest) != 2 {
		t.Errorf("after cleanup expect 2, got %d", len(rest))
	}

	// 删除设备应级联删除日志
	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	rest, _ = s.ListProbeLogs(ctx, LogFilter{})
	if len(rest) != 0 {
		t.Errorf("cascade delete failed, %d logs left", len(rest))
	}
}
