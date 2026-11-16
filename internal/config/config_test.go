package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with empty path: %v", err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Prober.Concurrency != 32 {
		t.Errorf("default concurrency = %d, want 32", cfg.Prober.Concurrency)
	}
}

func TestLoadFileOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  addr: ":9999"
prober:
  default_interval: 60
log:
  level: DEBUG
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != ":9999" {
		t.Errorf("addr = %q, want :9999", cfg.Server.Addr)
	}
	if cfg.Prober.DefaultInterval != 60 {
		t.Errorf("interval = %d, want 60", cfg.Prober.DefaultInterval)
	}
	// 未覆盖的项保留默认值
	if cfg.Storage.DBPath != "data/netprobe.db" {
		t.Errorf("db_path = %q, want default", cfg.Storage.DBPath)
	}
	// level 大小写归一
	if cfg.Log.Level != "debug" {
		t.Errorf("level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	if _, err := Load("/nonexistent/config.yaml"); err == nil {
		t.Fatal("expect error for missing file")
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"bad level", func(c *Config) { c.Log.Level = "verbose" }},
		{"empty addr", func(c *Config) { c.Server.Addr = " " }},
		{"basic auth no password", func(c *Config) {
			c.Server.BasicAuth.Enabled = true
			c.Server.BasicAuth.Password = ""
		}},
		{"zero concurrency", func(c *Config) { c.Prober.Concurrency = 0 }},
		{"retain days", func(c *Config) { c.Log.RetainDays = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("expect validation error for %s", tc.name)
			}
		})
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("NETPROBE_ADDR", ":7777")
	t.Setenv("NETPROBE_DB_PATH", "/tmp/x.db")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":7777" || cfg.Storage.DBPath != "/tmp/x.db" {
		t.Errorf("env override failed: %+v", cfg)
	}
}
